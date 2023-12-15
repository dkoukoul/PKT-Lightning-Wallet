package unspent

import (
	"fmt"
	"math"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/lnd/lnwallet"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/apiv1/wallet/unspent/lock"
)

type rpc struct {
	w *wallet.Wallet
	lnwallet *lnwallet.LightningWallet
}

func (r *rpc) ListUnspent(in *rpc_pb.ListUnspentRequest) (*rpc_pb.ListUnspentResponse, er.R) {
	// Validate the confirmation arguments.
	minConfs, maxConfs, err := lnrpc.ParseConfs(in.MinConfs, in.MaxConfs)
	if err != nil {
		return nil, err
	}

	// With our arguments validated, we'll query the internal wallet for
	// the set of UTXOs that match our query.
	//
	// We'll acquire the global coin selection lock to ensure there aren't
	// any other concurrent processes attempting to lock any UTXOs which may
	// be shown available to us.
	var utxos []*lnwallet.Utxo
	err = r.lnwallet.WithCoinSelectLock(func() er.R {
		utxos, err = r.lnwallet.ListUnspentWitness(
			minConfs, maxConfs,
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	params := r.w.ChainParams()
	rpcUtxos, err := lnrpc.MarshalUtxos(utxos, params)
	if err != nil {
		return nil, err
	}

	maxStr := ""
	if maxConfs != math.MaxInt32 {
		maxStr = " max=" + fmt.Sprintf("%d", maxConfs)
	}

	log.Debugf("[listunspent] min=%v%v, generated utxos: %v", minConfs,
		maxStr, utxos)

	return &rpc_pb.ListUnspentResponse{
		Utxos: rpcUtxos,
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
