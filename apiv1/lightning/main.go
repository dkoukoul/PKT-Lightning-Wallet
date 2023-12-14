package lightning

import (
	"time"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util/mailbox"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/walletunlocker_pb"
	"github.com/pkt-cash/pktd/lnd/chanbackup"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/neutrino"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
)

// ChannelsToRecover wraps any set of packed (serialized+encrypted) channel
// back ups together. These can be passed in when unlocking the wallet, or
// creating a new wallet for the first time with an existing seed.
type ChannelsToRecover struct {
	// PackedMultiChanBackup is an encrypted and serialized multi-channel
	// backup.
	PackedMultiChanBackup chanbackup.PackedMulti

	// PackedSingleChanBackups is a series of encrypted and serialized
	// single-channel backup for one or more channels.
	PackedSingleChanBackups chanbackup.PackedSingles
}

type StartLightning struct {
	StartupComplete   mailbox.Mailbox[bool]
	ChannelsToRestore ChannelsToRecover
}

type rpc struct {
	w              *wallet.Wallet
	neutrinoCS     *neutrino.ChainService
	startLightning *mailbox.Mailbox[*StartLightning]
}

func (r *rpc) start(in *walletunlocker_pb.StartLightningRequest) (*rpc_pb.Null, er.R) {
	walletPassphrase := []byte(in.WalletPassphrase)
	if len(in.WalletPassphraseBin) > 0 {
		walletPassphrase = in.WalletPassphraseBin
	}

	// Relock because the wallet might have a lock timeout
	for !r.w.Locked() {
		r.w.Lock()
		time.Sleep(time.Millisecond)
	}

	if err := r.w.Unlock(walletPassphrase, nil); err != nil {
		return nil, err
	}

	sl := StartLightning{
		StartupComplete:   mailbox.NewMailbox(false),
		ChannelsToRestore: extractChanBackups(in.ChannelBackups),
	}
	complete := mailbox.NewMailbox(false)

	r.startLightning.Store(&sl)

	complete.AwaitUpdate()

	return nil, nil
}

func Register(
	a *apiv1.Apiv1,
	w *wallet.Wallet,
	neutrinoCS *neutrino.ChainService,
	startLightning *mailbox.Mailbox[*StartLightning],
) {
	r := rpc{
		w:              w,
		neutrinoCS:     neutrinoCS,
		startLightning: startLightning,
	}
	apiv1.Endpoint(
		a,
		"start",
		`
		Launch the Lightning daemon, requires unlocking the wallet indefinitely.
		`,
		r.start,
	)
}
