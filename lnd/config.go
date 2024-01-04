// Copyright (c) 2013-2017 The btcsuite developers
// Copyright (c) 2015-2016 The Decred developers
// Copyright (C) 2015-2020 The Lightning Network Developers

package lnd

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	flags "github.com/jessevdk/go-flags"
	jsoniter "github.com/json-iterator/go"
	"github.com/pkt-cash/pktd/btcec"
	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg/globalcfg"
	"github.com/pkt-cash/pktd/lnd/autopilot"
	"github.com/pkt-cash/pktd/lnd/chainreg"
	"github.com/pkt-cash/pktd/lnd/chanbackup"
	"github.com/pkt-cash/pktd/lnd/channeldb"
	"github.com/pkt-cash/pktd/lnd/discovery"
	"github.com/pkt-cash/pktd/lnd/htlcswitch"
	"github.com/pkt-cash/pktd/lnd/htlcswitch/hodl"
	"github.com/pkt-cash/pktd/lnd/input"
	"github.com/pkt-cash/pktd/lnd/lncfg"
	"github.com/pkt-cash/pktd/lnd/lnrpc/routerrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/signrpc"
	"github.com/pkt-cash/pktd/lnd/routing"
	"github.com/pkt-cash/pktd/lnd/tor"
	"github.com/pkt-cash/pktd/neutrino"
	"github.com/pkt-cash/pktd/pktconfig/version"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/legacy/keystore"
	"github.com/pkt-cash/pktd/pktwallet/prompt"
	"github.com/pkt-cash/pktd/pktwallet/waddrmgr"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/pktwallet/wallet/seedwords"
	"github.com/pkt-cash/pktd/pktwallet/zero"
)

const (
	defaultDataDirname     = "data"
	defaultChainSubDirname = "chain"
	defaultGraphSubDirname = "graph"
	defaultTowerSubDirname = "watchtower"
	defaultLogLevel        = "info"
	defaultRESTPort        = 8080
	defaultPeerPort        = 9735

	defaultNoSeedBackup                  = false
	defaultPaymentsExpirationGracePeriod = time.Duration(0)
	defaultTrickleDelay                  = 90 * 1000
	defaultChanStatusSampleInterval      = time.Minute
	defaultChanEnableTimeout             = 19 * time.Minute
	defaultChanDisableTimeout            = 20 * time.Minute
	defaultHeightHintCacheQueryDisable   = false
	defaultMinBackoff                    = time.Second
	defaultMaxBackoff                    = time.Hour

	defaultTorSOCKSPort            = 9050
	defaultTorDNSHost              = "soa.nodes.lightning.directory"
	defaultTorDNSPort              = 53
	defaultTorControlPort          = 9051
	defaultTorV2PrivateKeyFilename = "v2_onion_private_key"
	defaultTorV3PrivateKeyFilename = "v3_onion_private_key"

	// minTimeLockDelta is the minimum timelock we require for incoming
	// HTLCs on our channels.
	minTimeLockDelta = routing.MinCLTVDelta

	defaultAlias = ""
	defaultColor = "#3399FF"

	// defaultHostSampleInterval is the default amount of time that the
	// HostAnnouncer will wait between DNS resolutions to check if the
	// backing IP of a host has changed.
	defaultHostSampleInterval = time.Minute * 5

	defaultChainInterval = time.Minute
	defaultChainTimeout  = time.Second * 10
	defaultChainBackoff  = time.Second * 30
	defaultChainAttempts = 3

	// Set defaults for a health check which ensures that we have space
	// available on disk. Although this check is off by default so that we
	// avoid breaking any existing setups (particularly on mobile), we still
	// set the other default values so that the health check can be easily
	// enabled with sane defaults.
	defaultRequiredDisk = 0.1
	defaultDiskInterval = time.Hour * 12
	defaultDiskTimeout  = time.Second * 5
	defaultDiskBackoff  = time.Minute
	defaultDiskAttempts = 0

	// defaultRemoteMaxHtlcs specifies the default limit for maximum
	// concurrent HTLCs the remote party may add to commitment transactions.
	// This value can be overridden with --default-remote-max-htlcs.
	defaultRemoteMaxHtlcs = 483

	// defaultMaxLocalCSVDelay is the maximum delay we accept on our
	// commitment output.
	// TODO(halseth): find a more scientific choice of value.
	defaultMaxLocalCSVDelay = 10000

	//default wallet filename
	defaultWalletFile = "wallet.db"
)

var (
	// DefaultPktDir is the default directory where pkt tries to find its
	// configuration file and store its data. This is a directory in the
	// user's application data, for example:
	//   C:\Users\<username>\AppData\Local\pktwallet on Windows
	//   ~/.pktwallet on Linux
	//   ~/Library/Application Support/pktwallet on MacOS
	defaultPktDir = btcutil.AppDataDir("pktwallet", false)
	// subdirectory where the wallet.db should be
	defaultPktWalletDir = filepath.Join(defaultPktDir, "pkt")

	// lnd folder should be under the main defaultPktDir
	// e.g. ~/.pktwallet/lnd
	DefaultLndDir = filepath.Join(defaultPktDir, "lnd")

	// DefaultConfigFile is the default full path of lnd's configuration
	// file.
	DefaultConfigFile = filepath.Join(DefaultLndDir, lncfg.DefaultConfigFilename)

	defaultDataDir  = filepath.Join(DefaultLndDir, defaultDataDirname)
	defaultTowerDir = filepath.Join(defaultDataDir, defaultTowerSubDirname)

	defaultTorSOCKS   = net.JoinHostPort("localhost", strconv.Itoa(defaultTorSOCKSPort))
	defaultTorDNS     = net.JoinHostPort(defaultTorDNSHost, strconv.Itoa(defaultTorDNSPort))
	defaultTorControl = net.JoinHostPort("localhost", strconv.Itoa(defaultTorControlPort))

	defaultSphinxDbName = "sphinxreplay.db"
)

