package chainreg

import (
	"fmt"
	"sync"

	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg/chainhash"
	"github.com/pkt-cash/pktd/lnd/chainntnfs"
	"github.com/pkt-cash/pktd/lnd/chainntnfs/neutrinonotify"
	"github.com/pkt-cash/pktd/lnd/channeldb"
	"github.com/pkt-cash/pktd/lnd/htlcswitch"
	"github.com/pkt-cash/pktd/lnd/input"
	"github.com/pkt-cash/pktd/lnd/keychain"
	"github.com/pkt-cash/pktd/lnd/lncfg"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/lnd/lnwallet"
	"github.com/pkt-cash/pktd/lnd/lnwallet/btcwallet"
	"github.com/pkt-cash/pktd/lnd/lnwallet/chainfee"
	"github.com/pkt-cash/pktd/lnd/lnwire"
	"github.com/pkt-cash/pktd/lnd/routing/chainview"
	"github.com/pkt-cash/pktd/neutrino"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
)

// Config houses necessary fields that a chainControl instance needs to
// function.
type Config struct {
	// Bitcoin defines settings for the Bitcoin chain.
	Bitcoin *lncfg.Chain

	// Litecoin defines settings for the Litecoin chain.
	Litecoin *lncfg.Chain

	Pkt *lncfg.Chain

	// PrimaryChain is a function that returns our primary chain via its
	// ChainCode.
	PrimaryChain func() ChainCode

	// HeightHintCacheQueryDisable is a boolean that disables height hint
	// queries if true.
	HeightHintCacheQueryDisable bool

	// NeutrinoMode defines settings for connecting to a neutrino light-client.
	NeutrinoMode *lncfg.Neutrino

	// LocalChanDB is a pointer to the local backing channel database.
	LocalChanDB *channeldb.DB

	// RemoteChanDB is a pointer to the remote backing channel database.
	RemoteChanDB *channeldb.DB

	// Wallet is a pointer to the backing wallet instance.
	Wallet *wallet.Wallet

	// NeutrinoCS is a pointer to a neutrino ChainService. Must be non-nil if
	// using neutrino.
	NeutrinoCS *neutrino.ChainService

	// ActiveNetParams details the current chain we are on.
	ActiveNetParams BitcoinNetParams

	// FeeURL defines the URL for fee estimation we will use. This field is
	// optional.
	FeeURL string
}

const (
	// DefaultBitcoinMinHTLCInMSat is the default smallest value htlc this
	// node will accept. This value is proposed in the channel open sequence
	// and cannot be changed during the life of the channel. It is 1 msat by
	// default to allow maximum flexibility in deciding what size payments
	// to forward.
	//
	// All forwarded payments are subjected to the min htlc constraint of
	// the routing policy of the outgoing channel. This implicitly controls
	// the minimum htlc value on the incoming channel too.
	DefaultBitcoinMinHTLCInMSat = lnwire.MilliSatoshi(1)

	// DefaultBitcoinMinHTLCOutMSat is the default minimum htlc value that
	// we require for sending out htlcs. Our channel peer may have a lower
	// min htlc channel parameter, but we - by default - don't forward
	// anything under the value defined here.
	DefaultBitcoinMinHTLCOutMSat = lnwire.MilliSatoshi(1000)

	// DefaultBitcoinBaseFeeMSat is the default forwarding base fee.
	DefaultBitcoinBaseFeeMSat = lnwire.MilliSatoshi(1000)

	// DefaultBitcoinFeeRate is the default forwarding fee rate.
	DefaultBitcoinFeeRate = lnwire.MilliSatoshi(1)

	// DefaultBitcoinTimeLockDelta is the default forwarding time lock
	// delta.
	DefaultBitcoinTimeLockDelta = 40

	DefaultLitecoinMinHTLCInMSat  = lnwire.MilliSatoshi(1)
	DefaultLitecoinMinHTLCOutMSat = lnwire.MilliSatoshi(1000)
	DefaultLitecoinBaseFeeMSat    = lnwire.MilliSatoshi(1000)
	DefaultLitecoinFeeRate        = lnwire.MilliSatoshi(1)
	DefaultLitecoinTimeLockDelta  = 576
	DefaultLitecoinDustLimit      = btcutil.Amount(54600)

	DefaultPktMinHTLCInMSat  = lnwire.MilliSatoshi(1)
	DefaultPktMinHTLCOutMSat = lnwire.MilliSatoshi(1000)
	DefaultPktBaseFeeMSat    = lnwire.MilliSatoshi(1000)
	DefaultPktFeeRate        = lnwire.MilliSatoshi(1)
	DefaultPktTimeLockDelta  = 576
	DefaultPktDustLimit      = btcutil.Amount(54600)

	// DefaultBitcoinStaticFeePerKW is the fee rate of 50 sat/vbyte
	// expressed in sat/kw.
	DefaultBitcoinStaticFeePerKW = chainfee.SatPerKWeight(12500)

	// DefaultBitcoinStaticMinRelayFeeRate is the min relay fee used for
	// static estimators.
	DefaultBitcoinStaticMinRelayFeeRate = chainfee.FeePerKwFloor

	// DefaultLitecoinStaticFeePerKW is the fee rate of 200 sat/vbyte
	// expressed in sat/kw.
	DefaultLitecoinStaticFeePerKW = chainfee.SatPerKWeight(50000)

	DefaultPktStaticFeePerKW = chainfee.SatPerKWeight(1000)

	// BtcToLtcConversionRate is a fixed ratio used in order to scale up
	// payments when running on the Litecoin chain.
	BtcToLtcConversionRate = 60
)

