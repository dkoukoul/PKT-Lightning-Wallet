package seed

import (
	"strings"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/walletunlocker_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/pktwallet/wallet/seedwords"
)

func create(in *walletunlocker_pb.GenSeedRequest) (*walletunlocker_pb.GenSeedResponse, er.R) {

	if len(in.SeedEntropy) != 0 {
		return nil, er.Errorf("seed input entropy is not supported")
	}

	// Now that we have our set of entropy, we'll create a new cipher seed
	// instance.
	//
	cipherSeed, err := seedwords.RandomSeed()
	if err != nil {
		return nil, err
	}

	//	fetch seed passphrase from request
	var seedPassphrase []byte

	if len(in.SeedPassphraseBin) > 0 {
		seedPassphrase = in.SeedPassphraseBin
	} else {
		if len(in.SeedPassphrase) > 0 {
			seedPassphrase = []byte(in.SeedPassphrase)
		}
	}
	encipheredSeed := cipherSeed.Encrypt(seedPassphrase)

	mnemonic, err := encipheredSeed.Words("english")
	if err != nil {
		return nil, err
	}

	return &walletunlocker_pb.GenSeedResponse{
		Seed: strings.Split(mnemonic, " "),
	}, nil
}

func changepassphrase(
	req *rpc_pb.ChangeSeedPassphraseRequest,
) (*rpc_pb.ChangeSeedPassphraseResponse, er.R) {

	//	get current seed passphrase from request
	//	if both bin and string passphrases are present, the bin have precedence
	var currentSeedCipherPass []byte

	if len(req.CurrentSeedPassphraseBin) > 0 {
		currentSeedCipherPass = req.CurrentSeedPassphraseBin
	} else if len(req.CurrentSeedPassphrase) > 0 {
		currentSeedCipherPass = []byte(req.CurrentSeedPassphrase)
	}

	//	get current seed and decipher it if necessary
	var mnemonic string

	mnemonic = strings.Join(req.CurrentSeed, " ")
	if len(mnemonic) == 0 {
		return nil, er.New("Current seed is required in the request")
	}

	currentSeedCiphered, err := seedwords.SeedFromWords(mnemonic)
	if err != nil {
		return nil, err
	}

	currentSeed, err := currentSeedCiphered.Decrypt(currentSeedCipherPass, false)
	if err != nil {
		return nil, err
	}

	//	get new seed passphrase from request
	//	if both bin and string passphrases are present, the bin have precedence
	var newSeedCipherPass []byte

	if len(req.NewSeedPassphraseBin) > 0 {
		newSeedCipherPass = req.NewSeedPassphraseBin
	} else if len(req.NewSeedPassphrase) > 0 {
		newSeedCipherPass = []byte(req.NewSeedPassphrase)
	}

	//	cipher the seed with the new passphrase
	newCipheredSeed := currentSeed.Encrypt(newSeedCipherPass)

	//	get the mnemonic for the new ciphered seed
	mnemonic, err = newCipheredSeed.Words("english")
	if err != nil {
		return nil, err
	}

	return &rpc_pb.ChangeSeedPassphraseResponse{
		Seed: strings.Split(mnemonic, " "),
	}, nil
}

func Register(
	a *apiv1.Apiv1,
	params *chaincfg.Params,
) {
	apiv1.Endpoint(
		a,
		"create",
		`
		Create a secret seed

		This allows you to statelessly create a new wallet seed.
		This seed can then be used to initialize a wallet.
		`,
		create,
	)
	apiv1.Endpoint(
		a,
		"changepassphrase",
		`
		Alter the passphrase which is used to encrypt a wallet seed

		The old seed words are transformed into a new seed words,
		representing the same seed but encrypted with a different passphrase.
		`,
		changepassphrase,
	)
}