// Config defines the configuration options for lnd.
//
// See LoadConfig for further details regarding the configuration
// loading+parsing process.
type Config struct {
	ShowVersion bool `short:"V" long:"version" description:"Display version information and exit"`

	LndDir       string `long:"lnddir" description:"The base directory that contains lnd's data, logs, configuration file, etc."`
	PktDir       string `long:"pktdir" description:"The base directory that contains pktwallet's data etc."`
	ConfigFile   string `short:"C" long:"configfile" description:"Path to configuration file"`
	DataDir      string `short:"b" long:"datadir" description:"The directory to store pld's data within"`
	WalletFile   string `long:"wallet" description:"Wallet file name or path, if a simple word such as 'personal' then pktwallet will look for wallet_personal.db, if prefixed with a / then pktwallet will consider it an absolute path. (default: wallet.db)"`
	SyncFreelist bool   `long:"sync-freelist" description:"Whether the databases used within pld should sync their freelist to disk. This is disabled by default resulting in improved memory performance during operation, but with an increase in startup time."`
	Create       bool   `long:"create" description:"Create a new wallet, walking through the steps to do so"`

	// We'll parse these 'raw' string arguments into real net.Addrs in the
	// loadConfig function. We need to expose the 'raw' strings so the
	// command line library can access them.
	// Only the parsed net.Addrs should be used!
	RawRESTListeners  []string `long:"restlisten" description:"Add an interface/port/socket to listen for REST connections"`
	RawListeners      []string `long:"listen" description:"Add an interface/port to listen for peer connections"`
	RawExternalIPs    []string `long:"externalip" description:"Add an ip:port to the list of local addresses we claim to listen on to peers. If a port is not specified, the default (9735) will be used regardless of other parameters"`
	ExternalHosts     []string `long:"externalhosts" description:"A set of hosts that should be periodically resolved to announce IPs for"`
	RESTListeners     []net.Addr
	RestCORS          []string `long:"restcors" description:"Add an ip:port/hostname to allow cross origin access from. To allow all origins, set as \"*\"."`
	Listeners         []net.Addr
	ExternalIPs       []net.Addr
	DisableListen     bool          `long:"nolisten" description:"Disable listening for incoming peer connections"`
	NAT               bool          `long:"nat" description:"Toggle NAT traversal support (using either UPnP or NAT-PMP) to automatically advertise your external IP address to the network -- NOTE this does not support devices behind multiple NATs"`
	MinBackoff        time.Duration `long:"minbackoff" description:"Shortest backoff when reconnecting to persistent peers. Valid time units are {s, m, h}."`
	MaxBackoff        time.Duration `long:"maxbackoff" description:"Longest backoff when reconnecting to persistent peers. Valid time units are {s, m, h}."`
	ConnectionTimeout time.Duration `long:"connectiontimeout" description:"The timeout value for network connections. Valid time units are {ms, s, m, h}."`

	DebugLevel string `short:"d" long:"debuglevel" description:"Logging level for all subsystems {trace, debug, info, warn, error, critical} -- You may also specify <global-level>,<subsystem>=<level>,<subsystem2>=<level>,... to set the log level for individual subsystems -- Use show to list available subsystems"`

	CPUProfile string `long:"cpuprofile" description:"Write CPU profile to the specified file"`

	Profile string `long:"profile" description:"Enable HTTP profiling on given port -- NOTE port must be between 1024 and 65535"`

	UnsafeDisconnect   bool   `long:"unsafe-disconnect" description:"DEPRECATED: Allows the rpcserver to intentionally disconnect from peers with open channels. THIS FLAG WILL BE REMOVED IN 0.10.0"`
	UnsafeReplay       bool   `long:"unsafe-replay" description:"Causes a link to replay the adds on its commitment txn after starting up, this enables testing of the sphinx replay logic."`
	MaxPendingChannels int    `long:"maxpendingchannels" description:"The maximum number of incoming pending channels permitted per peer."`
	BackupFilePath     string `long:"backupfilepath" description:"The target location of the channel backup file"`

	FeeURL string `long:"feeurl" description:"Optional URL for external fee estimation. If no URL is specified, the method for fee estimation will depend on the chosen backend and network."`

	Bitcoin      *lncfg.Chain    `group:"Bitcoin" namespace:"bitcoin"`
	NeutrinoMode *lncfg.Neutrino `group:"neutrino" namespace:"neutrino"`
	Litecoin     *lncfg.Chain    `group:"Litecoin" namespace:"litecoin"`
	Pkt          *lncfg.Chain    `group:"PKT" namespace:"pkt"`

	Autopilot *lncfg.AutoPilot `group:"Autopilot" namespace:"autopilot"`

	Tor *lncfg.Tor `group:"Tor" namespace:"tor"`

	SubRPCServers *subRPCServerConfigs `group:"subrpc"`

	Hodl *hodl.Config `group:"hodl" namespace:"hodl"`

	NoNetBootstrap bool `long:"nobootstrap" description:"If true, then automatic network bootstrapping will not be attempted."`

	NoSeedBackup bool `long:"noseedbackup" description:"If true, NO SEED WILL BE EXPOSED -- EVER, AND THE WALLET WILL BE ENCRYPTED USING THE DEFAULT PASSPHRASE. THIS FLAG IS ONLY FOR TESTING AND SHOULD NEVER BE USED ON MAINNET."`

	ResetWalletTransactions bool `long:"reset-wallet-transactions" description:"Removes all transaction history from the on-chain wallet on startup, forcing a full chain rescan starting at the wallet's birthday. Implements the same functionality as btcwallet's dropwtxmgr command. Should be set to false after successful execution to avoid rescanning on every restart of pld."`

	PaymentsExpirationGracePeriod time.Duration `long:"payments-expiration-grace-period" description:"A period to wait before force closing channels with outgoing htlcs that have timed-out and are a result of this node initiated payments."`
	TrickleDelay                  int           `long:"trickledelay" description:"Time in milliseconds between each release of announcements to the network"`
	ChanEnableTimeout             time.Duration `long:"chan-enable-timeout" description:"The duration that a peer connection must be stable before attempting to send a channel update to reenable or cancel a pending disables of the peer's channels on the network."`
	ChanDisableTimeout            time.Duration `long:"chan-disable-timeout" description:"The duration that must elapse after first detecting that an already active channel is actually inactive and sending channel update disabling it to the network. The pending disable can be canceled if the peer reconnects and becomes stable for chan-enable-timeout before the disable update is sent."`
	ChanStatusSampleInterval      time.Duration `long:"chan-status-sample-interval" description:"The polling interval between attempts to detect if an active channel has become inactive due to its peer going offline."`
	HeightHintCacheQueryDisable   bool          `long:"height-hint-cache-query-disable" description:"Disable queries from the height-hint cache to try to recover channels stuck in the pending close state. Disabling height hint queries may cause longer chain rescans, resulting in a performance hit. Unset this after channels are unstuck so you can get better performance again."`
	Alias                         string        `long:"alias" description:"The node alias. Used as a moniker by peers and intelligence services"`
	Color                         string        `long:"color" description:"The color of the node in hex format (i.e. '#3399FF'). Used to customize node appearance in intelligence services"`
	MinChanSize                   int64         `long:"minchansize" description:"The smallest channel size (in satoshis) that we should accept. Incoming channels smaller than this will be rejected"`
	MaxChanSize                   int64         `long:"maxchansize" description:"The largest channel size (in satoshis) that we should accept. Incoming channels larger than this will be rejected"`
	MaxPktFundingAmount           int64         `long:"maxpktfundingamount" description:"The largest funding for a channel (in satoshis) that we should accept."`
	DefaultRemoteMaxHtlcs         uint16        `long:"default-remote-max-htlcs" description:"The default max_htlc applied when opening or accepting channels. This value limits the number of concurrent HTLCs that the remote party can add to the commitment. The maximum possible value is 483."`

	NumGraphSyncPeers      int           `long:"numgraphsyncpeers" description:"The number of peers that we should receive new graph updates from. This option can be tuned to save bandwidth for light clients or routing nodes."`
	HistoricalSyncInterval time.Duration `long:"historicalsyncinterval" description:"The polling interval between historical graph sync attempts. Each historical graph sync attempt ensures we reconcile with the remote peer's graph from the genesis block."`

	IgnoreHistoricalGossipFilters bool `long:"ignore-historical-gossip-filters" description:"If true, will not reply with historical data that matches the range specified by a remote peer's gossip_timestamp_filter. Doing so will result in lower memory and bandwidth requirements."`

	RejectPush bool `long:"rejectpush" description:"If true, pld will not accept channel opening requests with non-zero push amounts. This should prevent accidental pushes to merchant nodes."`

	RejectHTLC bool `long:"rejecthtlc" description:"If true, pld will not forward any HTLCs that are meant as onward payments. This option will still allow pld to send HTLCs and receive HTLCs but pld won't be used as a hop."`

	StaggerInitialReconnect bool `long:"stagger-initial-reconnect" description:"If true, will apply a randomized staggering between 0s and 30s when reconnecting to persistent peers on startup. The first 10 reconnections will be attempted instantly, regardless of the flag's value"`

	MaxOutgoingCltvExpiry uint32 `long:"max-cltv-expiry" description:"The maximum number of blocks funds could be locked up for when forwarding payments."`

	MaxChannelFeeAllocation float64 `long:"max-channel-fee-allocation" description:"The maximum percentage of total funds that can be allocated to a channel's commitment fee. This only applies for the initiator of the channel. Valid values are within [0.1, 1]."`

	DryRunMigration bool `long:"dry-run-migration" description:"If true, pld will abort committing a migration if it would otherwise have been successful. This leaves the database unmodified, and still compatible with the previously active version of pld."`

	net tor.Net

	EnableUpfrontShutdown bool `long:"enable-upfront-shutdown" description:"If true, option upfront shutdown script will be enabled. If peers that we open channels with support this feature, we will automatically set the script to which cooperative closes should be paid out to on channel open. This offers the partial protection of a channel peer disconnecting from us if cooperative close is attempted with a different script."`

	AcceptKeySend bool `long:"accept-keysend" description:"If true, spontaneous payments through keysend will be accepted. [experimental]"`

	KeysendHoldTime time.Duration `long:"keysend-hold-time" description:"If non-zero, keysend payments are accepted but not immediately settled. If the payment isn't settled manually after the specified time, it is canceled automatically. [experimental]"`

	GcCanceledInvoicesOnStartup bool `long:"gc-canceled-invoices-on-startup" description:"If true, we'll attempt to garbage collect canceled invoices upon start."`

	GcCanceledInvoicesOnTheFly bool `long:"gc-canceled-invoices-on-the-fly" description:"If true, we'll delete newly canceled invoices on the fly."`

	Routing *lncfg.Routing `group:"routing" namespace:"routing"`

	Workers *lncfg.Workers `group:"workers" namespace:"workers"`

	Caches *lncfg.Caches `group:"caches" namespace:"caches"`

	WtClient *lncfg.WtClient `group:"wtclient" namespace:"wtclient"`

	Watchtower *lncfg.Watchtower `group:"watchtower" namespace:"watchtower"`

	ProtocolOptions *lncfg.ProtocolOptions `group:"protocol" namespace:"protocol"`

	AllowCircularRoute bool `long:"allow-circular-route" description:"If true, our node will allow htlc forwards that arrive and depart on the same channel."`

	HealthChecks *lncfg.HealthCheckConfig `group:"healthcheck" namespace:"healthcheck"`

	DB *lncfg.DB `group:"db" namespace:"db"`

	CjdnsSocket string `long:"cjdnssocket" description:"The path of the CJDNS socket (cjdroute.sock)"`

	// registeredChains keeps track of all chains that have been registered
	// with the daemon.
	registeredChains *chainreg.ChainRegistry

	// networkDir is the path to the directory of the currently active
	// network. This path will hold the files related to each different
	// network.
	networkDir string

	// ActiveNetParams contains parameters of the target chain.
	ActiveNetParams chainreg.BitcoinNetParams
}

