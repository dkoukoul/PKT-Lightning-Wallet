package wallet

import (
	"time"

	"github.com/pkt-cash/pktd/apiv1/lightning"
	"github.com/pkt-cash/pktd/apiv1/wallet/address"
	"github.com/pkt-cash/pktd/apiv1/wallet/transaction"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util/mailbox"
	"github.com/pkt-cash/pktd/chaincfg"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/walletunlocker_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
)

type rpc struct {
	w              *wallet.Wallet
	params         *chaincfg.Params
	startLightning *mailbox.Mailbox[*lightning.StartLightning]
}

func (r *rpc) unlockWallet(in *walletunlocker_pb.UnlockWalletRequest) (*rpc_pb.Null, er.R) {
	var unlockAfter <-chan time.Time
	if in.TimeoutSeconds > 0 {
		unlockAfter = time.After(time.Second * time.Duration(in.TimeoutSeconds))
	}
	pass := []byte(in.WalletPassphrase)
	if len(in.WalletPassphraseBin) > 0 {
		pass = in.WalletPassphraseBin
	}
	return nil, r.w.Unlock(pass, unlockAfter)
}

func (r *rpc) lockWallet(in *rpc_pb.Null) (*rpc_pb.Null, er.R) {
	if r.startLightning.Load() != nil {
		return nil, er.Errorf("Cannot re-lock wallet because the lightning daemon is running")
	}
	r.w.Lock()
	return nil, nil
}

func Register(
	walletCat *apiv1.Apiv1,
	w *wallet.Wallet,
	startLightning *mailbox.Mailbox[*lightning.StartLightning],
) {
	r := rpc{w: w, startLightning: startLightning}
	address.Register(
		apiv1.DefineCategory(walletCat, "address",
			`
			Management of PKT addresses in the wallet

			The root keys of this wallet can be used to derive as many addresses as you need.
			If you recover your wallet from seed, all of the same addresses will derive again
			in the same order. The public does not know that these addresses are linked to the
			same wallet unless you spend from multiple of them in the same transaction.

			Each address can be pay, be paid, hold a balance, and generally be used as it's own
			wallet.
			`,
		), w)
	transaction.Register(
		apiv1.DefineCategory(walletCat, "transaction",
			"Create and manage on-chain transactions with the wallet"),
		w,
	)

	apiv1.Endpoint(
		walletCat,
		"unlock",
		`
		Unlock an encrypted wallet for on-chain transactions.
		`,
		r.unlockWallet,
	)
	apiv1.Endpoint(
		walletCat,
		"lock",
		`
		Lock the wallet, deleting the keys from memory.
		If the lightning daemon has been started then this call will fail.
		`,
		r.lockWallet,
	)
}
