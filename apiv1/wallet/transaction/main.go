package transaction

import (
	"bytes"
	"math"

	"github.com/pkt-cash/pktd/btcjson"
	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/chaincfg/chainhash"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/describetxn"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/pktwallet/waddrmgr"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/pktwallet/wallet/txrules"
	"github.com/pkt-cash/pktd/txscript"
	"github.com/pkt-cash/pktd/wire"
)

type rpc struct {
	w *wallet.Wallet
}

func (r *rpc) decodeRawTransaction(req *rpc_pb.DecodeRawTransactionRequest) (*rpc_pb.TransactionInfo, er.R) {
	// Deserialize the transaction.
	var serializedTx []byte
	if len(req.BinTx) > 0 {
		serializedTx = req.BinTx
	} else {
		hexStr := req.HexTx
		if len(hexStr)%2 != 0 {
			hexStr = "0" + hexStr
		}
		stx, err := util.DecodeHex(hexStr)
		if err != nil {
			return nil, err
		}
		serializedTx = stx
	}

	var mtx wire.MsgTx
	err := mtx.Deserialize(bytes.NewReader(serializedTx))
	if err != nil {
		return nil, err
	}

	txi, err := describetxn.Describe(
		transactionGetter(r.w),
		mtx,
		r.w.ChainParams(),
		req.IncludeVinDetail,
	)
	if err != nil {
		return nil, err
	}
	return txi, nil
}

func (r *rpc) getTransaction(req *rpc_pb.GetTransactionRequest) (*rpc_pb.GetTransactionResponse, er.R) {
	txHash, err := chainhash.NewHashFromStr(req.Txid)
	if err != nil {
		return nil, btcjson.ErrRPCDecodeHexString.New("Transaction hash string decode failed", err)
	}

	details, err := wallet.UnstableAPI(r.w).TxDetails(txHash)
	if err != nil {
		return nil, err
	}
	if details == nil {
		return nil, btcjson.ErrRPCNoTxInfo.Default()
	}

	syncBlock := r.w.Manager.SyncedTo()

	// TODO: The serialized transaction is already in the DB, so
	// reserializing can be avoided here.
	var txBuf bytes.Buffer
	txBuf.Grow(details.MsgTx.SerializeSize())
	err = details.MsgTx.Serialize(&txBuf)
	if err != nil {
		return nil, err
	}

	// TODO: Add a "generated" field to this result type.  "generated":true
	// is only added if the transaction is a coinbase.
	transaction := rpc_pb.TransactionResult{
		Txid:            req.Txid,
		Raw:             txBuf.Bytes(),
		Time:            details.Received.Unix(),
		TimeReceived:    details.Received.Unix(),
		WalletConflicts: []string{},
	}

	if details.Block.Height != -1 {
		transaction.BlockHash = details.Block.Hash.String()
		transaction.BlockTime = details.Block.Time.Unix()
		transaction.Confirmations = int64(confirms(details.Block.Height, syncBlock.Height))
	}

	var (
		debitTotal  btcutil.Amount
		creditTotal btcutil.Amount // Excludes change
		fee         btcutil.Amount
		feeF64      float64
	)
	for _, deb := range details.Debits {
		debitTotal += deb.Amount
	}
	for _, cred := range details.Credits {
		if !cred.Change {
			creditTotal += cred.Amount
		}
	}
	// Fee can only be determined if every input is a debit.
	if len(details.Debits) == len(details.MsgTx.TxIn) {
		var outputTotal btcutil.Amount
		for _, output := range details.MsgTx.TxOut {
			outputTotal += btcutil.Amount(output.Value)
		}
		fee = debitTotal - outputTotal
		feeF64 = fee.ToBTC()
	}

	if len(details.Debits) == 0 {
		// Credits must be set later, but since we know the full length
		// of the details slice, allocate it with the correct cap.
		transaction.Details = make([]*rpc_pb.GetTransactionDetailsResult, 0, len(details.Credits))
	} else {
		transaction.Details = make([]*rpc_pb.GetTransactionDetailsResult, 1, len(details.Credits)+1)

		transaction.Details[0] = &rpc_pb.GetTransactionDetailsResult{
			// Fields left zeroed:
			//   InvolvesWatchOnly
			//   Account
			//   Address
			//   Vout
			//
			// TODO(jrick): Address and Vout should always be set,
			// but we're doing the wrong thing here by not matching
			// core.  Instead, gettransaction should only be adding
			// details for transaction outputs, just like
			// listtransactions (but using the short result format).
			Category:    "send",
			Amount:      (-debitTotal).ToBTC(), // negative since it is a send
			AmountUnits: uint64(debitTotal),
		}
		transaction.Fee = feeF64
		transaction.FeeUnits = uint64(fee)
	}

	credCat := wallet.RecvCategory(details, syncBlock.Height, r.w.ChainParams()).String()
	for _, cred := range details.Credits {
		// Change is ignored.
		if cred.Change {
			continue
		}

		var address string
		_, addrs, _, err := txscript.ExtractPkScriptAddrs(
			details.MsgTx.TxOut[cred.Index].PkScript, r.w.ChainParams())
		if err == nil && len(addrs) == 1 {
			addr := addrs[0]
			address = addr.EncodeAddress()
		}

		transaction.Details = append(transaction.Details, &rpc_pb.GetTransactionDetailsResult{
			// Fields left zeroed:
			//   InvolvesWatchOnly
			//   Fee
			Address:  address,
			Category: credCat,
			Amount:   cred.Amount.ToBTC(),
			Vout:     cred.Index,
		})
	}
	transaction.Amount = creditTotal.ToBTC()
	transaction.AmountUnits = uint64(creditTotal)

	return &rpc_pb.GetTransactionResponse{
		Transaction: &transaction,
	}, nil
}