// DefaultConfig returns all default values for the Config struct.
func DefaultConfig() Config {
	maxPktFundingAmount := btcutil.Amount(1 << 30 * 10000000)
	return Config{
		LndDir:     DefaultLndDir,
		PktDir:     defaultPktWalletDir,
		ConfigFile: DefaultConfigFile,
		DataDir:    defaultDataDir,
		WalletFile: defaultWalletFile,
		DebugLevel: defaultLogLevel,
		Bitcoin: &lncfg.Chain{
			MinHTLCIn:     chainreg.DefaultBitcoinMinHTLCInMSat,
			MinHTLCOut:    chainreg.DefaultBitcoinMinHTLCOutMSat,
			BaseFee:       chainreg.DefaultBitcoinBaseFeeMSat,
			FeeRate:       chainreg.DefaultBitcoinFeeRate,
			TimeLockDelta: chainreg.DefaultBitcoinTimeLockDelta,
			MaxLocalDelay: defaultMaxLocalCSVDelay,
		},
		Litecoin: &lncfg.Chain{
			MinHTLCIn:     chainreg.DefaultLitecoinMinHTLCInMSat,
			MinHTLCOut:    chainreg.DefaultLitecoinMinHTLCOutMSat,
			BaseFee:       chainreg.DefaultLitecoinBaseFeeMSat,
			FeeRate:       chainreg.DefaultLitecoinFeeRate,
			TimeLockDelta: chainreg.DefaultLitecoinTimeLockDelta,
			MaxLocalDelay: defaultMaxLocalCSVDelay,
		},
		Pkt: &lncfg.Chain{
			MinHTLCIn:     chainreg.DefaultPktMinHTLCInMSat,
			MinHTLCOut:    chainreg.DefaultPktMinHTLCOutMSat,
			BaseFee:       chainreg.DefaultPktBaseFeeMSat,
			FeeRate:       chainreg.DefaultPktFeeRate,
			TimeLockDelta: chainreg.DefaultPktTimeLockDelta,
			MaxLocalDelay: defaultMaxLocalCSVDelay,
		},
		NeutrinoMode: &lncfg.Neutrino{
			UserAgentName:    neutrino.UserAgentName,
			UserAgentVersion: neutrino.UserAgentVersion,
		},
		UnsafeDisconnect:   true,
		MaxPendingChannels: lncfg.DefaultMaxPendingChannels,
		NoSeedBackup:       defaultNoSeedBackup,
		MinBackoff:         defaultMinBackoff,
		MaxBackoff:         defaultMaxBackoff,
		ConnectionTimeout:  tor.DefaultConnTimeout,
		SubRPCServers: &subRPCServerConfigs{
			SignRPC:   &signrpc.Config{},
			RouterRPC: routerrpc.DefaultConfig(),
		},
		Autopilot: &lncfg.AutoPilot{
			MaxChannels:    5,
			Allocation:     0.6,
			MinChannelSize: int64(minChanFundingSize),
			MaxChannelSize: int64(MaxFundingAmount),
			MinConfs:       1,
			ConfTarget:     autopilot.DefaultConfTarget,
			Heuristic: map[string]float64{
				"top_centrality": 1.0,
			},
		},
		PaymentsExpirationGracePeriod: defaultPaymentsExpirationGracePeriod,
		TrickleDelay:                  defaultTrickleDelay,
		ChanStatusSampleInterval:      defaultChanStatusSampleInterval,
		ChanEnableTimeout:             defaultChanEnableTimeout,
		ChanDisableTimeout:            defaultChanDisableTimeout,
		HeightHintCacheQueryDisable:   defaultHeightHintCacheQueryDisable,
		Alias:                         defaultAlias,
		Color:                         defaultColor,
		MinChanSize:                   int64(minChanFundingSize),
		MaxChanSize:                   int64(0),
		MaxPktFundingAmount:           int64(maxPktFundingAmount),
		DefaultRemoteMaxHtlcs:         defaultRemoteMaxHtlcs,
		NumGraphSyncPeers:             defaultMinPeers,
		HistoricalSyncInterval:        discovery.DefaultHistoricalSyncInterval,
		Tor: &lncfg.Tor{
			SOCKS:   defaultTorSOCKS,
			DNS:     defaultTorDNS,
			Control: defaultTorControl,
		},
		net: &tor.ClearNet{},
		Workers: &lncfg.Workers{
			Read:  lncfg.DefaultReadWorkers,
			Write: lncfg.DefaultWriteWorkers,
			Sig:   lncfg.DefaultSigWorkers,
		},
		Caches: &lncfg.Caches{
			RejectCacheSize:  channeldb.DefaultRejectCacheSize,
			ChannelCacheSize: channeldb.DefaultChannelCacheSize,
		},
		Watchtower: &lncfg.Watchtower{
			TowerDir: defaultTowerDir,
		},
		HealthChecks: &lncfg.HealthCheckConfig{
			ChainCheck: &lncfg.CheckConfig{
				Interval: defaultChainInterval,
				Timeout:  defaultChainTimeout,
				Attempts: defaultChainAttempts,
				Backoff:  defaultChainBackoff,
			},
			DiskCheck: &lncfg.DiskCheckConfig{
				RequiredRemaining: defaultRequiredDisk,
				CheckConfig: &lncfg.CheckConfig{
					Interval: defaultDiskInterval,
					Attempts: defaultDiskAttempts,
					Timeout:  defaultDiskTimeout,
					Backoff:  defaultDiskBackoff,
				},
			},
		},
		MaxOutgoingCltvExpiry:   htlcswitch.DefaultMaxOutgoingCltvExpiry,
		MaxChannelFeeAllocation: htlcswitch.DefaultMaxLinkFeeAllocation,
		DB:                      lncfg.DefaultDB(),
		registeredChains:        chainreg.NewChainRegistry(),
		ActiveNetParams:         chainreg.BitcoinTestNetParams,
	}
}

