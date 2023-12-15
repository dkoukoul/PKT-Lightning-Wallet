package lock

import (
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg/chainhash"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/wire"
)

type rpc struct {
	w        *wallet.Wallet
}

func (r *rpc) listlockunspent(_ *rpc_pb.Null) (*rpc_pb.ListLockUnspentResponse, er.R) {
	list := r.w.LockedOutpoints()
	lu := make(map[string][]*rpc_pb.OutPoint)
	for _, l := range list {
		lu[l.LockName] = append(lu[l.LockName], &rpc_pb.OutPoint{TxidStr: l.Txid, OutputIndex: l.Vout})
	}
	out := make([]*rpc_pb.LockedUtxos, 0, len(lu))
	for name, ops := range lu {
		out = append(out, &rpc_pb.LockedUtxos{
			LockName: name,
			Utxos:    ops,
		})
	}
	return &rpc_pb.ListLockUnspentResponse{
		LockedUnspents: out,
	}, nil
}

func (r *rpc) lockunspent(in *rpc_pb.LockUnspentRequest) (*rpc_pb.LockUnspentResponse, er.R) {
	w := r.w
	lockname := "none"
	if in.Lockname != "" {
		lockname = in.Lockname
	}
	transactions := in.Transactions
	for _, input := range transactions {
		txHash, err := chainhash.NewHashFromStr(input.TxidStr)
		if err != nil {
			return nil, err
		}
		op := wire.OutPoint{Hash: *txHash, Index: uint32(input.OutputIndex)}
		w.LockOutpoint(op, lockname)
	}
	return nil, nil
}

func (r *rpc) unlockunspent(in *rpc_pb.LockUnspentRequest) (*rpc_pb.Null, er.R) {
	w := r.w
	if in.Lockname != "" {
		w.ResetLockedOutpoints(&in.Lockname)
	}
	transactions := in.Transactions
	for _, input := range transactions {
		txHash, err := chainhash.NewHashFromStr(input.TxidStr)
		if err != nil {
			return nil, err
		}
		op := wire.OutPoint{Hash: *txHash, Index: uint32(input.OutputIndex)}
		w.UnlockOutpoint(op)
	}

	return nil, nil
}

func (r *rpc) unlockallunspent(_ *rpc_pb.Null) (*rpc_pb.Null, er.R) {
	r.w.ResetLockedOutpoints(nil)
	return nil, nil
}

func Register(a *apiv1.Apiv1, w *wallet.Wallet) {
	r := rpc{w: w}
	apiv1.Endpoint(
		a,
		"",
		`
		List utxos which are locked

		Returns an set of outpoints marked as locked by using /wallet/unspent/lock/create
		These are batched by group name.
		`,
		r.listlockunspent,
	)
	apiv1.Endpoint(
		a,
		"create",
		`
		Lock one or more unspent outputs

		You may optionally specify a group name. You may call this endpoint
		multiple times with the same group name to add more unspents to the group.
		NOTE: The lock group name "none" is reserved.
		`,
		r.lockunspent,
	)
	apiv1.Endpoint(
		a,
		"delete",
		`
		Remove one or a group of locks

		If a lock name is specified, all locks with that name will be unlocked
		in addition to all unspents that are specifically identified. If the literal
		word "none" is specified as the lock name, all uncategorized locks will be removed.
		`,
		r.unlockunspent,
	)
	apiv1.Endpoint(
		a,
		"deleteall",
		`
		Remove every lock, including all categories.
		`,
		r.unlockallunspent,
	)
}