func (r *rpc) createTransaction(req *rpc_pb.CreateTransactionRequest) (*rpc_pb.CreateTransactionResponse, er.R) {
	toaddress := req.ToAddress
	amount := req.Amount
	fromaddresses := req.FromAddress

	autolock := req.Autolock

	if amount <= 0 {
		return nil, er.New("amount must be positive")
	}
	if math.IsInf(amount, 1) {
		amount = 0
	}
	minconf := int32(req.MinConf)
	if minconf < 0 {
		return nil, er.New("minconf must be positive")
	}
	inputminheight := 0
	if req.InputMinHeight > 0 {
		inputminheight = int(req.InputMinHeight)
	}
	// Create map of address and amount pairs.
	amt, err := btcutil.NewAmount(float64(amount))
	if err != nil {
		return nil, err
	}
	amounts := map[string]btcutil.Amount{
		toaddress: amt,
	}

	var vote *waddrmgr.NetworkStewardVote
	vote, err = r.w.NetworkStewardVote(0, waddrmgr.KeyScopeBIP0044)
	if err != nil {
		return nil, err
	}
	maxinputs := int(req.MaxInputs)
	sendmode := wallet.SendModeSigned
	if !req.Sign {
		sendmode = wallet.SendModeUnsigned
	}
	tx, err := sendOutputs(r.w, amounts, vote, &fromaddresses, minconf, txrules.DefaultRelayFeePerKb, sendmode, &req.ChangeAddress, inputminheight, maxinputs)
	if err != nil {
		return nil, err
	}

	for _, in := range tx.Tx.TxIn {
		op := in.PreviousOutPoint
		r.w.LockOutpoint(op, autolock)
	}

	var transaction []byte
	if req.ElectrumFormat {
		b := new(bytes.Buffer)
		if err := tx.Tx.BtcEncode(b, 0, wire.ForceEptfEncoding); err != nil {
			return nil, err
		}
		transaction = b.Bytes()
	} else {
		b := bytes.NewBuffer(make([]byte, 0, tx.Tx.SerializeSize()))
		if err := tx.Tx.Serialize(b); err != nil {
			return nil, err
		}
		transaction = b.Bytes()
	}

	return &rpc_pb.CreateTransactionResponse{
		Transaction: transaction,
	}, nil
}

func (r *rpc) sendFrom(req *rpc_pb.SendFromRequest) (*rpc_pb.SendFromResponse, er.R) {
	toaddress := req.ToAddress
	amount := req.Amount
	fromaddresses := req.FromAddress

	if amount <= 0 {
		return nil, er.New("amount must be positive")
	}
	if math.IsInf(amount, 1) {
		amount = 0
	}
	minconf := int32(req.MinConf)
	if minconf < 0 {
		return nil, er.New("minconf must be positive")
	}
	minheight := 0
	if req.MinHeight > 0 {
		minheight = int(req.MinHeight)
	}
	// Create map of address and amount pairs.
	amt, err := btcutil.NewAmount(float64(amount))
	if err != nil {
		return nil, err
	}
	amounts := map[string]btcutil.Amount{
		toaddress: amt,
	}

	maxinputs := int(req.MaxInputs)

	tx, err := sendPairs(r.w, amounts, &fromaddresses, minconf, txrules.DefaultRelayFeePerKb, maxinputs, minheight)
	if err != nil {
		return nil, err
	}

	return &rpc_pb.SendFromResponse{
		TxHash: tx,
	}, nil
}

func Register(a *apiv1.Apiv1, w *wallet.Wallet) {
	r := rpc{w: w}
	apiv1.Endpoint(
		a,
		"",
		`
		Get details regarding a transaction

    	Returns a JSON object with details regarding a transaction relevant to this wallet.
		If the transaction is not known to be relevant to at least one address in this wallet
		it will appear as "not found" even if the transaction is real.
		`,
		r.getTransaction,
	)

	apiv1.Endpoint(
		a,
		"create",
		`
		Create a transaction but do not send it to the chain

		This does not store the transaction as existing in the wallet so
		/wallet/transaction/query will not return a transaction created by this
		endpoint. In order to make multiple transactions concurrently, prior to
		the first transaction being submitted to the chain, you must specify the
		autolock field.
		`,
		r.createTransaction,
	)

	apiv1.Endpoint(
		a,
		"sendfrom",
		`
		Authors, signs, and sends a transaction which sources funds from specific addresses

		SendFrom authors, signs, and sends a transaction which sources it's funds
		from specific addresses.
		`,
		r.sendFrom,
	)

	// TODO(cjd): This is not written right, needs to be addressed
	// apiv1.Endpoint(
	// 	walletTransaction,
	// 	"sendmany",
	// 	`
	// 	Send PKT on-chain to multiple addresses

	// 	SendMany handles a request for a transaction that creates multiple specified
	// 	outputs in parallel. If neither target_conf, or sat_per_byte are set, then
	// 	the internal wallet will consult its fee model to determine a fee for the
	// 	default confirmation target.
	// 	`,
	// 	withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.SendManyRequest) (*rpc_pb.SendManyResponse, er.R) {
	// 		return rs.SendMany(context.TODO(), req)
	// 	}),
	// )
	apiv1.Endpoint(
		a,
		"decode",
		`
		Parse a binary representation of a transaction into it's relevant data

		Parse a binary or hex encoded transaction and returns a structured description of it.
		This endpoint also uses information from the wallet, if possible, to fill in additional
		data such as the amounts of the transaction inputs - data which is not present inside of the
		transaction itself. If the relevant data is not in the wallet, some info about the transaction
		will be missing such as input amounts and fees.
		`,
		r.decodeRawTransaction,
	)
}
