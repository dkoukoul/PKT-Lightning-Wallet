// Copyright (c) 2013-2017 The btcsuite developers
// Copyright (c) 2015-2016 The Decred developers
// Copyright (C) 2015-2017 The Lightning Network Developers

package lnd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof" // Blank import to set up profiling HTTP handlers.
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg/chainhash"
	"github.com/pkt-cash/pktd/cjdns"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/autopilot"
	"github.com/pkt-cash/pktd/lnd/chainreg"
	"github.com/pkt-cash/pktd/lnd/chanacceptor"
	"github.com/pkt-cash/pktd/lnd/channeldb"
	"github.com/pkt-cash/pktd/lnd/keychain"
	"github.com/pkt-cash/pktd/lnd/lncfg"
	"github.com/pkt-cash/pktd/lnd/lnrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/lnd/lnrpc/autopilotrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/routerrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/wtclientrpc"
	"github.com/pkt-cash/pktd/lnd/lnwallet"
	"github.com/pkt-cash/pktd/lnd/signal"
	"github.com/pkt-cash/pktd/lnd/tor"
	"github.com/pkt-cash/pktd/lnd/walletunlocker"
	"github.com/pkt-cash/pktd/lnd/watchtower"
	"github.com/pkt-cash/pktd/lnd/watchtower/wtdb"
	"github.com/pkt-cash/pktd/neutrino"
	"github.com/pkt-cash/pktd/neutrino/headerfs"
	"github.com/pkt-cash/pktd/pktconfig/version"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/pktwallet/walletdb"
)