// DefaultBtcChannelConstraints is the default set of channel constraints that are
// meant to be used when initially funding a Bitcoin channel.
//
// TODO(halseth): make configurable at startup?
var DefaultBtcChannelConstraints = channeldb.ChannelConstraints{
	DustLimit:        lnwallet.DefaultDustLimit(),
	MaxAcceptedHtlcs: input.MaxHTLCNumber / 2,
}

// DefaultLtcChannelConstraints is the default set of channel constraints that are
// meant to be used when initially funding a Litecoin channel.
var DefaultLtcChannelConstraints = channeldb.ChannelConstraints{
	DustLimit:        DefaultLitecoinDustLimit,
	MaxAcceptedHtlcs: input.MaxHTLCNumber / 2,
}

// ChainControl couples the three primary interfaces lnd utilizes for a
// particular chain together. A single ChainControl instance will exist for all
// the chains lnd is currently active on.
type ChainControl struct {
	// ChainIO represents an abstraction over a source that can query the blockchain.
	ChainIO lnwallet.BlockChainIO

	// HealthCheck is a function which can be used to send a low-cost, fast
	// query to the chain backend to ensure we still have access to our
	// node.
	HealthCheck func() er.R

	// FeeEstimator is used to estimate an optimal fee for transactions important to us.
	FeeEstimator chainfee.Estimator

	// Signer is used to provide signatures over things like transactions.
	Signer input.Signer

	// KeyRing represents a set of keys that we have the private keys to.
	KeyRing keychain.SecretKeyRing

	// Wc is an abstraction over some basic wallet commands. This base set of commands
	// will be provided to the Wallet *LightningWallet raw pointer below.
	Wc lnwallet.WalletController

	// MsgSigner is used to sign arbitrary messages.
	MsgSigner lnwallet.MessageSigner

	// ChainNotifier is used to receive blockchain events that we are interested in.
	ChainNotifier chainntnfs.ChainNotifier

	// ChainView is used in the router for maintaining an up-to-date graph.
	ChainView chainview.FilteredChainView

	// Wallet is our LightningWallet that also contains the abstract Wc above. This wallet
	// handles all of the lightning operations.
	Wallet *lnwallet.LightningWallet

	// RoutingPolicy is the routing policy we have decided to use.
	RoutingPolicy htlcswitch.ForwardingPolicy

	// MinHtlcIn is the minimum HTLC we will accept.
	MinHtlcIn lnwire.MilliSatoshi
}