// LoadConfig initializes and parses the config using a config file and command
// line options.
//
// The configuration proceeds as follows:
//  1. Start with a default config with sane settings
//  2. Pre-parse the command line to check for an alternative config file
//  3. Load configuration file overwriting defaults with any specified options
//  4. Parse CLI options and overwrite/add any specified options
func LoadConfig() (*Config, er.R) {
	// Pre-parse the command line options to pick up an alternative config
	// file.
	preCfg := DefaultConfig()
	if _, err := flags.Parse(&preCfg); err != nil {
		return nil, er.E(err)
	}

	// Show the version and exit if the version flag was specified.
	appName := filepath.Base(os.Args[0])
	appName = strings.TrimSuffix(appName, filepath.Ext(appName))
	usageMessage := fmt.Sprintf("Use %s -h to show usage", appName)
	if preCfg.ShowVersion {
		fmt.Println(appName, "version", version.Version())
		os.Exit(0)
	}

	// If the config file path has not been modified by the user, then we'll
	// use the default config file path. However, if the user has modified
	// their lnddir, then we should assume they intend to use the config
	// file within it.
	configFileDir := CleanAndExpandPath(preCfg.LndDir)
	configFilePath := CleanAndExpandPath(preCfg.ConfigFile)
	if configFileDir != DefaultLndDir {
		if configFilePath == DefaultConfigFile {
			configFilePath = filepath.Join(
				configFileDir, lncfg.DefaultConfigFilename,
			)
		}
	}

	// Next, load any additional configuration options from the file.
	var configFileError error
	cfg := preCfg

	parser := flags.NewParser(&cfg, flags.Default)
	errr := flags.NewIniParser(parser).ParseFile(configFilePath)
	if errr != nil {
		if _, ok := errr.(*os.PathError); !ok {
			return nil, er.E(errr)
		}
	}
	// Finally, parse the remaining command line options again to ensure
	// they take precedence.
	if _, err := flags.Parse(&cfg); err != nil {
		return nil, er.E(err)
	}

	// Make sure everything we just loaded makes sense.
	cleanCfg, err := ValidateConfig(cfg, usageMessage)
	if err != nil {
		return nil, err
	}

	if cfg.Create {
		// Error if the create flag is set and the wallet already
		// exists.
		dbPath := wallet.WalletDbPath(cfg.PktDir, cfg.WalletFile)
		dbFileExists, err := fileExists(dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return nil, err
		}
		if dbFileExists {
			err := er.Errorf("The wallet database file `%v` "+
				"already exists.", dbPath)
			fmt.Fprintln(os.Stderr, err)
			return nil, err
		}

		// Perform the initial wallet creation wizard.
		if err := createWallet(cleanCfg); err != nil {
			fmt.Fprintln(os.Stderr, "Unable to create wallet:", err)
			return nil, err
		}

		// Created successfully, so exit now with success.
		os.Exit(0)
	}

	// Warn about missing config file only after all other configuration is
	// done.  This prevents the warning on help messages and invalid
	// options.  Note this should go directly before the return.
	if configFileError != nil {
		log.Warnf("%v", configFileError)
	}

	return cleanCfg, nil
}

