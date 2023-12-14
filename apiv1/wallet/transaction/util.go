package transaction

import (
	"github.com/pkt-cash/pktd/btcjson"
	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg"
	"github.com/pkt-cash/pktd/chaincfg/chainhash"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/waddrmgr"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/pktwallet/wallet/txauthor"
	"github.com/pkt-cash/pktd/pktwallet/walletdb"
	"github.com/pkt-cash/pktd/txscript"
	"github.com/pkt-cash/pktd/wire"
	"github.com/pkt-cash/pktd/wire/ruleerror"
)

// sendPairs creates and sends payment transactions.
// It returns the transaction hash in string format upon success
// All errors are returned in btcjson.RPCError format
func sendPairs(w *wallet.Wallet, amounts map[string]btcutil.Amount,
	fromAddressses *[]string, minconf int32, feeSatPerKb btcutil.Amount, maxInputs, inputMinHeight int) (string, er.R) {

	vote, err := w.NetworkStewardVote(0, waddrmgr.KeyScopeBIP0044)
	if err != nil {
		return "", err
	}

	tx, err := sendOutputs(w, amounts, vote, fromAddressses, minconf, feeSatPerKb, wallet.SendModeBcasted, nil, inputMinHeight, maxInputs)
	if err != nil {
		return "", err
	}

	txHashStr := tx.Tx.TxHash().String()
	log.Infof("Successfully sent transaction [%s]", log.Txid(txHashStr))
	return txHashStr, nil
}

func sendOutputs(
	w *wallet.Wallet,
	amounts map[string]btcutil.Amount,
	vote *waddrmgr.NetworkStewardVote,
	fromAddressses *[]string,
	minconf int32,
	feeSatPerKb btcutil.Amount,
	sendMode wallet.SendMode,
	changeAddress *string,
	inputMinHeight int,
	maxInputs int,
) (*txauthor.AuthoredTx, er.R) {
	req := wallet.CreateTxReq{
		Minconf:        minconf,
		FeeSatPerKB:    feeSatPerKb,
		SendMode:       sendMode,
		InputMinHeight: inputMinHeight,
		MaxInputs:      maxInputs,
		Label:          "",
	}
	if inputMinHeight > 0 {
		// TODO(cjd): Ideally we would expose the comparator choice to the
		// API consumer, but this is an API break. When we're using inputMinHeight
		// it's normally because we're trying to do multiple createtransaction
		// requests without double-spending, so it's important to prefer oldest
		// in this case.
		req.InputComparator = wallet.PreferOldest
	}
	var err er.R
	req.Outputs, err = makeOutputs(amounts, vote, w.ChainParams())
	if err != nil {
		return nil, err
	}
	if changeAddress != nil && *changeAddress != "" {
		addr, err := btcutil.DecodeAddress(*changeAddress, w.ChainParams())
		if err != nil {
			return nil, err
		}
		req.ChangeAddress = &addr
	}
	if fromAddressses != nil && len(*fromAddressses) > 0 {
		addrs := make([]btcutil.Address, 0, len(*fromAddressses))
		for _, addrStr := range *fromAddressses {
			addr, err := btcutil.DecodeAddress(addrStr, w.ChainParams())
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, addr)
		}
		req.InputAddresses = addrs
	}
	tx, err := w.SendOutputs(req)
	if err != nil {
		if ruleerror.ErrNegativeTxOutValue.Is(err) {
			return nil, er.New("amount must be positive")
		}
		if waddrmgr.ErrLocked.Is(err) {
			return nil, er.New("Enter the wallet passphrase with walletpassphrase first")
		}
		if btcjson.Err.Is(err) {
			return nil, err
		}
		return nil, btcjson.ErrRPCInternal.New("SendOutputs failed", err)
	}
	return tx, nil
}

// makeOutputs creates a slice of transaction outputs from a pair of address
// strings to amounts.  This is used to create the outputs to include in newly
// created transactions from a JSON object describing the output destinations
// and amounts.
func makeOutputs(pairs map[string]btcutil.Amount, vote *waddrmgr.NetworkStewardVote,
	chainParams *chaincfg.Params) ([]*wire.TxOut, er.R) {
	outputs := make([]*wire.TxOut, 0, len(pairs))
	if vote == nil {
		vote = &waddrmgr.NetworkStewardVote{}
	}
	for addrStr, amt := range pairs {
		addr, err := btcutil.DecodeAddress(addrStr, chainParams)
		if err != nil {
			return nil, er.Errorf("cannot decode address: %s", err)
		}

		pkScript, err := txscript.PayToAddrScriptWithVote(addr, vote.VoteFor, vote.VoteAgainst)
		if err != nil {
			return nil, er.Errorf("cannot create txout script: %s", err)
		}

		outputs = append(outputs, wire.NewTxOut(int64(amt), pkScript))
	}
	return outputs, nil
}

func transactionGetter(w *wallet.Wallet) func(txns map[string]*wire.MsgTx) er.R {
	return func(txns map[string]*wire.MsgTx) er.R {
		return walletdb.View(w.Database(), func(dbtx walletdb.ReadTx) er.R {
			txmgrNs := dbtx.ReadBucket([]byte("wtxmgr"))
			for k := range txns {
				tx, err := w.TxStore.TxDetails(txmgrNs, chainhash.MustNewHashFromStr(k))
				if err != nil {
					// TxDetails only returns an error if something actually went wrong
					// not found == nil, nil
					return err
				} else if tx != nil {
					txns[k] = &tx.MsgTx
				}
			}
			return nil
		})
	}
}

// confirms returns the number of confirmations for a transaction in a block at
// height txHeight (or -1 for an unconfirmed tx) given the chain height
// curHeight.
func confirms(txHeight, curHeight int32) int32 {
	switch {
	case txHeight == -1, txHeight > curHeight:
		return 0
	default:
		return curHeight - txHeight + 1
	}
}