// NewChainControl attempts to create a ChainControl instance according
// to the parameters in the passed configuration. Currently three
// branches of ChainControl instances exist: one backed by a running btcd
// full-node, another backed by a running bitcoind full-node, and the other
// backed by a running neutrino light client instance. When running with a
// neutrino light client instance, `neutrinoCS` must be non-nil.
func NewChainControl(cfg *Config, api *apiv1.Apiv1) (*ChainControl, er.R) {

	// Set the RPC config from the "home" chain. Multi-chain isn't yet
	// active, so we'll restrict usage to a particular chain for now.
	homeChainConfig := cfg.Bitcoin
	if cfg.PrimaryChain() == LitecoinChain {
		homeChainConfig = cfg.Litecoin
	}
	if cfg.PrimaryChain() == PktChain {
		homeChainConfig = cfg.Pkt
	}
	log.Infof("Primary chain is set to: %v",
		cfg.PrimaryChain())

	cc := &ChainControl{}

	switch cfg.PrimaryChain() {
	case BitcoinChain:
		cc.RoutingPolicy = htlcswitch.ForwardingPolicy{
			MinHTLCOut:    cfg.Bitcoin.MinHTLCOut,
			BaseFee:       cfg.Bitcoin.BaseFee,
			FeeRate:       cfg.Bitcoin.FeeRate,
			TimeLockDelta: cfg.Bitcoin.TimeLockDelta,
		}
		cc.MinHtlcIn = cfg.Bitcoin.MinHTLCIn
		cc.FeeEstimator = chainfee.NewStaticEstimator(
			DefaultBitcoinStaticFeePerKW,
			DefaultBitcoinStaticMinRelayFeeRate,
		)
	case LitecoinChain:
		cc.RoutingPolicy = htlcswitch.ForwardingPolicy{
			MinHTLCOut:    cfg.Litecoin.MinHTLCOut,
			BaseFee:       cfg.Litecoin.BaseFee,
			FeeRate:       cfg.Litecoin.FeeRate,
			TimeLockDelta: cfg.Litecoin.TimeLockDelta,
		}
		cc.MinHtlcIn = cfg.Litecoin.MinHTLCIn
		cc.FeeEstimator = chainfee.NewStaticEstimator(
			DefaultLitecoinStaticFeePerKW, 0,
		)
	case PktChain:
		cc.RoutingPolicy = htlcswitch.ForwardingPolicy{
			MinHTLCOut:    cfg.Pkt.MinHTLCOut,
			BaseFee:       cfg.Pkt.BaseFee,
			FeeRate:       cfg.Pkt.FeeRate,
			TimeLockDelta: cfg.Pkt.TimeLockDelta,
		}
		cc.MinHtlcIn = cfg.Pkt.MinHTLCIn
		cc.FeeEstimator = chainfee.NewStaticEstimator(
			DefaultPktStaticFeePerKW, 0,
		)
	default:
		return nil, er.Errorf("default routing policy for chain %v is "+
			"unknown", cfg.PrimaryChain())
	}

	walletConfig := &btcwallet.Config{
		DataDir:   homeChainConfig.ChainDir,
		NetParams: cfg.ActiveNetParams.Params,
		CoinType:  cfg.ActiveNetParams.CoinType,
		Wallet:    cfg.Wallet,
	}

	var err er.R

	heightHintCacheConfig := chainntnfs.CacheConfig{
		QueryDisable: cfg.HeightHintCacheQueryDisable,
	}
	if cfg.HeightHintCacheQueryDisable {
		log.Infof("Height Hint Cache Queries disabled")
	}

	// Initialize the height hint cache within the chain directory.
	hintCache, err := chainntnfs.NewHeightHintCache(
		heightHintCacheConfig, cfg.LocalChanDB,
	)
	if err != nil {
		return nil, er.Errorf("unable to initialize height hint "+
			"cache: %v", err)
	}

	// Setup neutrino
	// We'll create ChainNotifier and FilteredChainView instances,
	// along with the wallet's ChainSource, which are all backed by
	// the neutrino light client.
	cc.ChainNotifier = neutrinonotify.New(
		cfg.NeutrinoCS, hintCache, hintCache,
	)
	cc.ChainView, err = chainview.NewCfFilteredChainView(cfg.NeutrinoCS)
	if err != nil {
		return nil, err
	}

	// Map the deprecated neutrino feeurl flag to the general fee
	// url.
	if cfg.NeutrinoMode.FeeURL != "" {
		if cfg.FeeURL != "" {
			return nil, er.New("feeurl and " +
				"neutrino.feeurl are mutually exclusive")
		}

		cfg.FeeURL = cfg.NeutrinoMode.FeeURL
	}

	walletConfig.ChainSource = cfg.NeutrinoCS

	// Get our best block as a health check.
	cc.HealthCheck = func() er.R {
		_, err := walletConfig.ChainSource.BestBlock()
		return err
	}
	// End setup neutrino

	// Override default fee estimator if an external service is specified.
	if cfg.FeeURL != "" {
		// Do not cache fees on regtest to make it easier to execute
		// manual or automated test cases.
		cacheFees := !cfg.Bitcoin.RegTest

		log.Infof("Using external fee estimator %v: cached=%v",
			cfg.FeeURL, cacheFees)

		cc.FeeEstimator = chainfee.NewWebAPIEstimator(
			chainfee.SparseConfFeeSource{
				URL: cfg.FeeURL,
			},
			!cacheFees,
		)
	}

	// Start fee estimator.
	if err := cc.FeeEstimator.Start(); err != nil {
		return nil, err
	}

	wc, err := btcwallet.New(*walletConfig, api)
	if err != nil {
		fmt.Printf("unable to create wallet controller: %v\n", err)
		return nil, err
	}

	cc.MsgSigner = wc
	cc.Signer = wc
	cc.ChainIO = wc
	cc.Wc = wc

	// Select the default channel constraints for the primary chain.
	channelConstraints := DefaultBtcChannelConstraints
	if cfg.PrimaryChain() == LitecoinChain {
		channelConstraints = DefaultLtcChannelConstraints
	}

	keyRing := keychain.NewBtcWalletKeyRing(
		wc.InternalWallet(), cfg.ActiveNetParams.CoinType,
	)
	cc.KeyRing = keyRing

	// Create, and start the lnwallet, which handles the core payment
	// channel logic, and exposes control via proxy state machines.
	walletCfg := lnwallet.Config{
		Database:           cfg.RemoteChanDB,
		Notifier:           cc.ChainNotifier,
		WalletController:   wc,
		Signer:             cc.Signer,
		FeeEstimator:       cc.FeeEstimator,
		SecretKeyRing:      keyRing,
		ChainIO:            cc.ChainIO,
		DefaultConstraints: channelConstraints,
		NetParams:          *cfg.ActiveNetParams.Params,
	}
	lnWallet, err := lnwallet.NewLightningWallet(walletCfg)
	if err != nil {
		fmt.Printf("unable to create wallet: %v\n", err)
		return nil, err
	}
	if err := lnWallet.Startup(); err != nil {
		fmt.Printf("unable to start wallet: %v\n", err)
		return nil, err
	}

	log.Info("LightningWallet opened")

	cc.Wallet = lnWallet

	return cc, nil
}

