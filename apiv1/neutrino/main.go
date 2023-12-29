package neutrino

import (
	"bytes"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/wire"
)

type rpc struct {
	w *wallet.Wallet
}

func (r *rpc) bcasttransaction(in *rpc_pb.BcastTransactionRequest) (*rpc_pb.BcastTransactionResponse, er.R) {
	var msgTx wire.MsgTx

	err := msgTx.Deserialize(bytes.NewReader(in.Tx))
	if err != nil {
		return nil, err
	}

	txidhash, err := r.w.ChainClient().SendRawTransaction(&msgTx, true)
	if err != nil {
		return nil, err
	}

	return &rpc_pb.BcastTransactionResponse{
		TxnHash: txidhash.String(),
	}, err
}

func Register(
	a *apiv1.Apiv1,
	w *wallet.Wallet,
) {
	r := rpc{w: w}
	apiv1.Endpoint(
		a,
		"bcasttransaction",
		`
		Broadcast a transaction to the network

		Broadcast a transaction to the network so it can be logged in the chain.
		`,
		r.bcasttransaction,
	)
}