// Main is the true entry point for lnd. It accepts a fully populated and
// validated main configuration struct and an optional listener config struct.
// This function starts all main system components then blocks until a signal
// is received on the shutdownChan at which point everything is shut down again.
func Main(cfg *Config, shutdownChan <-chan struct{}) er.R {
	// Show version at startup.
	log.Infof("Version: %s debuglevel=%s",
		version.Version(), cfg.DebugLevel)

	var network string
	switch {
	case cfg.Bitcoin.TestNet3 || cfg.Litecoin.TestNet3:
		network = "testnet"

	case cfg.Bitcoin.MainNet || cfg.Litecoin.MainNet || cfg.Pkt.MainNet:
		network = "mainnet"

	case cfg.Bitcoin.SimNet || cfg.Litecoin.SimNet:
		network = "simnet"

	case cfg.Bitcoin.RegTest || cfg.Litecoin.RegTest:
		network = "regtest"
	}

	log.Infof("Active chain: %v (network=%v)",
		strings.ToTitle(cfg.registeredChains.PrimaryChain().String()),
		network,
	)

	// Bring up the REST handler immediately
	api, apiRouter := apiv1.New()
	restContext := RpcContext{}
	restContext.RegisterFunctions(api)
	//restHandler := restrpc.RestHandlers(&restContext)
	//restrpc.RestHandlersHelp(restHandler)

	for _, restEndpoint := range cfg.RESTListeners {
		lis, err := lncfg.ListenOnAddress(restEndpoint)
		if err != nil {
			log.Errorf("REST unable to listen on %s", restEndpoint)
			return err
		}
		go func() {
			log.Infof("REST started at %s", lis.Addr())
			corsHandler := allowCORS(apiRouter, cfg.RestCORS)
			err := http.Serve(lis, corsHandler)
			if err != nil && !lnrpc.IsClosedConnError(err) {
				log.Error(err)
			}
		}()
	}

	// Enable http profiling server if requested.
	if cfg.Profile != "" {
		go func() {
			listenAddr := net.JoinHostPort("", cfg.Profile)
			profileRedirect := http.RedirectHandler("/debug/pprof",
				http.StatusSeeOther)
			http.Handle("/", profileRedirect)
			fmt.Println(http.ListenAndServe(listenAddr, nil))
		}()
	}

	// Write cpu profile if requested.
	if cfg.CPUProfile != "" {
		f, err := os.Create(cfg.CPUProfile)
		if err != nil {
			err := er.Errorf("unable to create CPU profile: %v",
				err)
			log.Error(err)
			return err
		}
		pprof.StartCPUProfile(f)
		defer f.Close()
		defer pprof.StopCPUProfile()
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	localChanDB, remoteChanDB, cleanUp, err := initializeDatabases(ctx, cfg)
	switch {
	case channeldb.ErrDryRunMigrationOK.Is(err):
		log.Infof("%v, exiting", err)
		return nil
	case err != nil:
		return er.Errorf("unable to open databases: %v", err)
	}

	defer cleanUp()

	// Before starting the wallet, we'll create and start our Neutrino
	// light client instance, if enabled, in order to allow it to sync
	// while the rest of the daemon continues startup.
	mainChain := cfg.Bitcoin
	if cfg.registeredChains.PrimaryChain() == chainreg.LitecoinChain {
		mainChain = cfg.Litecoin
	}
	if cfg.registeredChains.PrimaryChain() == chainreg.PktChain {
		mainChain = cfg.Pkt
	}

	neutrinoCS, neutrinoCleanUp, err := initNeutrinoBackend(
		cfg, cfg.PktDir, api.Category("neutrino"),
	)
	if err != nil {
		err := er.Errorf("unable to initialize neutrino "+
			"backend: %v", err)
		log.Error(err)
		return err
	}
	defer neutrinoCleanUp()
	restContext.MaybeNeutrino = neutrinoCS

	var (
		walletInitParams WalletUnlockParams
		privateWalletPw  = lnwallet.DefaultPrivatePassphrase
		publicWalletPw   = lnwallet.DefaultPublicPassphrase
	)

	// If the user didn't request a seed, then we'll manually assume a
	// wallet birthday of now, as otherwise the seed would've specified
	// this information.
	walletInitParams.Birthday = time.Now()

	// Set up meta Service pass neutrino for getinfo and changepassword
	// call init later to pass arguments needed for changepassword
	metaService := lnrpc.NewMetaService(neutrinoCS)

	//Parse filename from --wallet or default
	walletPath, walletFilename := WalletFilename(cfg.WalletFile)
	//Get default pkt dir ~/.pktwallet/pkt
	if walletPath == "" {
		walletPath = cfg.PktDir
	}
	//Initialize the metaservice with params needed for change password
	metaService.Init(!cfg.SyncFreelist, cfg.ActiveNetParams.Params, walletFilename, walletPath)

	restContext.MaybeMetaService = metaService
	// We wait until the user provides a password over RPC. In case lnd is
	// started with the --noseedbackup flag, we use the default password
	// for wallet encryption.

	if !cfg.NoSeedBackup {
		params, err := waitForWalletPassword(cfg, &restContext, api)
		if err != nil {
			err := er.Errorf("unable to set up wallet password "+
				"listeners: %v", err)
			log.Error(err)
			return err
		}

		walletInitParams = *params
		privateWalletPw = walletInitParams.Password
		publicWalletPw = walletInitParams.Password
		//Pass wallet to metaservice for getinfo2
		metaService.SetWallet(walletInitParams.Wallet)
		restContext.MaybeWallet = walletInitParams.Wallet
		defer func() {
			if err := walletInitParams.UnloadWallet(); err != nil {
				log.Errorf("Could not unload wallet: %v", err)
			}
		}()

		if walletInitParams.RecoveryWindow > 0 {
			log.Infof("Wallet recovery mode enabled with "+
				"address lookahead of %d addresses",
				walletInitParams.RecoveryWindow)
		}
	}

	// With the information parsed from the configuration, create valid
	// instances of the pertinent interfaces required to operate the
	// Lightning Network Daemon.
	//
	// When we create the chain control, we need storage for the height
	// hints and also the wallet itself, for these two we want them to be
	// replicated, so we'll pass in the remote channel DB instance.
	chainControlCfg := &chainreg.Config{
		Bitcoin:                     cfg.Bitcoin,
		Litecoin:                    cfg.Litecoin,
		Pkt:                         cfg.Pkt,
		PrimaryChain:                cfg.registeredChains.PrimaryChain,
		HeightHintCacheQueryDisable: cfg.HeightHintCacheQueryDisable,
		NeutrinoMode:                cfg.NeutrinoMode,
		LocalChanDB:                 localChanDB,
		RemoteChanDB:                remoteChanDB,
		PrivateWalletPw:             privateWalletPw,
		PublicWalletPw:              publicWalletPw,
		Birthday:                    walletInitParams.Birthday,
		RecoveryWindow:              walletInitParams.RecoveryWindow,
		Wallet:                      walletInitParams.Wallet,
		NeutrinoCS:                  neutrinoCS,
		ActiveNetParams:             cfg.ActiveNetParams,
		FeeURL:                      cfg.FeeURL,
	}

	activeChainControl, err := chainreg.NewChainControl(chainControlCfg, api)
	if err != nil {
		err := er.Errorf("unable to create chain control: %v", err)
		log.Error(err)
		return err
	}

	// Finally before we start the server, we'll register the "holy
	// trinity" of interface for our current "home chain" with the active
	// chainRegistry interface.
	primaryChain := cfg.registeredChains.PrimaryChain()
	cfg.registeredChains.RegisterChain(primaryChain, activeChainControl)

	// TODO(roasbeef): add rotation
	idKeyDesc, err := activeChainControl.KeyRing.DeriveKey(
		keychain.KeyLocator{
			Family: keychain.KeyFamilyNodeKey,
			Index:  0,
		},
	)
	if err != nil {
		err := er.Errorf("error deriving node key: %v", err)
		log.Error(err)
		return err
	}

	if cfg.Tor.Active {
		log.Infof("Proxying all network traffic via Tor "+
			"(stream_isolation=%v)! NOTE: Ensure the backend node "+
			"is proxying over Tor as well", cfg.Tor.StreamIsolation)
	}

	// If the watchtower client should be active, open the client database.
	// This is done here so that Close always executes when lndMain returns.
	var towerClientDB *wtdb.ClientDB
	if cfg.WtClient.Active {
		var err er.R
		towerClientDB, err = wtdb.OpenClientDB(cfg.localDatabaseDir())
		if err != nil {
			err := er.Errorf("unable to open watchtower client "+
				"database: %v", err)
			log.Error(err)
			return err
		}
		defer towerClientDB.Close()
	}

	// If tor is active and either v2 or v3 onion services have been specified,
	// make a tor controller and pass it into both the watchtower server and
	// the regular lnd server.
	var torController *tor.Controller
	if cfg.Tor.Active && (cfg.Tor.V2 || cfg.Tor.V3) {
		torController = tor.NewController(
			cfg.Tor.Control, cfg.Tor.TargetIPAddress, cfg.Tor.Password,
		)

		// Start the tor controller before giving it to any other subsystems.
		if err := torController.Start(); err != nil {
			err := er.Errorf("unable to initialize tor controller: %v", err)
			log.Error(err)
			return err
		}
		defer func() {
			if err := torController.Stop(); err != nil {
				log.Errorf("error stopping tor controller: %v", err)
			}
		}()
	}

	var tower *watchtower.Standalone
	if cfg.Watchtower.Active {
		// Segment the watchtower directory by chain and network.
		towerDBDir := filepath.Join(
			cfg.Watchtower.TowerDir,
			cfg.registeredChains.PrimaryChain().String(),
			lncfg.NormalizeNetwork(cfg.ActiveNetParams.Name),
		)

		towerDB, err := wtdb.OpenTowerDB(towerDBDir)
		if err != nil {
			err := er.Errorf("unable to open watchtower "+
				"database: %v", err)
			log.Error(err)
			return err
		}
		defer towerDB.Close()

		towerKeyDesc, err := activeChainControl.KeyRing.DeriveKey(
			keychain.KeyLocator{
				Family: keychain.KeyFamilyTowerID,
				Index:  0,
			},
		)
		if err != nil {
			err := er.Errorf("error deriving tower key: %v", err)
			log.Error(err)
			return err
		}

		wtCfg := &watchtower.Config{
			BlockFetcher:   activeChainControl.ChainIO,
			DB:             towerDB,
			EpochRegistrar: activeChainControl.ChainNotifier,
			Net:            cfg.net,
			NewAddress: func() (btcutil.Address, er.R) {
				return activeChainControl.Wallet.NewAddress(
					lnwallet.WitnessPubKey, false,
				)
			},
			NodeKeyECDH: keychain.NewPubKeyECDH(
				towerKeyDesc, activeChainControl.KeyRing,
			),
			PublishTx: activeChainControl.Wallet.PublishTransaction,
			ChainHash: *cfg.ActiveNetParams.GenesisHash,
		}

		// If there is a tor controller (user wants auto hidden services), then
		// store a pointer in the watchtower config.
		if torController != nil {
			wtCfg.TorController = torController
			wtCfg.WatchtowerKeyPath = cfg.Tor.WatchtowerKeyPath

			switch {
			case cfg.Tor.V2:
				wtCfg.Type = tor.V2
			case cfg.Tor.V3:
				wtCfg.Type = tor.V3
			}
		}

		wtConfig, err := cfg.Watchtower.Apply(wtCfg, lncfg.NormalizeAddresses)
		if err != nil {
			err := er.Errorf("unable to configure watchtower: %v",
				err)
			log.Error(err)
			return err
		}

		tower, err = watchtower.New(wtConfig)
		if err != nil {
			err := er.Errorf("unable to create watchtower: %v", err)
			log.Error(err)
			return err
		}
	}

	// Initialize the ChainedAcceptor.
	chainedAcceptor := chanacceptor.NewChainedAcceptor()

	// Set up the core server which will listen for incoming peer
	// connections.
	server, err := newServer(
		cfg, cfg.Listeners, localChanDB, remoteChanDB, towerClientDB,
		activeChainControl, &idKeyDesc, walletInitParams.ChansToRestore,
		chainedAcceptor, torController,
	)
	if err != nil {
		err := er.Errorf("unable to create server: %v", err)
		log.Error(err)
		return err
	}

	if server.towerClient != nil {
		wtclient, err := wtclientrpc.New(&wtclientrpc.Config{
			Active:   true,
			Client:   server.towerClient,
			Resolver: cfg.net.ResolveTCPAddr,
		})
		if err != nil {
			return err
		}
		restContext.MaybeWatchTowerClient = wtclient
	}

	// Set up an autopilot manager from the current config. This will be
	// used to manage the underlying autopilot agent, starting and stopping
	// it at will.
	atplCfg, err := initAutoPilot(server, cfg.Autopilot, mainChain, cfg.ActiveNetParams)
	if err != nil {
		err := er.Errorf("unable to initialize autopilot: %v", err)
		log.Error(err)
		return err
	}

	atplManager, err := autopilot.NewManager(atplCfg)
	if err != nil {
		err := er.Errorf("unable to create autopilot manager: %v", err)
		log.Error(err)
		return err
	}
	if err := atplManager.Start(); err != nil {
		err := er.Errorf("unable to start autopilot manager: %v", err)
		log.Error(err)
		return err
	}
	defer atplManager.Stop()
	autopilotrpc.Register(atplManager, api.Category("lightning"))

	// Initialize, and register our implementation of the gRPC interface
	// exported by the rpcServer.
	rpcServer, err := newRPCServer(
		cfg,
		server,
		atplManager,
		server.invoices,
		tower,
		chainedAcceptor,
		metaService,
	)
	if err != nil {
		err := er.Errorf("unable to create RPC server: %v", err)
		log.Error(err)
		return err
	}
	restContext.MaybeRpcServer = rpcServer
	if err := rpcServer.Start(); err != nil {
		err := er.Errorf("unable to start RPC server: %v", err)
		log.Error(err)
		return err
	}
	defer rpcServer.Stop()

	routerRpc, err := routerrpc.New(cfg.SubRPCServers.RouterRPC)
	if err != nil {
		return err
	}
	restContext.MaybeRouterServer = routerRpc

	// We have brought up the RPC server so we can now cause the wallet/create
	// or wallet/unlock to complete.
	close(walletInitParams.Complete)

	// If we're not in regtest or simnet mode, We'll wait until we're fully
	// synced to continue the start up of the remainder of the daemon. This
	// ensures that we don't accept any possibly invalid state transitions, or
	// accept channels with spent funds.
	if !(cfg.Bitcoin.RegTest || cfg.Bitcoin.SimNet ||
		cfg.Litecoin.RegTest || cfg.Litecoin.SimNet) {

		bs, err := activeChainControl.ChainIO.BestBlock()
		if err != nil {
			err := er.Errorf("unable to determine chain tip: %v",
				err)
			log.Error(err)
			return err
		}

		log.Infof("Waiting for chain backend to finish sync, "+
			"start_height=%v", bs.Height)

		for {
			if !signal.Alive() {
				return nil
			}

			synced, _, err := activeChainControl.Wallet.IsSynced()
			if err != nil {
				err := er.Errorf("unable to determine if "+
					"wallet is synced: %v", err)
				log.Error(err)
				return err
			}

			if synced {
				break
			}

			time.Sleep(time.Second * 1)
		}

		bs, err = activeChainControl.ChainIO.BestBlock()
		if err != nil {
			err := er.Errorf("unable to determine chain tip: %v",
				err)
			log.Error(err)
			return err
		}

		log.Infof("Chain backend is fully synced (end_height=%v)!",
			bs.Height)
	}

	// With all the relevant chains initialized, we can finally start the
	// server itself.
	if err := server.Start(); err != nil {
		err := er.Errorf("unable to start server: %v", err)
		log.Error(err)
		return err
	}
	defer server.Stop()

	// Once the wallet is unlocked, and lnd server is ready
	// we can start listening for cjdns invoice requests
	if cfg.CjdnsSocket != "" {
		log.Infof("Listening for cjdns invoice request...")
		err := cjdns.Initialize(cfg.CjdnsSocket)
		if err != nil {
			log.Infof("Can not initialize cjdns: ", err)
		} else {
			if rs := restContext.MaybeRpcServer; rs != nil {
				//TESTING: ONLY from developer's host, will try to connect to hardcoded lnd peer
				hostname, _ := os.Hostname()
				if hostname == "x1" {
					//connect to lnd peer
					listPeersResponse, err := rs.ListPeers(context.TODO(), &rpc_pb.ListPeersRequest{})
					if err != nil {
						log.Error("Error listing lnd peers: ", err)
					}
					if len(listPeersResponse.Peers) == 0 {
						//TODO: read cjdns addres from cjdns peers
						// lndhost := "[fce3:86e9:b183:1a06:ad9a:c37f:14fe:36c2]:9735"
						lndhost := "192.168.1.12:9735"
						//TODO: get lnd identity pubkey
						lndPubkey := "037a44193419b4e58eb43607a521dc1da3bb262019c5ab565e7f4224714f1b5695"
						lndAddress := rpc_pb.LightningAddress{
							Pubkey: lndPubkey,
							Host:   lndhost,
						}
						connectRequest := &rpc_pb.ConnectPeerRequest{
							Addr: &lndAddress,
						}
						log.Infof("Connecting to LND peer: ", connectRequest.Addr.Host)
						for {
							_, err := rs.ConnectPeer(context.TODO(), connectRequest)
							if err != nil {
								log.Warnf("Error connecting to LND peer: ", err)
							} else {
								break
							}
							time.Sleep(time.Second * 5)
						}
					}
				}
			}
			for {
				invoiceRequestchan, err := cjdns.ListeningForInvoiceRequest()
				request := <-invoiceRequestchan
				if err != nil {
					log.Infof("Error listening for CJDNS invoice request: ", err)
				}
				if request.CjdnsAddr != "" {
					log.Infof("CJDNS invoice request: ", request)
					errorInvoiceResponse := &rpc_pb.RestError{
						Message: "Error creating invoice",
					}
					if rs := restContext.MaybeRpcServer; rs != nil {
						listPeersResponse, err := rs.ListPeers(context.TODO(), &rpc_pb.ListPeersRequest{})
						if err != nil {
							log.Errorf("Error listing lnd peers: ", err)
						}
						if len(listPeersResponse.Peers) == 0 {
							errorInvoiceResponse = &rpc_pb.RestError{
								Message: "No lnd peer connection",
							}
							cjdns.SendCjdnsInvoiceResponse(request, &rpc_pb.AddInvoiceResponse{}, errorInvoiceResponse)
						} else {
							invoice := &rpc_pb.Invoice{
								Value: int64(request.Amount),
							}
							// log.Infof("Creating invoice with: ", invoice)
							invoiceResponse, errr := rs.AddInvoice(context.TODO(), invoice)
							if errr != nil {
								log.Error("Error adding invoice: ", errr)
							}
							// log.Infof("Invoice response: ", invoiceResponse)
							errrr := cjdns.SendCjdnsInvoiceResponse(request, invoiceResponse, nil)
							if errrr != nil {
								log.Errorf("Error sending CJDNS invoice response: ", err)
							}
						}
					} else {
						log.Errorf("No rpc server...")
					}
				}
			}
		}
	}

	// Now that the server has started, if the autopilot mode is currently
	// active, then we'll start the autopilot agent immediately. It will be
	// stopped together with the autopilot service.
	if cfg.Autopilot.Active {
		if err := atplManager.StartAgent(); err != nil {
			err := er.Errorf("unable to start autopilot agent: %v",
				err)
			log.Error(err)
			return err
		}
	}

	if cfg.Watchtower.Active {
		if err := tower.Start(); err != nil {
			err := er.Errorf("unable to start watchtower: %v", err)
			log.Error(err)
			return err
		}
		defer tower.Stop()
	}

	// Wait for shutdown signal from either a graceful server stop or from
	// the interrupt handler.
	<-shutdownChan
	return nil
}

// WalletUnlockParams holds the variables used to parameterize the unlocking of
// lnd's wallet after it has already been created.
type WalletUnlockParams struct {
	// Password is the public and private wallet passphrase.
	Password []byte

	// Birthday specifies the approximate time that this wallet was created.
	// This is used to bound any rescans on startup.
	Birthday time.Time

	// RecoveryWindow specifies the address lookahead when entering recovery
	// mode. A recovery will be attempted if this value is non-zero.
	RecoveryWindow uint32

	// Wallet is the loaded and unlocked Wallet. This is returned
	// from the unlocker service to avoid it being unlocked twice (once in
	// the unlocker service to check if the password is correct and again
	// later when lnd actually uses it). Because unlocking involves scrypt
	// which is resource intensive, we want to avoid doing it twice.
	Wallet *wallet.Wallet

	// ChansToRestore a set of static channel backups that should be
	// restored before the main server instance starts up.
	ChansToRestore walletunlocker.ChannelsToRecover

	// UnloadWallet is a function for unloading the wallet, which should
	// be called on shutdown.
	UnloadWallet func() er.R

	Complete chan struct{}
}

// waitForWalletPassword will spin up gRPC and REST endpoints for the
// WalletUnlocker server, and block until a password is provided by
// the user to this RPC server.
func waitForWalletPassword(
	cfg *Config,
	restContext *RpcContext,
	api *apiv1.Apiv1,
) (*WalletUnlockParams, er.R) {

	chainConfig := cfg.Bitcoin
	if cfg.registeredChains.PrimaryChain() == chainreg.LitecoinChain {
		chainConfig = cfg.Litecoin
	} else if cfg.registeredChains.PrimaryChain() == chainreg.PktChain {
		chainConfig = cfg.Pkt
	}

	//Parse filename from --wallet or default
	walletPath, walletFilename := WalletFilename(cfg.WalletFile)
	//Get default pkt dir ~/.pktwallet/pkt
	if walletPath == "" {
		walletPath = cfg.PktDir
	}
	pwService := walletunlocker.New(
		chainConfig.ChainDir, cfg.ActiveNetParams.Params,
		!cfg.SyncFreelist, walletPath, walletFilename, api,
	)
	restContext.MaybeWalletUnlocker = pwService

	// Wait for user to provide the password.
	log.Infof("Waiting for wallet (" + walletFilename + ") encryption password. " +
		"See `./bin/pldctl help wallet/create` " +
		"to create a wallet, or `./bin/pldctl help wallet/unlock` " +
		"to unlock an existing wallet, or `./bin/pldctl help wallet/changepassword` to change the " +
		"password of an existing wallet and unlock it.")

	// We currently don't distinguish between getting a password to be used
	// for creation or unlocking, as a new wallet db will be created if
	// none exists when creating the chain control.
	select {

	// The wallet is being created for the first time, we'll check to see
	// if the user provided any entropy for seed creation. If so, then
	// we'll create the wallet early to load the seed.
	case initMsg := <-pwService.InitMsgs:
		password := initMsg.Passphrase
		cipherSeed := initMsg.Seed
		recoveryWindow := initMsg.RecoveryWindow

		if initMsg.WalletName != "" {
			walletFilename = initMsg.WalletName
		}

		loader := wallet.NewLoader(
			cfg.ActiveNetParams.Params, walletPath, walletFilename, !cfg.SyncFreelist,
			recoveryWindow,
		)

		newWallet, err := loader.CreateNewWallet(
			[]byte(wallet.InsecurePubPassphrase), password,
			initMsg.LegacySeed, time.Time{}, cipherSeed, api,
		)
		if err != nil {
			// Don't leave the file open in case the new wallet
			// could not be created for whatever reason.
			if err := loader.UnloadWallet(); err != nil {
				log.Errorf("Could not unload new "+
					"wallet: %v", err)
			}
			return nil, err
		}

		// For new wallets, the ResetWalletTransactions flag is a no-op.
		if cfg.ResetWalletTransactions {
			log.Warnf("Ignoring reset-wallet-transactions " +
				"flag for new wallet as it has no effect")
		}

		birthday := time.Time{}
		if cipherSeed != nil {
			birthday = cipherSeed.Birthday()
		}

		return &WalletUnlockParams{
			Password:       password,
			Birthday:       birthday,
			RecoveryWindow: recoveryWindow,
			Wallet:         newWallet,
			ChansToRestore: initMsg.ChanBackups,
			UnloadWallet:   loader.UnloadWallet,
			Complete:       initMsg.Complete,
		}, nil

	// The wallet has already been created in the past, and is simply being
	// unlocked. So we'll just return these passphrases.
	case unlockMsg := <-pwService.UnlockMsgs:
		// Resetting the transactions is something the user likely only
		// wants to do once so we add a prominent warning to the log to
		// remind the user to turn off the setting again after
		// successful completion.
		if cfg.ResetWalletTransactions {
			log.Warnf("Dropping all transaction history from " +
				"on-chain wallet. Remember to disable " +
				"reset-wallet-transactions flag for next " +
				"start of pld")

			err := wallet.DropTransactionHistory(
				unlockMsg.Wallet.Database(), true,
			)
			if err != nil {
				if err := unlockMsg.UnloadWallet(); err != nil {
					log.Errorf("Could not unload "+
						"wallet: %v", err)
				}
				return nil, err
			}
		}

		return &WalletUnlockParams{
			Password:       unlockMsg.Passphrase,
			RecoveryWindow: unlockMsg.RecoveryWindow,
			Wallet:         unlockMsg.Wallet,
			ChansToRestore: unlockMsg.ChanBackups,
			UnloadWallet:   unlockMsg.UnloadWallet,
			Complete:       unlockMsg.Complete,
		}, nil

	case <-signal.ShutdownChannel():
		return nil, er.Errorf("shutting down")
	}
}

// initializeDatabases extracts the current databases that we'll use for normal
// operation in the daemon. Two databases are returned: one remote and one
// local. However, only if the replicated database is active will the remote
// database point to a unique database. Otherwise, the local and remote DB will
// both point to the same local database. A function closure that closes all
// opened databases is also returned.
func initializeDatabases(ctx context.Context,
	cfg *Config) (*channeldb.DB, *channeldb.DB, func(), er.R) {

	log.Infof("Opening the main database, this might take a few " +
		"minutes...")

	if cfg.DB.Backend == lncfg.BoltBackend {
		log.Infof("Opening bbolt database, sync_freelist=%v, "+
			"auto_compact=%v", cfg.DB.Bolt.SyncFreelist,
			cfg.DB.Bolt.AutoCompact)
	}

	startOpenTime := time.Now()

	databaseBackends, err := cfg.DB.GetBackends(
		ctx, cfg.localDatabaseDir(), cfg.networkName(),
	)
	if err != nil {
		return nil, nil, nil, er.Errorf("unable to obtain database "+
			"backends: %v", err)
	}

	// If the remoteDB is nil, then we'll just open a local DB as normal,
	// having the remote and local pointer be the exact same instance.
	var (
		localChanDB, remoteChanDB *channeldb.DB
		closeFuncs                []func()
	)
	if databaseBackends.RemoteDB == nil {
		// Open the channeldb, which is dedicated to storing channel,
		// and network related metadata.
		localChanDB, err = channeldb.CreateWithBackend(
			databaseBackends.LocalDB,
			channeldb.OptionSetRejectCacheSize(cfg.Caches.RejectCacheSize),
			channeldb.OptionSetChannelCacheSize(cfg.Caches.ChannelCacheSize),
			channeldb.OptionDryRunMigration(cfg.DryRunMigration),
		)
		switch {
		case channeldb.ErrDryRunMigrationOK.Is(err):
			return nil, nil, nil, err

		case err != nil:
			err := er.Errorf("unable to open local channeldb: %v", err)
			log.Error(err)
			return nil, nil, nil, err
		}

		closeFuncs = append(closeFuncs, func() {
			localChanDB.Close()
		})

		remoteChanDB = localChanDB
	} else {
		log.Infof("Database replication is available! Creating " +
			"local and remote channeldb instances")

		// Otherwise, we'll open two instances, one for the state we
		// only need locally, and the other for things we want to
		// ensure are replicated.
		localChanDB, err = channeldb.CreateWithBackend(
			databaseBackends.LocalDB,
			channeldb.OptionSetRejectCacheSize(cfg.Caches.RejectCacheSize),
			channeldb.OptionSetChannelCacheSize(cfg.Caches.ChannelCacheSize),
			channeldb.OptionDryRunMigration(cfg.DryRunMigration),
		)
		switch {
		// As we want to allow both versions to get thru the dry run
		// migration, we'll only exit the second time here once the
		// remote instance has had a time to migrate as well.
		case channeldb.ErrDryRunMigrationOK.Is(err):
			log.Infof("Local DB dry run migration successful")

		case err != nil:
			err := er.Errorf("unable to open local channeldb: %v", err)
			log.Error(err)
			return nil, nil, nil, err
		}

		closeFuncs = append(closeFuncs, func() {
			localChanDB.Close()
		})

		log.Infof("Opening replicated database instance...")

		remoteChanDB, err = channeldb.CreateWithBackend(
			databaseBackends.RemoteDB,
			channeldb.OptionDryRunMigration(cfg.DryRunMigration),
		)
		switch {
		case channeldb.ErrDryRunMigrationOK.Is(err):
			return nil, nil, nil, err

		case err != nil:
			localChanDB.Close()

			err := er.Errorf("unable to open remote channeldb: %v", err)
			log.Error(err)
			return nil, nil, nil, err
		}

		closeFuncs = append(closeFuncs, func() {
			remoteChanDB.Close()
		})
	}

	openTime := time.Since(startOpenTime)
	log.Infof("Database now open (time_to_open=%v)!", openTime)

	cleanUp := func() {
		for _, closeFunc := range closeFuncs {
			closeFunc()
		}
	}

	return localChanDB, remoteChanDB, cleanUp, nil
}

// initNeutrinoBackend inits a new instance of the neutrino light client
// backend given a target chain directory to store the chain state.
func initNeutrinoBackend(cfg *Config, chainDir string, napi *apiv1.Apiv1) (*neutrino.ChainService,
	func(), er.R) {

	// Ensure that the neutrino db path exists.
	if errr := os.MkdirAll(chainDir, 0700); errr != nil {
		return nil, nil, er.E(errr)
	}

	dbName := filepath.Join(chainDir, "neutrino.db")
	db, err := walletdb.Create("bdb", dbName, !cfg.SyncFreelist)
	if err != nil {
		return nil, nil, er.Errorf("unable to create neutrino "+
			"database: %v", err)
	}

	headerStateAssertion, errr := parseHeaderStateAssertion(
		cfg.NeutrinoMode.AssertFilterHeader,
	)
	if errr != nil {
		db.Close()
		return nil, nil, errr
	}

	// With the database open, we can now create an instance of the
	// neutrino light client. We pass in relevant configuration parameters
	// required.
	config := neutrino.Config{
		DataDir:      chainDir,
		Database:     db,
		ChainParams:  *cfg.ActiveNetParams.Params,
		AddPeers:     cfg.NeutrinoMode.AddPeers,
		ConnectPeers: cfg.NeutrinoMode.ConnectPeers,
		Dialer: func(addr net.Addr) (net.Conn, er.R) {
			return cfg.net.Dial(
				addr.Network(), addr.String(),
				cfg.ConnectionTimeout,
			)
		},
		NameResolver: func(host string) ([]net.IP, er.R) {
			addrs, err := cfg.net.LookupHost(host)
			if err != nil {
				return nil, err
			}

			ips := make([]net.IP, 0, len(addrs))
			for _, strIP := range addrs {
				ip := net.ParseIP(strIP)
				if ip == nil {
					continue
				}

				ips = append(ips, ip)
			}

			return ips, nil
		},
		AssertFilterHeader: headerStateAssertion,
		CheckConectivity:   cfg.NeutrinoMode.CheckConectivity,
	}

	// neutrino.MaxPeers = 8
	neutrino.TargetOutbound = 8

	neutrino.BanDuration = time.Hour * 48
	neutrino.UserAgentName = cfg.NeutrinoMode.UserAgentName
	neutrino.UserAgentVersion = cfg.NeutrinoMode.UserAgentVersion

	neutrinoCS, err := neutrino.NewChainService(config, napi)
	if err != nil {
		db.Close()
		return nil, nil, er.Errorf("unable to create neutrino light "+
			"client: %v", err)
	}

	if err := neutrinoCS.Start(); err != nil {
		db.Close()
		return nil, nil, err
	}

	cleanUp := func() {
		neutrinoCS.Stop()
		db.Close()
	}

	return neutrinoCS, cleanUp, nil
}

// parseHeaderStateAssertion parses the user-specified neutrino header state
// into a headerfs.FilterHeader.
func parseHeaderStateAssertion(state string) (*headerfs.FilterHeader, er.R) {
	if len(state) == 0 {
		return nil, nil
	}

	split := strings.Split(state, ":")
	if len(split) != 2 {
		return nil, er.Errorf("header state assertion %v in "+
			"unexpected format, expected format height:hash", state)
	}

	height, errr := strconv.ParseUint(split[0], 10, 32)
	if errr != nil {
		return nil, er.Errorf("invalid filter header height: %v", errr)
	}

	hash, err := chainhash.NewHashFromStr(split[1])
	if err != nil {
		return nil, er.Errorf("invalid filter header hash: %v", err)
	}

	return &headerfs.FilterHeader{
		Height:     uint32(height),
		FilterHash: *hash,
	}, nil
}

// Parse wallet filename,
// return path and filename when it starts with /
func WalletFilename(walletName string) (string, string) {
	if strings.HasSuffix(walletName, ".db") {
		if strings.HasPrefix(walletName, "/") {
			dir, filename := filepath.Split(walletName)
			return dir, filename
		}
		return "", walletName
	} else {
		return "", fmt.Sprintf("wallet_%s.db", walletName)
	}
}