var (
	// BitcoinTestnetGenesis is the genesis hash of Bitcoin's testnet
	// chain.
	BitcoinTestnetGenesis = chainhash.Hash([chainhash.HashSize]byte{
		0x43, 0x49, 0x7f, 0xd7, 0xf8, 0x26, 0x95, 0x71,
		0x08, 0xf4, 0xa3, 0x0f, 0xd9, 0xce, 0xc3, 0xae,
		0xba, 0x79, 0x97, 0x20, 0x84, 0xe9, 0x0e, 0xad,
		0x01, 0xea, 0x33, 0x09, 0x00, 0x00, 0x00, 0x00,
	})

	// BitcoinMainnetGenesis is the genesis hash of Bitcoin's main chain.
	BitcoinMainnetGenesis = chainhash.Hash([chainhash.HashSize]byte{
		0x6f, 0xe2, 0x8c, 0x0a, 0xb6, 0xf1, 0xb3, 0x72,
		0xc1, 0xa6, 0xa2, 0x46, 0xae, 0x63, 0xf7, 0x4f,
		0x93, 0x1e, 0x83, 0x65, 0xe1, 0x5a, 0x08, 0x9c,
		0x68, 0xd6, 0x19, 0x00, 0x00, 0x00, 0x00, 0x00,
	})

	// LitecoinTestnetGenesis is the genesis hash of Litecoin's testnet4
	// chain.
	LitecoinTestnetGenesis = chainhash.Hash([chainhash.HashSize]byte{
		0xa0, 0x29, 0x3e, 0x4e, 0xeb, 0x3d, 0xa6, 0xe6,
		0xf5, 0x6f, 0x81, 0xed, 0x59, 0x5f, 0x57, 0x88,
		0x0d, 0x1a, 0x21, 0x56, 0x9e, 0x13, 0xee, 0xfd,
		0xd9, 0x51, 0x28, 0x4b, 0x5a, 0x62, 0x66, 0x49,
	})

	// LitecoinMainnetGenesis is the genesis hash of Litecoin's main chain.
	LitecoinMainnetGenesis = chainhash.Hash([chainhash.HashSize]byte{
		0xe2, 0xbf, 0x04, 0x7e, 0x7e, 0x5a, 0x19, 0x1a,
		0xa4, 0xef, 0x34, 0xd3, 0x14, 0x97, 0x9d, 0xc9,
		0x98, 0x6e, 0x0f, 0x19, 0x25, 0x1e, 0xda, 0xba,
		0x59, 0x40, 0xfd, 0x1f, 0xe3, 0x65, 0xa7, 0x12,
	})

	// chainMap is a simple index that maps a chain's genesis hash to the
	// ChainCode enum for that chain.
	chainMap = map[chainhash.Hash]ChainCode{
		BitcoinTestnetGenesis:  BitcoinChain,
		LitecoinTestnetGenesis: LitecoinChain,

		BitcoinMainnetGenesis:  BitcoinChain,
		LitecoinMainnetGenesis: LitecoinChain,
	}

	// ChainDNSSeeds is a map of a chain's hash to the set of DNS seeds
	// that will be use to bootstrap peers upon first startup.
	//
	// The first item in the array is the primary host we'll use to attempt
	// the SRV lookup we require. If we're unable to receive a response
	// over UDP, then we'll fall back to manual TCP resolution. The second
	// item in the array is a special A record that we'll query in order to
	// receive the IP address of the current authoritative DNS server for
	// the network seed.
	//
	// TODO(roasbeef): extend and collapse these and chainparams.go into
	// struct like chaincfg.Params
	ChainDNSSeeds = map[chainhash.Hash][][2]string{
		BitcoinMainnetGenesis: {
			{
				"nodes.lightning.directory",
				"soa.nodes.lightning.directory",
			},
			{
				"lseed.bitcoinstats.com",
			},
		},

		BitcoinTestnetGenesis: {
			{
				"test.nodes.lightning.directory",
				"soa.nodes.lightning.directory",
			},
		},

		LitecoinMainnetGenesis: {
			{
				"ltc.nodes.lightning.directory",
				"soa.nodes.lightning.directory",
			},
		},

		*PktMainNetParams.GenesisHash: {
			{
				"pkt.lseed.pkteer.com",
				"pkt.lseed.cjd.li",
			},
		},
	}
)

