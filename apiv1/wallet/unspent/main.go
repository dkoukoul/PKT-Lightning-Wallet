package unspent

import (
	"github.com/pkt-cash/pktd/apiv1/wallet/unspent/lock"
	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/chaincfg/chainhash"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/txscript"
)

type rpc struct {
	w *wallet.Wallet
}

func (r *rpc) ListUnspent(in *rpc_pb.ListUnspentRequest) (*rpc_pb.ListUnspentResponse, er.R) {
	// Validate the confirmation arguments.
	minConfs, maxConfs, err := lnrpc.ParseConfs(in.MinConfs, in.MaxConfs)
	if err != nil {
		return nil, err
	}
	
	unspentOutputs, err := r.w.ListUnspent(minConfs, maxConfs, nil)
	if err != nil {
		return nil, err
	}
	
	witnessOutputs := make([]*rpc_pb.Utxo, 0, len(unspentOutputs))
	for _, output := range unspentOutputs {
		pkScript, err := util.DecodeHex(output.ScriptPubKey)
		if err != nil {
			return nil, err
		}

		//addressType := rpc_pb.UnknownAddressType
		addressType := rpc_pb.AddressType_UNUSED_WITNESS_PUBKEY_HASH
		if txscript.IsPayToWitnessPubKeyHash(pkScript) {
			//addressType = lnwallet.WitnessPubKey
			addressType = rpc_pb.AddressType_WITNESS_PUBKEY_HASH
		} else if txscript.IsPayToScriptHash(pkScript) {
			// TODO(roasbeef): This assumes all p2sh outputs returned by the
			// wallet are nested p2pkh. We can't check the redeem script because
			// the btcwallet service does not include it.
			//addressType = lnwallet.NestedWitnessPubKey
			addressType = rpc_pb.AddressType_NESTED_PUBKEY_HASH
		}

		//if addressType == lnwallet.WitnessPubKey ||
		//	addressType == lnwallet.NestedWitnessPubKey {
		if addressType == rpc_pb.AddressType_WITNESS_PUBKEY_HASH ||
			addressType == rpc_pb.AddressType_NESTED_PUBKEY_HASH {

			txid, err := chainhash.NewHashFromStr(output.TxID)
			if err != nil {
				return nil, err
			}

			// We'll ensure we properly convert the amount given in
			// BTC to satoshis.
			amt, err := btcutil.NewAmount(output.Amount)
			if err != nil {
				return nil, err
			}
			//fill the utxo
			utxo := &rpc_pb.Utxo{
				AddressType: 	addressType,
				AmountSat:      int64(amt),
				Outpoint: &rpc_pb.OutPoint{
					TxidBytes: txid[:],
					TxidStr:  output.TxID,
				},
			}
			witnessOutputs = append(witnessOutputs, utxo)
		}
	}

	return &rpc_pb.ListUnspentResponse{
		Utxos: witnessOutputs,
	}, nil
}


func Register(a *apiv1.Apiv1, w *wallet.Wallet) {
	r := rpc{w: w}
	lock.Register(
		apiv1.DefineCategory(a, "lock",
		`
		Unspent outputs which are locked

		Locking of unspent outputs prevent them from being used as funding for transaction/create
		or transaction/sendcoins, etc. This is useful when creating multiple transactions which are
		not sent to the chain (yet). Locking the outputs will prevent each subsequent transaction
		from trying to source the same funds, making mutually invalid transactions.

		Locked outputs can be grouped with "named" locks, so that they can be unlocked as a group.
		This is useful when one transaction may source many unspent outputs, they can be locked
		with the name/purpose of that transaction.
		`),
		w,
	)
	apiv1.Endpoint(
		a,
		"",
		`
		List utxos available for spending

		ListUnspent returns a list of all utxos spendable by the wallet with a
		number of confirmations between the specified minimum and maximum.
		`,
		r.ListUnspent,
	)
}