// ValidateConfig check the given configuration to be sane. This makes sure no
// illegal values or combination of values are set. All file system paths are
// normalized. The cleaned up config is returned on success.
func ValidateConfig(cfg Config, usageMessage string) (*Config, er.R) {
	// If the provided lnd directory is not the default, we'll modify the
	// path to all of the files and directories that will live within it.
	lndDir := CleanAndExpandPath(cfg.LndDir)
	if lndDir != DefaultLndDir {
		cfg.DataDir = filepath.Join(lndDir, defaultDataDirname)

		// If the watchtower's directory is set to the default, i.e. the
		// user has not requested a different location, we'll move the
		// location to be relative to the specified lnd directory.
		if cfg.Watchtower.TowerDir == defaultTowerDir {
			cfg.Watchtower.TowerDir =
				filepath.Join(cfg.DataDir, defaultTowerSubDirname)
		}
	}

	funcName := "loadConfig"
	makeDirectory := func(dir string) er.R {
		errr := os.MkdirAll(dir, 0700)
		if errr != nil {
			// Show a nicer error message if it's because a symlink
			// is linked to a directory that does not exist
			// (probably because it's not mounted).
			var err er.R
			if e, ok := errr.(*os.PathError); ok && os.IsExist(errr) {
				link, lerr := os.Readlink(e.Path)
				if lerr == nil {
					str := "is symlink %s -> %s mounted?"
					err = er.Errorf(str, e.Path, link)
				}
			} else {
				err = er.E(errr)
			}

			str := "%s: Failed to create pld directory: %v"
			err = er.Errorf(str, funcName, err)
			_, _ = fmt.Fprintln(os.Stderr, err)
			return err
		}

		return nil
	}

	// As soon as we're done parsing configuration options, ensure all paths
	// to directories and files are cleaned and expanded before attempting
	// to use them later on.
	cfg.DataDir = CleanAndExpandPath(cfg.DataDir)
	cfg.PktDir = CleanAndExpandPath(cfg.PktDir)
	cfg.Tor.PrivateKeyPath = CleanAndExpandPath(cfg.Tor.PrivateKeyPath)
	cfg.Tor.WatchtowerKeyPath = CleanAndExpandPath(cfg.Tor.WatchtowerKeyPath)
	cfg.Watchtower.TowerDir = CleanAndExpandPath(cfg.Watchtower.TowerDir)

	// Create the lnd directory and all other sub directories if they don't
	// already exist. This makes sure that directory trees are also created
	// for files that point to outside of the lnddir.
	dirs := []string{
		lndDir, cfg.DataDir,
		cfg.PktDir,
		cfg.Watchtower.TowerDir,
		filepath.Dir(cfg.Tor.PrivateKeyPath),
		filepath.Dir(cfg.Tor.WatchtowerKeyPath),
	}
	for _, dir := range dirs {
		if err := makeDirectory(dir); err != nil {
			return nil, err
		}
	}

	// Ensure that the user didn't attempt to specify negative values for
	// any of the autopilot params.
	if cfg.Autopilot.MaxChannels < 0 {
		str := "%s: autopilot.maxchannels must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.Allocation < 0 {
		str := "%s: autopilot.allocation must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MinChannelSize < 0 {
		str := "%s: autopilot.minchansize must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MaxChannelSize < 0 {
		str := "%s: autopilot.maxchansize must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MinConfs < 0 {
		str := "%s: autopilot.minconfs must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.ConfTarget < 1 {
		str := "%s: autopilot.conftarget must be positive"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}

	// Ensure that the specified values for the min and max channel size
	// are within the bounds of the normal chan size constraints.
	// if cfg.Autopilot.MinChannelSize < int64(minChanFundingSize) {
	// 	cfg.Autopilot.MinChannelSize = int64(minChanFundingSize)
	// }
	// if cfg.Autopilot.MaxChannelSize > int64(MaxFundingAmount) {
	// 	cfg.Autopilot.MaxChannelSize = int64(MaxFundingAmount)
	// }

	if _, err := validateAtplCfg(cfg.Autopilot); err != nil {
		return nil, err
	}

	// Ensure that --maxchansize is properly handled when set by user.
	// For non-Wumbo channels this limit remains 16777215 satoshis by default
	// as specified in BOLT-02. For wumbo channels this limit is 1,000,000,000.
	// satoshis (10 BTC). Always enforce --maxchansize explicitly set by user.
	// If unset (marked by 0 value), then enforce proper default.
	if cfg.MaxChanSize == 0 {
		if cfg.ProtocolOptions.Wumbo() {
			cfg.MaxChanSize = int64(MaxBtcFundingAmountWumbo)
		} else {
			cfg.MaxChanSize = int64(MaxBtcFundingAmount)
		}
	}

	// Ensure that the user specified values for the min and max channel
	// size make sense.
	if cfg.MaxChanSize < cfg.MinChanSize {
		return nil, er.Errorf("invalid channel size parameters: "+
			"max channel size %v, must be no less than min chan size %v",
			cfg.MaxChanSize, cfg.MinChanSize,
		)
	}

	// Don't allow superflous --maxchansize greater than
	// MaxPktFundingAmount set in the config.
	if !cfg.ProtocolOptions.Wumbo() && cfg.MaxChanSize > int64(cfg.MaxPktFundingAmount) {
		return nil, er.Errorf("invalid channel size parameters: "+
			"maximum channel size %v is greater than maximum non-wumbo"+
			" channel size %v",
			cfg.MaxChanSize, MaxFundingAmount,
		)
	}

	// Ensure a valid max channel fee allocation was set.
	if cfg.MaxChannelFeeAllocation <= 0 || cfg.MaxChannelFeeAllocation > 1 {
		return nil, er.Errorf("invalid max channel fee allocation: "+
			"%v, must be within (0, 1]",
			cfg.MaxChannelFeeAllocation)
	}

	// Validate the Tor config parameters.
	socks, err := lncfg.ParseAddressString(
		cfg.Tor.SOCKS, strconv.Itoa(defaultTorSOCKSPort),
		cfg.net.ResolveTCPAddr,
	)
	if err != nil {
		return nil, err
	}
	cfg.Tor.SOCKS = socks.String()

	// We'll only attempt to normalize and resolve the DNS host if it hasn't
	// changed, as it doesn't need to be done for the default.
	if cfg.Tor.DNS != defaultTorDNS {
		dns, err := lncfg.ParseAddressString(
			cfg.Tor.DNS, strconv.Itoa(defaultTorDNSPort),
			cfg.net.ResolveTCPAddr,
		)
		if err != nil {
			return nil, err
		}
		cfg.Tor.DNS = dns.String()
	}

	control, err := lncfg.ParseAddressString(
		cfg.Tor.Control, strconv.Itoa(defaultTorControlPort),
		cfg.net.ResolveTCPAddr,
	)
	if err != nil {
		return nil, err
	}
	cfg.Tor.Control = control.String()

	// Ensure that tor socks host:port is not equal to tor control
	// host:port. This would lead to lnd not starting up properly.
	if cfg.Tor.SOCKS == cfg.Tor.Control {
		str := "%s: tor.socks and tor.control can not use " +
			"the same host:port"
		return nil, er.Errorf(str, funcName)
	}

	switch {
	case cfg.Tor.V2 && cfg.Tor.V3:
		return nil, er.New("either tor.v2 or tor.v3 can be set, " +
			"but not both")
	case cfg.DisableListen && (cfg.Tor.V2 || cfg.Tor.V3):
		return nil, er.New("listening must be enabled when " +
			"enabling inbound connections over Tor")
	}

	if cfg.Tor.PrivateKeyPath == "" {
		switch {
		case cfg.Tor.V2:
			cfg.Tor.PrivateKeyPath = filepath.Join(
				lndDir, defaultTorV2PrivateKeyFilename,
			)
		case cfg.Tor.V3:
			cfg.Tor.PrivateKeyPath = filepath.Join(
				lndDir, defaultTorV3PrivateKeyFilename,
			)
		}
	}

	if cfg.Tor.WatchtowerKeyPath == "" {
		switch {
		case cfg.Tor.V2:
			cfg.Tor.WatchtowerKeyPath = filepath.Join(
				cfg.Watchtower.TowerDir, defaultTorV2PrivateKeyFilename,
			)
		case cfg.Tor.V3:
			cfg.Tor.WatchtowerKeyPath = filepath.Join(
				cfg.Watchtower.TowerDir, defaultTorV3PrivateKeyFilename,
			)
		}
	}

	// Set up the network-related functions that will be used throughout
	// the daemon. We use the standard Go "net" package functions by
	// default. If we should be proxying all traffic through Tor, then
	// we'll use the Tor proxy specific functions in order to avoid leaking
	// our real information.
	if cfg.Tor.Active {
		cfg.net = &tor.ProxyNet{
			SOCKS:           cfg.Tor.SOCKS,
			DNS:             cfg.Tor.DNS,
			StreamIsolation: cfg.Tor.StreamIsolation,
		}
	}

	if cfg.DisableListen && cfg.NAT {
		return nil, er.New("NAT traversal cannot be used when " +
			"listening is disabled")
	}
	if cfg.NAT && len(cfg.ExternalHosts) != 0 {
		return nil, er.New("NAT support and externalhosts are " +
			"mutually exclusive, only one should be selected")
	}

	if !cfg.Bitcoin.Active && !cfg.Litecoin.Active && !cfg.Pkt.Active {
		// Default to PKT
		cfg.Pkt.Active = true
	}

	// Determine the active chain configuration and its parameters.
	switch {
	// At this moment, multiple active chains are not supported.
	case cfg.Litecoin.Active && cfg.Bitcoin.Active:
		str := "%s: Currently both Bitcoin and Litecoin cannot be " +
			"active together"
		return nil, er.Errorf(str, funcName)

	// Either Bitcoin must be active, or Litecoin must be active.
	// Otherwise, we don't know which chain we're on.
	case !cfg.Bitcoin.Active && !cfg.Litecoin.Active && !cfg.Pkt.Active:
		return nil, er.Errorf("%s: either bitcoin.active or "+
			"litecoin.active must be set to 1 (true)", funcName)

	case cfg.Pkt.Active:
		cfg.ActiveNetParams = chainreg.PktMainNetParams
		// Calling it /pkt/mainnet makes life easier
		cfg.ActiveNetParams.Name = "mainnet"
		cfg.Pkt.ChainDir = filepath.Join(cfg.DataDir,
			defaultChainSubDirname,
			chainreg.PktChain.String())

		// Finally we'll register the litecoin chain as our current
		// primary chain.
		cfg.registeredChains.RegisterPrimaryChain(chainreg.PktChain)
		MaxFundingAmount = maxPktFundingAmount

	case cfg.Litecoin.Active:
		err := cfg.Litecoin.Validate(minTimeLockDelta, minLtcRemoteDelay)
		if err != nil {
			return nil, err
		}

		// Multiple networks can't be selected simultaneously.  Count
		// number of network flags passed; assign active network params
		// while we're at it.
		numNets := 0
		var ltcParams chainreg.LitecoinNetParams
		if cfg.Litecoin.MainNet {
			numNets++
			ltcParams = chainreg.LitecoinMainNetParams
		}
		if cfg.Litecoin.TestNet3 {
			numNets++
			ltcParams = chainreg.LitecoinTestNetParams
		}
		if cfg.Litecoin.RegTest {
			numNets++
			ltcParams = chainreg.LitecoinRegTestNetParams
		}
		if cfg.Litecoin.SimNet {
			numNets++
			ltcParams = chainreg.LitecoinSimNetParams
		}

		if numNets > 1 {
			str := "%s: The mainnet, testnet, and simnet params " +
				"can't be used together -- choose one of the " +
				"three"
			err := er.Errorf(str, funcName)
			return nil, err
		}

		// The target network must be provided, otherwise, we won't
		// know how to initialize the daemon.
		if numNets == 0 {
			str := "%s: either --litecoin.mainnet, or " +
				"litecoin.testnet must be specified"
			err := er.Errorf(str, funcName)
			return nil, err
		}

		// The litecoin chain is the current active chain. However
		// throughout the codebase we required chaincfg.Params. So as a
		// temporary hack, we'll mutate the default net params for
		// bitcoin with the litecoin specific information.
		chainreg.ApplyLitecoinParams(&cfg.ActiveNetParams, &ltcParams)

		cfg.Litecoin.ChainDir = filepath.Join(cfg.DataDir,
			defaultChainSubDirname,
			chainreg.LitecoinChain.String())

		// Finally we'll register the litecoin chain as our current
		// primary chain.
		cfg.registeredChains.RegisterPrimaryChain(chainreg.LitecoinChain)
		MaxFundingAmount = maxLtcFundingAmount

	case cfg.Bitcoin.Active:
		// Multiple networks can't be selected simultaneously.  Count
		// number of network flags passed; assign active network params
		// while we're at it.
		numNets := 0
		if cfg.Bitcoin.MainNet {
			numNets++
			cfg.ActiveNetParams = chainreg.BitcoinMainNetParams
		}
		if cfg.Bitcoin.TestNet3 {
			numNets++
			cfg.ActiveNetParams = chainreg.BitcoinTestNetParams
		}
		if cfg.Bitcoin.RegTest {
			numNets++
			cfg.ActiveNetParams = chainreg.BitcoinRegTestNetParams
		}
		if cfg.Bitcoin.SimNet {
			numNets++
			cfg.ActiveNetParams = chainreg.BitcoinSimNetParams
		}
		if numNets > 1 {
			str := "%s: The mainnet, testnet, regtest, and " +
				"simnet params can't be used together -- " +
				"choose one of the four"
			err := er.Errorf(str, funcName)
			return nil, err
		}

		// The target network must be provided, otherwise, we won't
		// know how to initialize the daemon.
		if numNets == 0 {
			str := "%s: either --bitcoin.mainnet, or " +
				"bitcoin.testnet, bitcoin.simnet, or bitcoin.regtest " +
				"must be specified"
			err := er.Errorf(str, funcName)
			return nil, err
		}

		err := cfg.Bitcoin.Validate(minTimeLockDelta, minBtcRemoteDelay)
		if err != nil {
			return nil, err
		}

		cfg.Bitcoin.ChainDir = filepath.Join(cfg.DataDir,
			defaultChainSubDirname,
			chainreg.BitcoinChain.String())

		// Finally we'll register the bitcoin chain as our current
		// primary chain.
		cfg.registeredChains.RegisterPrimaryChain(chainreg.BitcoinChain)
	}
	globalcfg.SelectConfig(cfg.ActiveNetParams.GlobalConf)

	// Ensure that the user didn't attempt to specify negative values for
	// any of the autopilot params.
	if cfg.Autopilot.MaxChannels < 0 {
		str := "%s: autopilot.maxchannels must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.Allocation < 0 {
		str := "%s: autopilot.allocation must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MinChannelSize < 0 {
		str := "%s: autopilot.minchansize must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}
	if cfg.Autopilot.MaxChannelSize < 0 {
		str := "%s: autopilot.maxchansize must be non-negative"
		err := er.Errorf(str, funcName)
		_, _ = fmt.Fprintln(os.Stderr, err)
		return nil, err
	}

	// Ensure that the specified values for the min and max channel size
	// don't are within the bounds of the normal chan size constraints.
	// if cfg.Autopilot.MinChannelSize < int64(minChanFundingSize) {
	// 	cfg.Autopilot.MinChannelSize = int64(minChanFundingSize)
	// }
	// if cfg.Autopilot.MaxChannelSize > int64(MaxFundingAmount) {
	// 	cfg.Autopilot.MaxChannelSize = int64(MaxFundingAmount)
	// }

	// Validate profile port number.
	if cfg.Profile != "" {
		profilePort, err := strconv.Atoi(cfg.Profile)
		if err != nil || profilePort < 1024 || profilePort > 65535 {
			str := "%s: The profile port must be between 1024 and 65535"
			err := er.Errorf(str, funcName)
			_, _ = fmt.Fprintln(os.Stderr, err)
			_, _ = fmt.Fprintln(os.Stderr, usageMessage)
			return nil, err
		}
	}

	// We'll now construct the network directory which will be where we
	// store all the data specific to this chain/network.
	cfg.networkDir = filepath.Join(
		cfg.DataDir, defaultChainSubDirname,
		cfg.registeredChains.PrimaryChain().String(),
		lncfg.NormalizeNetwork(cfg.ActiveNetParams.Name),
	)

	// Similarly, if a custom back up file path wasn't specified, then
	// we'll update the file location to match our set network directory.
	if cfg.BackupFilePath == "" {
		cfg.BackupFilePath = filepath.Join(
			cfg.networkDir, chanbackup.DefaultBackupFileName,
		)
	}

	// Parse, validate, and set debug log level(s).
	err = log.SetLogLevels(cfg.DebugLevel)
	if err != nil {
		err = er.Errorf("%s: %v", funcName, err.String())
		_, _ = fmt.Fprintln(os.Stderr, err)
		_, _ = fmt.Fprintln(os.Stderr, usageMessage)
		return nil, err
	}

	// Listen on localhost if no REST listeners were specified.
	if len(cfg.RawRESTListeners) == 0 {
		addr := fmt.Sprintf("localhost:%d", defaultRESTPort)
		cfg.RawRESTListeners = append(cfg.RawRESTListeners, addr)
	}

	// Listen on the default interface/port if no listeners were specified.
	// An empty address string means default interface/address, which on
	// most unix systems is the same as 0.0.0.0. If Tor is active, we
	// default to only listening on localhost for hidden service
	// connections.
	if len(cfg.RawListeners) == 0 {
		addr := fmt.Sprintf(":%d", defaultPeerPort)
		if cfg.Tor.Active {
			addr = fmt.Sprintf("localhost:%d", defaultPeerPort)
		}
		cfg.RawListeners = append(cfg.RawListeners, addr)
	}

	// Add default port to all REST listener addresses if needed and remove
	// duplicate addresses.
	cfg.RESTListeners, err = lncfg.NormalizeAddresses(
		cfg.RawRESTListeners, strconv.Itoa(defaultRESTPort),
		cfg.net.ResolveTCPAddr,
	)
	if err != nil {
		return nil, err
	}

	// Remove the listening addresses specified if listening is disabled.
	if cfg.DisableListen {
		log.Infof("Listening on the p2p interface is disabled!")
		cfg.Listeners = nil
		cfg.ExternalIPs = nil
	} else {

		// Add default port to all listener addresses if needed and remove
		// duplicate addresses.
		cfg.Listeners, err = lncfg.NormalizeAddresses(
			cfg.RawListeners, strconv.Itoa(defaultPeerPort),
			cfg.net.ResolveTCPAddr,
		)
		if err != nil {
			return nil, err
		}

		// Add default port to all external IP addresses if needed and remove
		// duplicate addresses.
		cfg.ExternalIPs, err = lncfg.NormalizeAddresses(
			cfg.RawExternalIPs, strconv.Itoa(defaultPeerPort),
			cfg.net.ResolveTCPAddr,
		)
		if err != nil {
			return nil, err
		}

		// For the p2p port it makes no sense to listen to an Unix socket.
		// Also, we would need to refactor the brontide listener to support
		// that.
		for _, p2pListener := range cfg.Listeners {
			if lncfg.IsUnix(p2pListener) {
				err := er.Errorf("unix socket addresses cannot be "+
					"used for the p2p connection listener: %s",
					p2pListener)
				return nil, err
			}
		}
	}

	// Ensure that the specified minimum backoff is below or equal to the
	// maximum backoff.
	if cfg.MinBackoff > cfg.MaxBackoff {
		return nil, er.Errorf("maxbackoff must be greater than " +
			"minbackoff")
	}

	// Newer versions of lnd added a new sub-config for bolt-specific
	// parameters. However we want to also allow existing users to use the
	// value on the top-level config. If the outer config value is set,
	// then we'll use that directly.
	if cfg.SyncFreelist {
		cfg.DB.Bolt.SyncFreelist = cfg.SyncFreelist
	}

	// Ensure that the user hasn't chosen a remote-max-htlc value greater
	// than the protocol maximum.
	maxRemoteHtlcs := uint16(input.MaxHTLCNumber / 2)
	if cfg.DefaultRemoteMaxHtlcs > maxRemoteHtlcs {
		return nil, er.Errorf("default-remote-max-htlcs (%v) must be "+
			"less than %v", cfg.DefaultRemoteMaxHtlcs,
			maxRemoteHtlcs)
	}

	// Validate the subconfigs for workers, caches, and the tower client.
	err = lncfg.Validate(
		cfg.Workers,
		cfg.Caches,
		cfg.WtClient,
		cfg.DB,
		cfg.HealthChecks,
	)
	if err != nil {
		return nil, err
	}

	// Finally, ensure that the user's color is correctly formatted,
	// otherwise the server will not be able to start after the unlocking
	// the wallet.
	_, err = parseHexColor(cfg.Color)
	if err != nil {
		return nil, er.Errorf("unable to parse node color: %v", err)
	}

	// All good, return the sanitized result.
	return &cfg, err
}

// localDatabaseDir returns the default directory where the
// local bolt db files are stored.
func (c *Config) localDatabaseDir() string {
	return filepath.Join(c.DataDir,
		defaultGraphSubDirname,
		lncfg.NormalizeNetwork(c.ActiveNetParams.Name))
}

func (c *Config) networkName() string {
	return lncfg.NormalizeNetwork(c.ActiveNetParams.Name)
}

// CleanAndExpandPath expands environment variables and leading ~ in the
// passed path, cleans the result, and returns it.
// This function is taken from https://github.com/btcsuite/btcd
func CleanAndExpandPath(path string) string {
	if path == "" {
		return ""
	}

	// Expand initial ~ to OS specific home directory.
	if strings.HasPrefix(path, "~") {
		var homeDir string
		u, err := user.Current()
		if err == nil {
			homeDir = u.HomeDir
		} else {
			homeDir = os.Getenv("HOME")
		}

		path = strings.Replace(path, "~", homeDir, 1)
	}

	// NOTE: The os.ExpandEnv doesn't work with Windows-style %VARIABLE%,
	// but the variables can still be expanded via POSIX-style $VARIABLE.
	return filepath.Clean(os.ExpandEnv(path))
}

// convertLegacyKeystore converts all of the addresses in the passed legacy
// key store to the new waddrmgr.Manager format.  Both the legacy keystore and
// the new manager must be unlocked.
func convertLegacyKeystore(legacyKeyStore *keystore.Store, w *wallet.Wallet) er.R {
	netParams := legacyKeyStore.Net()
	blockStamp := waddrmgr.BlockStamp{
		Height: 0,
		Hash:   *netParams.GenesisHash,
	}
	for _, walletAddr := range legacyKeyStore.ActiveAddresses() {
		switch addr := walletAddr.(type) {
		case keystore.PubKeyAddress:
			privKey, err := addr.PrivKey()
			if err != nil {
				fmt.Printf("WARN: Failed to obtain private key "+
					"for address %v: %v\n", addr.Address(),
					err)
				continue
			}

			wif, err := btcutil.NewWIF((*btcec.PrivateKey)(privKey),
				netParams, addr.Compressed())
			if err != nil {
				fmt.Printf("WARN: Failed to create wallet "+
					"import format for address %v: %v\n",
					addr.Address(), err)
				continue
			}

			_, err = w.ImportPrivateKey(waddrmgr.KeyScopeBIP0044,
				wif, &blockStamp, false)
			if err != nil {
				fmt.Printf("WARN: Failed to import private "+
					"key for address %v: %v\n",
					addr.Address(), err)
				continue
			}

		case keystore.ScriptAddress:
			_, err := w.ImportP2SHRedeemScript(addr.Script())
			if err != nil {
				fmt.Printf("WARN: Failed to import "+
					"pay-to-script-hash script for "+
					"address %v: %v\n", addr.Address(), err)
				continue
			}

		default:
			fmt.Printf("WARN: Skipping unrecognized legacy "+
				"keystore type: %T\n", addr)
			continue
		}
	}

	return nil
}

func fileExists(filePath string) (bool, er.R) {
	_, errr := os.Stat(filePath)
	if errr != nil {
		if os.IsNotExist(errr) {
			return false, nil
		}
		return false, er.E(errr)
	}
	return true, nil
}

type WalletSetupCfg struct {
	Passphrase       *string `json:"passphrase"`
	PublicPassphrase *string `json:"viewpassphrase"`
	Seed             *string `json:"seed"`
	SeedPassphrase   *string `json:"seedpassphrase"`
}

// createWallet prompts the user for information needed to generate a new wallet
// and generates the wallet accordingly.  The new wallet will reside at the
// provided path.
func createWallet(cfg *Config) er.R {
	//dbDir := networkDir(cfg.AppDataDir.Value, activeNet.Params)
	// TODO(cjd): noFreelistSync ?
	//loader := wallet.NewLoader(activeNet.Params, dbDir, cfg.Wallet, false, 250)
	loader := wallet.NewLoader(cfg.ActiveNetParams.Params, cfg.PktDir, cfg.WalletFile, false, 250)

	keystorePath := filepath.Join(cfg.networkDir, keystore.Filename)
	var legacyKeyStore *keystore.Store
	_, errr := os.Stat(keystorePath)
	if errr != nil && !os.IsNotExist(errr) {
		// A stat error not due to a non-existant file should be
		// returned to the caller.
		return er.E(errr)
	} else if errr == nil {
		// Keystore file exists.
		var err er.R
		//legacyKeyStore, err = keystore.OpenDir(netDir)
		legacyKeyStore, err = keystore.OpenDir(cfg.networkDir)
		if err != nil {
			return err
		}
	}

	fi, err := os.Stdin.Stat()
	if err != nil {
		panic("createWallet: os.Stdin.Stat failure.")
	}
	tty := false
	var privPass []byte
	pubPass := []byte(wallet.InsecurePubPassphrase)
	var seedInput []byte
	var seed *seedwords.Seed
	setupCfg := WalletSetupCfg{}
	if (fi.Mode() & os.ModeCharDevice) != 0 {
		tty = true
	} else if bytes, err := ioutil.ReadAll(os.Stdin); err != nil {
		return er.E(err)
	} else if err := jsoniter.Unmarshal(bytes, &setupCfg); err != nil {
		return er.E(err)
	} else {
		log.SetLogLevels("off")
		if setupCfg.Passphrase != nil {
			privPass = []byte(*setupCfg.Passphrase)
		}
		if setupCfg.PublicPassphrase != nil {
			pubPass = []byte(*setupCfg.PublicPassphrase)
		}
		if setupCfg.Seed != nil {
			if decoded, err := hex.DecodeString(*setupCfg.Seed); err == nil {
				zero.Bytes(decoded)
				seedInput = []byte(*setupCfg.Seed)
			} else {
				seedEnc, err := seedwords.SeedFromWords(*setupCfg.Seed)
				if err != nil {
					return err
				}
				var bs []byte
				if setupCfg.SeedPassphrase != nil {
					bs = []byte(*setupCfg.SeedPassphrase)
				}
				if setupCfg.SeedPassphrase != nil || !seedEnc.NeedsPassphrase() {
					s, err := seedEnc.Decrypt(bs, false)
					if err != nil {
						return err
					}
					seed = s
				} else {
					return er.New("The provided seed requires a passphrase")
				}
			}
		} else {
			if s, err := seedwords.RandomSeed(); err != nil {
				return err
			} else {
				seed = s
			}
		}
	}

	// Start by prompting for the private passphrase.  When there is an
	// existing keystore, the user will be promped for that passphrase,
	// otherwise they will be prompted for a new one.
	var reader *bufio.Reader
	if tty {
		reader = bufio.NewReader(os.Stdin)
		pvt, err := prompt.PrivatePass(reader, legacyKeyStore)
		if err != nil {
			return err
		}
		privPass = pvt
	}

	// When there exists a legacy keystore, unlock it now and set up a
	// callback to import all keystore keys into the new walletdb
	// wallet
	if legacyKeyStore != nil {
		err := legacyKeyStore.Unlock(privPass)
		if err != nil {
			return err
		}

		// Import the addresses in the legacy keystore to the new wallet if
		// any exist, locking each wallet again when finished.
		loader.RunAfterLoad(func(w *wallet.Wallet) {
			defer legacyKeyStore.Lock()

			fmt.Println("Importing addresses from existing wallet...")

			lockChan := make(chan time.Time, 1)
			defer func() {
				lockChan <- time.Time{}
			}()
			err := w.Unlock(privPass, lockChan)
			if err != nil {
				fmt.Printf("ERR: Failed to unlock new wallet "+
					"during old wallet key import: %v", err)
				return
			}

			err = convertLegacyKeystore(legacyKeyStore, w)
			if err != nil {
				fmt.Printf("ERR: Failed to import keys from old "+
					"wallet format: %v", err)
				return
			}

			// Remove the legacy key store.
			errr = os.Remove(keystorePath)
			if errr != nil {
				fmt.Printf("WARN: Failed to remove legacy wallet "+
					"from'%s'\n", keystorePath)
			}
		})
	}

	// Ascertain the wallet generation seed.  This will either be an
	// automatically generated value the user has already confirmed or a
	// value the user has entered which has already been validated.
	if tty {
		si, sd, err := prompt.Seed(reader, privPass)
		if err != nil {
			return err
		}
		seedInput = si
		seed = sd
	}

	if tty {
		fmt.Println("Creating the wallet...")
	}
	w, werr := loader.CreateNewWallet(pubPass, privPass, seedInput, time.Now(), seed, nil)
	if werr != nil {
		return werr
	}

	w.Manager.Close()
	if tty {
		fmt.Println("The wallet has been created successfully.")
	} else if seed != nil {
		seedEnc := seed.Encrypt(privPass)
		if words, err := seedEnc.Words("english"); err != nil {
			return err
		} else {
			fmt.Printf(`{"seed":"%s"}`+"\n", words)
		}
		seedEnc.Zero()
	} else {
		fmt.Printf(`{"seed":"%s"}`+"\n", seedInput)
	}
	if seed != nil {
		seed.Zero()
	}
	return nil
}