// ChainRegistry keeps track of the current chains
type ChainRegistry struct {
	sync.RWMutex

	activeChains map[ChainCode]*ChainControl
	netParams    map[ChainCode]*BitcoinNetParams

	primaryChain ChainCode
}

// NewChainRegistry creates a new ChainRegistry.
func NewChainRegistry() *ChainRegistry {
	return &ChainRegistry{
		activeChains: make(map[ChainCode]*ChainControl),
		netParams:    make(map[ChainCode]*BitcoinNetParams),
	}
}

// RegisterChain assigns an active ChainControl instance to a target chain
// identified by its ChainCode.
func (c *ChainRegistry) RegisterChain(newChain ChainCode,
	cc *ChainControl) {

	c.Lock()
	c.activeChains[newChain] = cc
	c.Unlock()
}

// LookupChain attempts to lookup an active ChainControl instance for the
// target chain.
func (c *ChainRegistry) LookupChain(targetChain ChainCode) (
	*ChainControl, bool) {

	c.RLock()
	cc, ok := c.activeChains[targetChain]
	c.RUnlock()
	return cc, ok
}

// LookupChainByHash attempts to look up an active ChainControl which
// corresponds to the passed genesis hash.
func (c *ChainRegistry) LookupChainByHash(chainHash chainhash.Hash) (*ChainControl, bool) {
	c.RLock()
	defer c.RUnlock()

	targetChain, ok := chainMap[chainHash]
	if !ok {
		return nil, ok
	}

	cc, ok := c.activeChains[targetChain]
	return cc, ok
}

// RegisterPrimaryChain sets a target chain as the "home chain" for lnd.
func (c *ChainRegistry) RegisterPrimaryChain(cc ChainCode) {
	c.Lock()
	defer c.Unlock()

	c.primaryChain = cc
}

// PrimaryChain returns the primary chain for this running lnd instance. The
// primary chain is considered the "home base" while the other registered
// chains are treated as secondary chains.
func (c *ChainRegistry) PrimaryChain() ChainCode {
	c.RLock()
	defer c.RUnlock()

	return c.primaryChain
}

// ActiveChains returns a slice containing the active chains.
func (c *ChainRegistry) ActiveChains() []ChainCode {
	c.RLock()
	defer c.RUnlock()

	chains := make([]ChainCode, 0, len(c.activeChains))
	for activeChain := range c.activeChains {
		chains = append(chains, activeChain)
	}

	return chains
}

// NumActiveChains returns the total number of active chains.
func (c *ChainRegistry) NumActiveChains() uint32 {
	c.RLock()
	defer c.RUnlock()

	return uint32(len(c.activeChains))
}
