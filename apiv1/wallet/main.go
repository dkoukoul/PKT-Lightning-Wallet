package wallet

import (
	"strings"
	"time"

	"github.com/pkt-cash/pktd/apiv1/lightning"
	"github.com/pkt-cash/pktd/apiv1/wallet/address"
	"github.com/pkt-cash/pktd/apiv1/wallet/transaction"
	"github.com/pkt-cash/pktd/apiv1/wallet/unspent"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util/mailbox"
	"github.com/pkt-cash/pktd/generated/proto/meta_pb"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/walletunlocker_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/lnd/lnwallet"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
)

type rpc struct {
	w              *wallet.Wallet
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

func (r *rpc) getsecret(in *rpc_pb.GetSecretRequest) (*rpc_pb.GetSecretResponse, er.R) {
	ptrsecret, err := r.w.GetSecret(in.Name)
	secret := ""
	if ptrsecret != nil {
		secret = *ptrsecret
	}
	if err != nil {
		return nil, er.New("Unlock wallet first to get secret")
	}
	return &rpc_pb.GetSecretResponse {
		Secret: secret,
	}, nil
}

func (r *rpc) seed(req *rpc_pb.GetWalletSeedRequest) (*rpc_pb.GetWalletSeedResponse, er.R) {
	seed := r.w.Manager.Seed()
	if seed == nil {
		return nil, er.New("No seed found, this is probably a legacy wallet")
	}
	words, err := seed.Words("english")
	if err != nil {
		return nil, err
	}
	return &rpc_pb.GetWalletSeedResponse{
		Seed: strings.Split(words, " "),
	}, nil
}

// ChangePassphrase changes the password of the wallet and sends the new password
// across the UnlockPasswords channel to automatically unlock the wallet if
// successful.
func (r *rpc) changepassphrase(in *meta_pb.ChangePasswordRequest) (*rpc_pb.Null, er.R) {

	//	fetch current wallet passphrase from request
	var walletPassphrase []byte

	if len(in.CurrentPasswordBin) > 0 {
		walletPassphrase = in.CurrentPasswordBin
	} else {
		if len(in.CurrentPassphrase) > 0 {
			walletPassphrase = []byte(in.CurrentPassphrase)
		} else {
			// If the current password is blank, we'll assume the user is coming
			// from a --noseedbackup state, so we'll use the default passwords.
			walletPassphrase = []byte(lnwallet.DefaultPrivatePassphrase)
		}
	}

	//	fetch new wallet passphrase from request
	var newWalletPassphrase []byte

	if len(in.NewPassphraseBin) > 0 {
		newWalletPassphrase = in.NewPassphraseBin
	} else {
		if len(in.NewPassphrase) > 0 {
			newWalletPassphrase = []byte(in.NewPassphrase)
		} else {
			newWalletPassphrase = []byte(lnwallet.DefaultPrivatePassphrase)
		}
	}

	publicPw := []byte(wallet.InsecurePubPassphrase)
	newPubPw := []byte(wallet.InsecurePubPassphrase)

	// Attempt to change both the public and private passphrases for the
	// wallet. This will be done atomically in order to prevent one
	// passphrase change from being successful and not the other.
	err := r.w.ChangePassphrases(
		publicPw, newPubPw, walletPassphrase, newWalletPassphrase,
	)
	if err != nil {
		return nil, er.Errorf("unable to change wallet passphrase: "+
			"%v", err)
	}

	return nil, nil
}

// WalletBalance returns total unspent outputs(confirmed and unconfirmed), all
// confirmed unspent outputs and all unconfirmed unspent outputs under control
// by the wallet. This method can be modified by having the request specify
// only witness outputs should be factored into the final output sum.
// TODO(roasbeef): add async hooks into wallet balance changes
func (r *rpc) balance(*rpc_pb.Null) (*rpc_pb.WalletBalanceResponse, er.R) {

	// Get total balance, from txs that have >= 0 confirmations.
	totalBal, err := r.w.CalculateBalance(0)
	if err != nil {
		return nil, err
	}

	// Get confirmed balance, from txs that have >= 1 confirmations.
	// TODO(halseth): get both unconfirmed and confirmed balance in one
	// call, as this is racy.
	confirmedBal, err := r.w.CalculateBalance(1)
	if err != nil {
		return nil, err
	}

	// Get unconfirmed balance, from txs with 0 confirmations.
	unconfirmedBal := totalBal - confirmedBal

	log.Debugf("[walletbalance] Total balance=%v (confirmed=%v, "+
		"unconfirmed=%v)", totalBal, confirmedBal, unconfirmedBal)

	return &rpc_pb.WalletBalanceResponse{
		TotalBalance:       int64(totalBal),
		ConfirmedBalance:   int64(confirmedBal),
		UnconfirmedBalance: int64(unconfirmedBal),
	}, nil
}

func (r *rpc) checkpassphrase(in *meta_pb.CheckPasswordRequest) (*meta_pb.CheckPasswordResponse, er.R) {

	//	fetch current wallet passphrase from request
	var walletPassphrase []byte

	if len(in.WalletPasswordBin) > 0 {
		walletPassphrase = in.WalletPasswordBin
	} else {
		if len(in.WalletPassphrase) > 0 {
			walletPassphrase = []byte(in.WalletPassphrase)
		} else {
			// If the current password is blank, we'll assume the user is coming
			// from a --noseedbackup state, so we'll use the default passwords.
			walletPassphrase = []byte(lnwallet.DefaultPrivatePassphrase)
		}
	}

	publicPw := []byte(wallet.InsecurePubPassphrase)
	validPassphrase := false

	//	attempt to check the private passphrases for the wallet.
	err := r.w.CheckPassphrase(publicPw, walletPassphrase)
	if err != nil {
		log.Info("CheckPassphrase failed, incorect passphrase")
	} else {
		log.Info("CheckPassphrase success, correct passphrase")
		validPassphrase = true
	}

	return &meta_pb.CheckPasswordResponse{
		ValidPassphrase: validPassphrase,
	}, nil
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
	unspent.Register(
		apiv1.DefineCategory(walletCat, "unspent",
			"Detected unspent transactions associated with one of our wallet addresses"),
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
	apiv1.Endpoint(
		walletCat,
		"getsecret",
		`
		Get a secret seed which is generated using the wallet's private key, this can be used as a password for another application
		`,
		r.getsecret,
	)
	apiv1.Endpoint(
		walletCat,
		"seed",
		`
		Get the wallet seed words for this wallet

    	Get the wallet seed words for this wallet, this seed is returned in an
		ENCRYPTED form (using the wallet passphrase as key). The output is 15 words.
		`,
		r.seed,
	)
	apiv1.Endpoint(
		walletCat,
		"balance",
		`
		Compute and display the wallet's current balance

		WalletBalance returns total unspent outputs(confirmed and unconfirmed), all
		confirmed unspent outputs and all unconfirmed unspent outputs under control
		of the wallet.
		`,
		r.balance,
	)
	apiv1.Endpoint(
		walletCat,
		"changepassphrase",
		`
		Change an encrypted wallet's password at startup

		ChangePassword changes the password of the encrypted wallet. This will
		automatically unlock the wallet database if successful.
		`,
		r.changepassphrase,
	)
	apiv1.Endpoint(
		walletCat,
		"checkpassphrase",
		`
		Check the wallet's password

    	CheckPassword verify that the password in the request is valid for the wallet.
		`,
		r.checkpassphrase,
	)

}
