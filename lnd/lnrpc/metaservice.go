package lnrpc

import (
	"context"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg"
	"github.com/pkt-cash/pktd/generated/proto/meta_pb"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnwallet"
	"github.com/pkt-cash/pktd/neutrino"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
)

type MetaService struct {
	Wallet *wallet.Wallet

	noFreelistSync bool
	netParams      *chaincfg.Params

	walletFile string
	walletPath string
}

// New creates and returns a new MetaService
func NewMetaService(neutrino *neutrino.ChainService) *MetaService {
	return &MetaService{}
}

func (m *MetaService) SetWallet(wallet *wallet.Wallet) {
	m.Wallet = wallet
}

func (m *MetaService) Init(
	noFreelistSync bool,
	netParams *chaincfg.Params,
	walletFile string,
	walletPath string,
) {
	m.netParams = netParams
	m.walletFile = walletFile
	m.walletPath = walletPath
}

// ChangePassword changes the password of the wallet and sends the new password
// across the UnlockPasswords channel to automatically unlock the wallet if
// successful.
func (m *MetaService) ChangePassword(ctx context.Context,
	in *meta_pb.ChangePasswordRequest) (*rpc_pb.Null, er.R) {

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

	walletFile := m.walletFile
	if m.Wallet == nil || m.Wallet.Locked() {
		if in.WalletName != "" {
			walletFile = in.WalletName
		}
		loader := wallet.NewLoader(m.netParams, m.walletPath, walletFile, m.noFreelistSync, 0)

		// First, we'll make sure the wallet exists for the specific chain and
		// network.
		walletExists, err := loader.WalletExists()
		if err != nil {
			return nil, err
		}

		if !walletExists {
			return nil, er.New("wallet not found")
		}

		// Load the existing wallet in order to proceed with the password change.
		w, err := loader.OpenExistingWallet(publicPw, false, nil)
		if err != nil {
			return nil, err
		}
		m.Wallet = w
		// Now that we've opened the wallet, we need to close it in case of an
		// error. But not if we succeed, then the caller must close it.
		orderlyReturn := false
		defer func() {
			if !orderlyReturn {
				_ = loader.UnloadWallet()
			}
		}()

		// Before we actually change the password, we need to check if all flags
		// were set correctly. The content of the previously generated macaroon
		// files will become invalid after we generate a new root key. So we try
		// to delete them here and they will be recreated during normal startup
		// later. If they are missing, this is only an error if the
		// stateless_init flag was not set.

	} else if (in.WalletName != "") && (in.WalletName != m.walletFile) {
		walletFile = in.WalletName
		loader := wallet.NewLoader(m.netParams, m.walletPath, walletFile, m.noFreelistSync, 0)

		// First, we'll make sure the wallet exists for the specific chain and
		// network.
		walletExists, err := loader.WalletExists()
		if err != nil {
			return nil, err
		}

		if !walletExists {
			return nil, er.New("wallet not found")
		}

		// Load the existing wallet in order to proceed with the password change.
		w, err := loader.OpenExistingWallet(publicPw, false, nil)
		if err != nil {
			return nil, err
		}
		m.Wallet = w
		// Now that we've opened the wallet, we need to close it in case of an
		// error. But not if we succeed, then the caller must close it.
		orderlyReturn := false
		defer func() {
			if !orderlyReturn {
				_ = loader.UnloadWallet()
			}
		}()
	}

	// Attempt to change both the public and private passphrases for the
	// wallet. This will be done atomically in order to prevent one
	// passphrase change from being successful and not the other.
	err := m.Wallet.ChangePassphrases(
		publicPw, newPubPw, walletPassphrase, newWalletPassphrase,
	)
	if err != nil {
		return nil, er.Errorf("unable to change wallet passphrase: "+
			"%v", err)
	}

	return nil, nil
}

//	CheckPassword just verifies if the password of the wallet is valid, and is
//	meant to be used independent of wallet's state being unlocked or locked.
func (m *MetaService) CheckPassword(
	ctx context.Context,
	req *meta_pb.CheckPasswordRequest,
) (*meta_pb.CheckPasswordResponse, er.R) {

	//	fetch current wallet passphrase from request
	var walletPassphrase []byte

	if len(req.WalletPasswordBin) > 0 {
		walletPassphrase = req.WalletPasswordBin
	} else {
		if len(req.WalletPassphrase) > 0 {
			walletPassphrase = []byte(req.WalletPassphrase)
		} else {
			// If the current password is blank, we'll assume the user is coming
			// from a --noseedbackup state, so we'll use the default passwords.
			walletPassphrase = []byte(lnwallet.DefaultPrivatePassphrase)
		}
	}

	publicPw := []byte(wallet.InsecurePubPassphrase)
	validPassphrase := false
	//	if wallet is locked, temporary unlock it just to check the passphrase
	var walletAux *wallet.Wallet = m.Wallet
	//if wallet_name not passed then try to unlock the default
	walletFile := m.walletFile
	if walletAux == nil || walletAux.Locked() {
		if req.WalletName != "" {
			walletFile = req.WalletName
		}
		loader := wallet.NewLoader(m.netParams, m.walletPath, walletFile, m.noFreelistSync, 0)

		// First, we'll make sure the wallet exists for the specific chain and network.
		walletExists, err := loader.WalletExists()
		if err != nil {
			return nil, err
		}

		if !walletExists {
			return nil, er.New("wallet " + walletFile + " not found")
		}

		// Load the existing wallet in order to proceed with the password change.
		walletAux, err = loader.OpenExistingWallet(publicPw, false, nil)
		if err != nil {
			return nil, err
		}
		log.Info("Wallet " + walletFile + " temporary opened with success")

		// Now that we've opened the wallet, we need to close it before exit
		defer func() {
			if walletAux != m.Wallet {
				_ = loader.UnloadWallet()
				log.Info("Wallet unloaded with success")
			}
		}()
	} else if (req.WalletName != "") && (req.WalletName != m.walletFile) {
		walletFile = req.WalletName
		//wallet is unlocked but not the requested one, so we unlock the wallet_name now
		loader := wallet.NewLoader(m.netParams, m.walletPath, walletFile, m.noFreelistSync, 0)
		// First, we'll make sure the wallet exists for the specific chain and network.
		walletExists, err := loader.WalletExists()
		if err != nil {
			return nil, err
		}

		if !walletExists {
			return nil, er.New("wallet " + walletFile + " not found")
		}

		// Load the existing wallet in order to proceed with the password change.
		walletAux, err = loader.OpenExistingWallet(publicPw, false, nil)
		if err != nil {
			return nil, err
		}
		log.Info("Wallet " + walletFile + " temporary opened with success")

		// Now that we've opened the wallet, we need to close it before exit
		defer func() {
			if walletAux != m.Wallet {
				_ = loader.UnloadWallet()
				log.Info("Wallet unloaded with success")
			}
		}()
	}

	//	attempt to check the private passphrases for the wallet.
	err := walletAux.CheckPassphrase(publicPw, walletPassphrase)
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
