package lnd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/pkt-cash/pktd/blockchain"
	"github.com/pkt-cash/pktd/btcec"
	"github.com/pkt-cash/pktd/btcjson"
	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/psbt"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/chaincfg"
	"github.com/pkt-cash/pktd/chaincfg/chainhash"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/autopilot"
	"github.com/pkt-cash/pktd/lnd/chainreg"
	"github.com/pkt-cash/pktd/lnd/chanacceptor"
	"github.com/pkt-cash/pktd/lnd/chanbackup"
	"github.com/pkt-cash/pktd/lnd/chanfitness"
	"github.com/pkt-cash/pktd/lnd/channeldb"
	"github.com/pkt-cash/pktd/lnd/channeldb/kvdb"
	"github.com/pkt-cash/pktd/lnd/channelnotifier"
	"github.com/pkt-cash/pktd/lnd/contractcourt"
	"github.com/pkt-cash/pktd/lnd/describetxn"
	"github.com/pkt-cash/pktd/lnd/discovery"
	"github.com/pkt-cash/pktd/lnd/feature"
	"github.com/pkt-cash/pktd/lnd/htlcswitch"
	"github.com/pkt-cash/pktd/lnd/htlcswitch/hop"
	"github.com/pkt-cash/pktd/lnd/input"
	"github.com/pkt-cash/pktd/lnd/invoices"
	"github.com/pkt-cash/pktd/lnd/keychain"
	"github.com/pkt-cash/pktd/lnd/labels"
	"github.com/pkt-cash/pktd/lnd/lncfg"
	"github.com/pkt-cash/pktd/lnd/lnrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/invoicesrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/routerrpc"
	"github.com/pkt-cash/pktd/lnd/lntypes"
	"github.com/pkt-cash/pktd/lnd/lnwallet"
	"github.com/pkt-cash/pktd/lnd/lnwallet/btcwallet"
	"github.com/pkt-cash/pktd/lnd/lnwallet/chainfee"
	"github.com/pkt-cash/pktd/lnd/lnwallet/chancloser"
	"github.com/pkt-cash/pktd/lnd/lnwallet/chanfunding"
	"github.com/pkt-cash/pktd/lnd/lnwire"
	"github.com/pkt-cash/pktd/lnd/peer"
	"github.com/pkt-cash/pktd/lnd/peernotifier"
	"github.com/pkt-cash/pktd/lnd/record"
	"github.com/pkt-cash/pktd/lnd/routing"
	"github.com/pkt-cash/pktd/lnd/routing/route"
	"github.com/pkt-cash/pktd/lnd/sweep"
	"github.com/pkt-cash/pktd/lnd/watchtower"
	"github.com/pkt-cash/pktd/lnd/zpay32"
	"github.com/pkt-cash/pktd/pktconfig/version"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/waddrmgr"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/pktwallet/wallet/seedwords"
	"github.com/pkt-cash/pktd/pktwallet/wallet/txauthor"
	"github.com/pkt-cash/pktd/pktwallet/wallet/txrules"
	"github.com/pkt-cash/pktd/pktwallet/walletdb"
	"github.com/pkt-cash/pktd/txscript"
	"github.com/pkt-cash/pktd/wire"
	"github.com/pkt-cash/pktd/wire/ruleerror"
)

type LightningRPCServer struct {
	server *server

	cfg *Config

	// routerBackend contains the backend implementation of the router
	// rpc sub server.
	routerBackend *routerrpc.RouterBackend

	// chanPredicate is used in the bidirectional ChannelAcceptor streaming
	// method.
	chanPredicate *chanacceptor.ChainedAcceptor

	quit chan struct{}

	// selfNode is our own pubkey.
	selfNode route.Vertex

	wallet *wallet.Wallet

	rpc_pb.UnimplementedLightningServer
}

// newRPCServer creates and returns a new instance of the rpcServer. The
// rpcServer will handle creating all listening sockets needed by it, and any
// of the sub-servers that it maintains. The set of serverOpts should be the
// base level options passed to the grPC server. This typically includes things
// like requiring TLS, etc.
func newRPCServer(
	cfg *Config,
	s *server,
	atpl *autopilot.Manager,
	invoiceRegistry *invoices.InvoiceRegistry,
	tower *watchtower.Standalone,
	chanPredicate *chanacceptor.ChainedAcceptor,
	metaService *lnrpc.MetaService,
) (*LightningRPCServer, er.R) {

	// Set up router rpc backend.
	channelGraph := s.localChanDB.ChannelGraph()
	selfNode, err := channelGraph.SourceNode()
	if err != nil {
		return nil, err
	}
	graph := s.localChanDB.ChannelGraph()
	routerBackend := &routerrpc.RouterBackend{
		SelfNode: selfNode.PubKeyBytes,
		FetchChannelCapacity: func(chanID uint64) (btcutil.Amount,
			er.R) {

			info, _, _, err := graph.FetchChannelEdgesByID(chanID)
			if err != nil {
				return 0, err
			}
			return info.Capacity, nil
		},
		FetchChannelEndpoints: func(chanID uint64) (route.Vertex,
			route.Vertex, er.R) {

			info, _, _, err := graph.FetchChannelEdgesByID(
				chanID,
			)
			if err != nil {
				return route.Vertex{}, route.Vertex{},
					er.Errorf("unable to fetch channel "+
						"edges by channel ID %d: %v",
						chanID, err)
			}

			return info.NodeKey1Bytes, info.NodeKey2Bytes, nil
		},
		FindRoute:              s.chanRouter.FindRoute,
		MissionControl:         s.missionControl,
		ActiveNetParams:        cfg.ActiveNetParams.Params,
		Tower:                  s.controlTower,
		MaxTotalTimelock:       cfg.MaxOutgoingCltvExpiry,
		DefaultFinalCltvDelta:  uint16(cfg.Bitcoin.TimeLockDelta),
		SubscribeHtlcEvents:    s.htlcNotifier.SubscribeHtlcEvents,
		InterceptableForwarder: s.interceptableSwitch,
	}

	genInvoiceFeatures := func() *lnwire.FeatureVector {
		return s.featureMgr.Get(feature.SetInvoice)
	}

	// Before we create any of the sub-servers, we need to ensure that all
	// the dependencies they need are properly populated within each sub
	// server configuration struct.
	//
	// TODO(roasbeef): extend sub-sever config to have both (local vs remote) DB
	err = cfg.SubRPCServers.PopulateDependencies(
		cfg, s.cc, cfg.networkDir, atpl, invoiceRegistry,
		s.htlcSwitch, cfg.ActiveNetParams.Params, s.chanRouter,
		routerBackend, s.nodeSigner, s.remoteChanDB, s.sweeper, tower,
		s.towerClient, cfg.net.ResolveTCPAddr, genInvoiceFeatures,
	)
	if err != nil {
		return nil, err
	}

	rootRPCServer := &LightningRPCServer{
		cfg:           cfg,
		server:        s,
		routerBackend: routerBackend,
		chanPredicate: chanPredicate,
		quit:          make(chan struct{}, 1),
		selfNode:      selfNode.PubKeyBytes,
		wallet:        metaService.Wallet,
	}
	return rootRPCServer, nil
}

// Start launches any helper goroutines required for the rpcServer to function.
func (r *LightningRPCServer) Start() er.R {
	return nil
}

// Stop signals any active goroutines for a graceful closure.
func (r *LightningRPCServer) Stop() er.R {
	close(r.quit)

	return nil
}

// addrPairsToOutputs converts a map describing a set of outputs to be created,
// the outputs themselves. The passed map pairs up an address, to a desired
// output value amount. Each address is converted to its corresponding pkScript
// to be used within the constructed output(s).
func addrPairsToOutputs(addrPairs map[string]int64,
	params *chaincfg.Params) ([]*wire.TxOut, er.R) {

	outputs := make([]*wire.TxOut, 0, len(addrPairs))
	for addr, amt := range addrPairs {
		addr, err := btcutil.DecodeAddress(addr, params)
		if err != nil {
			return nil, err
		}

		pkscript, err := txscript.PayToAddrScript(addr)
		if err != nil {
			return nil, err
		}

		outputs = append(outputs, wire.NewTxOut(amt, pkscript))
	}

	return outputs, nil
}

// allowCORS wraps the given http.Handler with a function that adds the
// Access-Control-Allow-Origin header to the response.
func allowCORS(handler http.Handler, origins []string) http.Handler {
	allowHeaders := "Access-Control-Allow-Headers"
	allowMethods := "Access-Control-Allow-Methods"
	allowOrigin := "Access-Control-Allow-Origin"

	// If the user didn't supply any origins that means CORS is disabled
	// and we should return the original handler.
	if len(origins) == 0 {
		return handler
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Skip everything if the browser doesn't send the Origin field.
		if origin == "" {
			handler.ServeHTTP(w, r)
			return
		}

		// Set the static header fields first.
		w.Header().Set(
			allowHeaders,
			"Content-Type, Accept, Grpc-Metadata-Macaroon",
		)
		w.Header().Set(allowMethods, "GET, POST, DELETE")

		// Either we allow all origins or the incoming request matches
		// a specific origin in our list of allowed origins.
		for _, allowedOrigin := range origins {
			if allowedOrigin == "*" || origin == allowedOrigin {
				// Only set allowed origin to requested origin.
				w.Header().Set(allowOrigin, origin)

				break
			}
		}

		// For a pre-flight request we only need to send the headers
		// back. No need to call the rest of the chain.
		if r.Method == "OPTIONS" {
			return
		}

		// Everything's prepared now, we can pass the request along the
		// chain of handlers.
		handler.ServeHTTP(w, r)
	})
}

// sendCoinsOnChain makes an on-chain transaction in or to send coins to one or
// more addresses specified in the passed payment map. The payment map maps an
// address to a specified output value to be sent to that address.
func (r *LightningRPCServer) sendCoinsOnChain(paymentMap map[string]int64,
	feeRate chainfee.SatPerKWeight, minconf int32,
	label string) (*chainhash.Hash, er.R) {

	outputs, err := addrPairsToOutputs(paymentMap, r.cfg.ActiveNetParams.Params)
	if err != nil {
		return nil, err
	}

	tx, err := r.server.cc.Wallet.SendOutputs(outputs, feeRate, minconf, label)
	if err != nil {
		return nil, err
	}

	txHash := tx.TxHash()
	return &txHash, nil
}

// ListUnspent returns useful information about each unspent output owned by the
// wallet, as reported by the underlying `ListUnspentWitness`; the information
// returned is: outpoint, amount in satoshis, address, address type,
// scriptPubKey in hex and number of confirmations.  The result is filtered to
// contain outputs whose number of confirmations is between a minimum and
// maximum number of confirmations specified by the user, with 0 meaning
// unconfirmed.
func (r *LightningRPCServer) ListUnspent(ctx context.Context,
	in *rpc_pb.ListUnspentRequest) (*rpc_pb.ListUnspentResponse, er.R) {

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
	err = r.server.cc.Wallet.WithCoinSelectLock(func() er.R {
		utxos, err = r.server.cc.Wallet.ListUnspentWitness(
			minConfs, maxConfs,
		)
		return err
	})
	if err != nil {
		return nil, err
	}

	rpcUtxos, err := lnrpc.MarshalUtxos(utxos, r.cfg.ActiveNetParams.Params)
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

// EstimateFee handles a request for estimating the fee for sending a
// transaction spending to multiple specified outputs in parallel.
func (r *LightningRPCServer) EstimateFee(ctx context.Context,
	in *rpc_pb.EstimateFeeRequest) (*rpc_pb.EstimateFeeResponse, er.R) {

	for addr, amnt := range in.AddrToAmount {
		log.Debugf("[0] EstimateFee(): address: %s ; amount: %d", addr, amnt)
	}

	// Create the list of outputs we are spending to.
	outputs, err := addrPairsToOutputs(in.AddrToAmount, r.cfg.ActiveNetParams.Params)
	if err != nil {
		return nil, err
	}
	log.Debugf("[1] EstimateFee(): #outputs: %d", len(outputs))

	// Query the fee estimator for the fee rate for the given confirmation
	// target.
	target := in.TargetConf
	feePerKw, err := sweep.DetermineFeePerKw(
		r.server.cc.FeeEstimator, sweep.FeePreference{
			ConfTarget: uint32(target),
		},
	)
	if err != nil {
		return nil, err
	}
	log.Debugf("[2] EstimateFee(): feePerKw: %d", feePerKw)

	// We will ask the wallet to create a tx using this fee rate. We set
	// dryRun=true to avoid inflating the change addresses in the db.
	var tx *txauthor.AuthoredTx
	walletx := r.server.cc.Wallet
	err = walletx.WithCoinSelectLock(func() er.R {
		tx, err = walletx.CreateSimpleTx(outputs, feePerKw, wallet.SendModeUnsigned)
		return err
	})
	if err != nil {
		return nil, err
	}
	log.Debugf("[3] EstimateFee()")

	// Use the created tx to calculate the total fee.
	totalOutput := int64(0)
	for _, out := range tx.Tx.TxOut {
		totalOutput += out.Value
	}
	totalFee := int64(tx.TotalInput) - totalOutput

	resp := &rpc_pb.EstimateFeeResponse{
		FeeSat:            totalFee,
		FeerateSatPerByte: int64(feePerKw.FeePerKVByte() / 1000),
	}
	log.Debugf("[4] EstimateFee()")

	log.Debugf("[estimatefee] fee estimate for conf target %d: %v",
		target, resp)

	return resp, nil
}

// SendCoins executes a request to send coins to a particular address. Unlike
// SendMany, this RPC call only allows creating a single output at a time.
func (r *LightningRPCServer) SendCoins(ctx context.Context,
	in *rpc_pb.SendCoinsRequest) (*rpc_pb.SendCoinsResponse, error) {

	// Based on the passed fee related parameters, we'll determine an
	// appropriate fee rate for this transaction.
	satPerKw := chainfee.SatPerKVByte(in.SatPerByte * 1000).FeePerKWeight()
	feePerKw, err := sweep.DetermineFeePerKw(
		r.server.cc.FeeEstimator, sweep.FeePreference{
			ConfTarget: uint32(in.TargetConf),
			FeeRate:    satPerKw,
		},
	)
	if err != nil {
		return nil, er.Native(err)
	}

	// Then, we'll extract the minimum number of confirmations that each
	// output we use to fund the transaction should satisfy.
	minConfs, err := lnrpc.ExtractMinConfs(in.MinConfs, in.SpendUnconfirmed)
	if err != nil {
		return nil, er.Native(err)
	}

	log.Infof("[sendcoins] addr=%v, amt=%v, sat/kw=%v, min_confs=%v, "+
		"sweep_all=%v",
		in.Addr, btcutil.Amount(in.Amount), int64(feePerKw), minConfs,
		in.SendAll)

	// Decode the address receiving the coins, we need to check whether the
	// address is valid for this network.
	targetAddr, err := btcutil.DecodeAddress(
		in.Addr, r.cfg.ActiveNetParams.Params,
	)
	if err != nil {
		return nil, er.Native(err)
	}

	// Make the check on the decoded address according to the active network.
	if !targetAddr.IsForNet(r.cfg.ActiveNetParams.Params) {
		return nil, er.Native(er.Errorf("address: %v is not valid for this "+
			"network: %v", targetAddr.String(),
			r.cfg.ActiveNetParams.Params.Name))
	}

	// If the destination address parses to a valid pubkey, we assume the user
	// accidentally tried to send funds to a bare pubkey address. This check is
	// here to prevent unintended transfers.
	decodedAddr, _ := util.DecodeHex(in.Addr)
	_, err = btcec.ParsePubKey(decodedAddr, btcec.S256())
	if err == nil {
		return nil, er.Native(er.Errorf("cannot send coins to pubkeys"))
	}

	label, err := labels.ValidateAPI(in.Label)
	if err != nil {
		return nil, er.Native(err)
	}

	var txid *chainhash.Hash

	wallet := r.server.cc.Wallet

	// If the send all flag is active, then we'll attempt to sweep all the
	// coins in the wallet in a single transaction (if possible),
	// otherwise, we'll respect the amount, and attempt a regular 2-output
	// send.
	if in.SendAll {
		// At this point, the amount shouldn't be set since we've been
		// instructed to sweep all the coins from the wallet.
		if in.Amount != 0 {
			return nil, er.Native(er.Errorf("amount set while SendAll is " +
				"active"))
		}

		bs, err := r.server.cc.ChainIO.BestBlock()
		if err != nil {
			return nil, er.Native(err)
		}

		// With the sweeper instance created, we can now generate a
		// transaction that will sweep ALL outputs from the wallet in a
		// single transaction. This will be generated in a concurrent
		// safe manner, so no need to worry about locking.
		sweepTxPkg, err := sweep.CraftSweepAllTx(
			feePerKw, lnwallet.DefaultDustLimit(),
			uint32(bs.Height), targetAddr, wallet,
			wallet.WalletController, wallet.WalletController,
			r.server.cc.FeeEstimator, r.server.cc.Signer,
		)
		if err != nil {
			return nil, er.Native(err)
		}

		log.Debugf("Sweeping all coins from wallet to addr=%v, "+
			"with tx=%v", in.Addr, spew.Sdump(sweepTxPkg.SweepTx))

		// As our sweep transaction was created, successfully, we'll
		// now attempt to publish it, cancelling the sweep pkg to
		// return all outputs if it fails.
		err = wallet.PublishTransaction(sweepTxPkg.SweepTx, label)
		if err != nil {
			sweepTxPkg.CancelSweepAttempt()

			return nil, er.Native(er.Errorf("unable to broadcast sweep "+
				"transaction: %v", err))
		}

		sweepTXID := sweepTxPkg.SweepTx.TxHash()
		txid = &sweepTXID
	} else {

		// We'll now construct out payment map, and use the wallet's
		// coin selection synchronization method to ensure that no coin
		// selection (funding, sweep alls, other sends) can proceed
		// while we instruct the wallet to send this transaction.
		paymentMap := map[string]int64{targetAddr.String(): in.Amount}
		err := wallet.WithCoinSelectLock(func() er.R {
			newTXID, err := r.sendCoinsOnChain(
				paymentMap, feePerKw, minConfs, label,
			)
			if err != nil {
				return err
			}

			txid = newTXID

			return nil
		})
		if err != nil {
			return nil, er.Native(err)
		}
	}

	log.Infof("[sendcoins] spend generated txid: %v", txid.String())

	return &rpc_pb.SendCoinsResponse{Txid: txid.String()}, nil
}

// SendMany handles a request for a transaction create multiple specified
// outputs in parallel.
func (r *LightningRPCServer) SendMany(ctx context.Context,
	in *rpc_pb.SendManyRequest) (*rpc_pb.SendManyResponse, error) {

	// Based on the passed fee related parameters, we'll determine an
	// appropriate fee rate for this transaction.
	satPerKw := chainfee.SatPerKVByte(in.SatPerByte * 1000).FeePerKWeight()
	feePerKw, err := sweep.DetermineFeePerKw(
		r.server.cc.FeeEstimator, sweep.FeePreference{
			ConfTarget: uint32(in.TargetConf),
			FeeRate:    satPerKw,
		},
	)
	if err != nil {
		return nil, er.Native(err)
	}

	// Then, we'll extract the minimum number of confirmations that each
	// output we use to fund the transaction should satisfy.
	minConfs, err := lnrpc.ExtractMinConfs(in.MinConfs, in.SpendUnconfirmed)
	if err != nil {
		return nil, er.Native(err)
	}

	label, err := labels.ValidateAPI(in.Label)
	if err != nil {
		return nil, er.Native(err)
	}

	log.Infof("[sendmany] outputs=%v, sat/kw=%v",
		spew.Sdump(in.AddrToAmount), int64(feePerKw))

	var txid *chainhash.Hash

	// We'll attempt to send to the target set of outputs, ensuring that we
	// synchronize with any other ongoing coin selection attempts which
	// happen to also be concurrently executing.
	wallet := r.server.cc.Wallet
	err = wallet.WithCoinSelectLock(func() er.R {
		sendManyTXID, err := r.sendCoinsOnChain(
			in.AddrToAmount, feePerKw, minConfs, label,
		)
		if err != nil {
			return err
		}

		txid = sendManyTXID

		return nil
	})
	if err != nil {
		return nil, er.Native(err)
	}

	log.Infof("[sendmany] spend generated txid: %v", txid.String())

	return &rpc_pb.SendManyResponse{Txid: txid.String()}, nil
}

// NewAddress creates a new address under control of the local wallet.
func (r *LightningRPCServer) NewAddress(ctx context.Context,
	in *rpc_pb.NewAddressRequest) (*rpc_pb.NewAddressResponse, error) {

	// Translate the gRPC proto address type to the wallet controller's
	// available address types.
	var (
		addr btcutil.Address
		err  er.R
	)
	switch in.Type {
	case rpc_pb.AddressType_WITNESS_PUBKEY_HASH:
		addr, err = r.server.cc.Wallet.NewAddress(
			lnwallet.WitnessPubKey, false,
		)
		if err != nil {
			return nil, er.Native(err)
		}

	case rpc_pb.AddressType_NESTED_PUBKEY_HASH:
		addr, err = r.server.cc.Wallet.NewAddress(
			lnwallet.NestedWitnessPubKey, false,
		)
		if err != nil {
			return nil, er.Native(err)
		}

	case rpc_pb.AddressType_UNUSED_WITNESS_PUBKEY_HASH:
		addr, err = r.server.cc.Wallet.LastUnusedAddress(
			lnwallet.WitnessPubKey,
		)
		if err != nil {
			return nil, er.Native(err)
		}

	case rpc_pb.AddressType_UNUSED_NESTED_PUBKEY_HASH:
		addr, err = r.server.cc.Wallet.LastUnusedAddress(
			lnwallet.NestedWitnessPubKey,
		)
		if err != nil {
			return nil, er.Native(err)
		}
	}

	log.Debugf("[newaddress] type=%v addr=%v", in.Type, addr.String())
	return &rpc_pb.NewAddressResponse{Address: addr.String()}, nil
}

var (
	// signedMsgPrefix is a special prefix that we'll prepend to any
	// messages we sign/verify. We do this to ensure that we don't
	// accidentally sign a sighash, or other sensitive material. By
	// prepending this fragment, we mind message signing to our particular
	// context.
	signedMsgPrefix = []byte("Lightning Signed Message:")
)

// SignMessage signs a message with the resident node's private key. The
// returned signature string is zbase32 encoded and pubkey recoverable, meaning
// that only the message digest and signature are needed for verification.
func (r *LightningRPCServer) SignMessage(ctx context.Context,
	in *rpc_pb.SignMessageRequest) (*rpc_pb.SignMessageResponse, er.R) {

	//	make sure request have a non empty MsgBin or Msg
	if (in.MsgBin == nil || len(in.MsgBin) == 0) && len(in.Msg) == 0 {
		return nil, er.Errorf("need a message to sign")
	}

	//	if request have both MsgBin and Msg, sign only MsgBin
	var msg []byte

	if in.MsgBin != nil && len(in.MsgBin) > 0 {
		msg = in.MsgBin
	} else {
		msg = []byte(in.Msg)
	}

	msg = append(signedMsgPrefix, msg...)
	src, err := r.server.nodeSigner.SignCompact(msg)
	if err != nil {
		return nil, err
	}
	sigBytes := base64.StdEncoding.EncodeToString(src)

	return &rpc_pb.SignMessageResponse{Signature: sigBytes}, nil
}

// ConnectPeer attempts to establish a connection to a remote peer.
func (r *LightningRPCServer) ConnectPeer(ctx context.Context,
	in *rpc_pb.ConnectPeerRequest) (*rpc_pb.Null, er.R) {

	// The server hasn't yet started, so it won't be able to service any of
	// our requests, so we'll bail early here.
	if !r.server.Started() {
		return nil, ErrServerNotActive.Default()
	}

	if in.Addr == nil {
		return nil, er.Errorf("need: lnc pubkeyhash@hostname")
	}

	pubkeyHex, err := util.DecodeHex(in.Addr.Pubkey)
	if err != nil {
		return nil, err
	}
	pubKey, err := btcec.ParsePubKey(pubkeyHex, btcec.S256())
	if err != nil {
		return nil, err
	}

	// Connections to ourselves are disallowed for obvious reasons.
	if pubKey.IsEqual(r.server.identityECDH.PubKey()) {
		return nil, er.Errorf("cannot make connection to self")
	}

	addr, err := parseAddr(in.Addr.Host, r.cfg.net)
	if err != nil {
		return nil, err
	}

	peerAddr := &lnwire.NetAddress{
		IdentityKey: pubKey,
		Address:     addr,
		ChainNet:    r.cfg.ActiveNetParams.Net,
	}

	log.Debugf("[connectpeer] requested connection to %x@%s",
		peerAddr.IdentityKey.SerializeCompressed(), peerAddr.Address)

	// By default, we will use the global connection timeout value.
	timeout := r.cfg.ConnectionTimeout

	// Check if the connection timeout is set. If set, we will use it in our
	// request.
	if in.Timeout != 0 {
		timeout = time.Duration(in.Timeout) * time.Second
		log.Debugf(
			"[connectpeer] connection timeout is set to %v",
			timeout,
		)
	}

	if err := r.server.ConnectToPeer(peerAddr,
		in.Perm, timeout); err != nil {

		log.Errorf(
			"[connectpeer]: error connecting to peer: %v", err,
		)
		return nil, err
	}

	log.Debugf("Connected to peer: %v", peerAddr.String())
	return nil, nil
}

// DisconnectPeer attempts to disconnect one peer from another identified by a
// given pubKey. In the case that we currently have a pending or active channel
// with the target peer, this action will be disallowed.
func (r *LightningRPCServer) DisconnectPeer(ctx context.Context,
	in *rpc_pb.DisconnectPeerRequest) (*rpc_pb.Null, er.R) {

	log.Debugf("[disconnectpeer] from peer(%s)", in.PubKey)

	if !r.server.Started() {
		return nil, ErrServerNotActive.Default()
	}

	// First we'll validate the string passed in within the request to
	// ensure that it's a valid hex-string, and also a valid compressed
	// public key.
	pubKeyBytes, err := util.DecodeHex(string(in.PubKey))
	if err != nil {
		return nil, er.Errorf("unable to decode pubkey bytes: %v", err)
	}
	peerPubKey, err := btcec.ParsePubKey(pubKeyBytes, btcec.S256())
	if err != nil {
		return nil, er.Errorf("unable to parse pubkey: %v", err)
	}

	// Next, we'll fetch the pending/active channels we have with a
	// particular peer.
	nodeChannels, err := r.server.remoteChanDB.FetchOpenChannels(peerPubKey)
	if err != nil {
		return nil, er.Errorf("unable to fetch channels for peer: %v", err)
	}

	// In order to avoid erroneously disconnecting from a peer that we have
	// an active channel with, if we have any channels active with this
	// peer, then we'll disallow disconnecting from them.
	if len(nodeChannels) > 0 && !r.cfg.UnsafeDisconnect {
		return nil, er.Errorf("cannot disconnect from peer(%x), "+
			"all active channels with the peer need to be closed "+
			"first", pubKeyBytes)
	}

	// With all initial validation complete, we'll now request that the
	// server disconnects from the peer.
	if err := r.server.DisconnectPeer(peerPubKey); err != nil {
		return nil, er.Errorf("unable to disconnect peer: %v", err)
	}

	return nil, nil
}

// newFundingShimAssembler returns a new fully populated
// chanfunding.CannedAssembler using a FundingShim obtained from an RPC caller.
func newFundingShimAssembler(chanPointShim *rpc_pb.ChanPointShim, initiator bool,
	keyRing keychain.KeyRing) (chanfunding.Assembler, er.R) {

	// Perform some basic sanity checks to ensure that all the expected
	// fields are populated.
	switch {
	case chanPointShim.RemoteKey == nil:
		return nil, er.Errorf("remote key not set")

	case chanPointShim.LocalKey == nil:
		return nil, er.Errorf("local key desc not set")

	case chanPointShim.LocalKey.RawKeyBytes == nil:
		return nil, er.Errorf("local raw key bytes not set")

	case chanPointShim.LocalKey.KeyLoc == nil:
		return nil, er.Errorf("local key loc not set")

	case chanPointShim.ChanPoint == nil:
		return nil, er.Errorf("chan point not set")

	case len(chanPointShim.PendingChanId) != 32:
		return nil, er.Errorf("pending chan ID not set")
	}

	// First, we'll map the RPC's channel point to one we can actually use.
	index := chanPointShim.ChanPoint.OutputIndex
	txid, err := GetChanPointFundingTxid(chanPointShim.ChanPoint)
	if err != nil {
		return nil, err
	}
	chanPoint := wire.NewOutPoint(txid, index)

	// Next we'll parse out the remote party's funding key, as well as our
	// full key descriptor.
	remoteKey, err := btcec.ParsePubKey(
		chanPointShim.RemoteKey, btcec.S256(),
	)
	if err != nil {
		return nil, err
	}

	shimKeyDesc := chanPointShim.LocalKey
	localKey, err := btcec.ParsePubKey(
		shimKeyDesc.RawKeyBytes, btcec.S256(),
	)
	if err != nil {
		return nil, err
	}
	localKeyDesc := keychain.KeyDescriptor{
		PubKey: localKey,
		KeyLocator: keychain.KeyLocator{
			Family: keychain.KeyFamily(
				shimKeyDesc.KeyLoc.KeyFamily,
			),
			Index: uint32(shimKeyDesc.KeyLoc.KeyIndex),
		},
	}

	// Verify that if we re-derive this key according to the passed
	// KeyLocator, that we get the exact same key back. Otherwise, we may
	// end up in a situation where we aren't able to actually sign for this
	// newly created channel.
	derivedKey, err := keyRing.DeriveKey(localKeyDesc.KeyLocator)
	if err != nil {
		return nil, err
	}
	if !derivedKey.PubKey.IsEqual(localKey) {
		return nil, er.Errorf("KeyLocator does not match attached " +
			"raw pubkey")
	}

	// With all the parts assembled, we can now make the canned assembler
	// to pass into the wallet.
	return chanfunding.NewCannedAssembler(
		chanPointShim.ThawHeight, *chanPoint,
		btcutil.Amount(chanPointShim.Amt), &localKeyDesc,
		remoteKey, initiator,
	), nil
}

// newFundingShimAssembler returns a new fully populated
// chanfunding.PsbtAssembler using a FundingShim obtained from an RPC caller.
func newPsbtAssembler(req *rpc_pb.OpenChannelRequest, normalizedMinConfs int32,
	psbtShim *rpc_pb.PsbtShim, netParams *chaincfg.Params) (
	chanfunding.Assembler, er.R) {

	var (
		packet *psbt.Packet
		err    er.R
	)

	// Perform some basic sanity checks to ensure that all the expected
	// fields are populated and none of the incompatible fields are.
	if len(psbtShim.PendingChanId) != 32 {
		return nil, er.Errorf("pending chan ID not set")
	}
	if normalizedMinConfs != 1 {
		return nil, er.Errorf("setting non-default values for " +
			"minimum confirmation is not supported for PSBT " +
			"funding")
	}
	if req.SatPerByte != 0 || req.TargetConf != 0 {
		return nil, er.Errorf("specifying fee estimation parameters " +
			"is not supported for PSBT funding")
	}

	// The base PSBT is optional. But if it's set, it has to be a valid,
	// binary serialized PSBT.
	if len(psbtShim.BasePsbt) > 0 {
		packet, err = psbt.NewFromRawBytes(
			bytes.NewReader(psbtShim.BasePsbt), false,
		)
		if err != nil {
			return nil, er.Errorf("error parsing base PSBT: %v",
				err)
		}
	}

	// With all the parts assembled, we can now make the canned assembler
	// to pass into the wallet.
	return chanfunding.NewPsbtAssembler(
		btcutil.Amount(req.LocalFundingAmount), packet, netParams,
		!psbtShim.NoPublish,
	), nil
}

// canOpenChannel returns an error if the necessary subsystems for channel
// funding are not ready.
func (r *LightningRPCServer) canOpenChannel() er.R {
	// We can't open a channel until the main server has started.
	if !r.server.Started() {
		return ErrServerNotActive.Default()
	}

	// Creation of channels before the wallet syncs up is currently
	// disallowed.
	isSynced, _, err := r.server.cc.Wallet.IsSynced()
	if err != nil {
		return err
	}
	if !isSynced {
		return er.New("channels cannot be created before the " +
			"wallet is fully synced")
	}

	return nil
}

// praseOpenChannelReq parses an OpenChannelRequest message into the server's
// native openChanReq struct. The logic is abstracted so that it can be shared
// between OpenChannel and OpenChannelSync.
func (r *LightningRPCServer) parseOpenChannelReq(in *rpc_pb.OpenChannelRequest,
	isSync bool) (*openChanReq, er.R) {

	log.Debugf("[openchannel] request to NodeKey(%x) "+
		"allocation(us=%v, them=%v)", in.NodePubkey,
		in.LocalFundingAmount, in.PushSat)

	localFundingAmt := btcutil.Amount(in.LocalFundingAmount)
	remoteInitialBalance := btcutil.Amount(in.PushSat)
	minHtlcIn := lnwire.MilliSatoshi(in.MinHtlcMsat)
	remoteCsvDelay := uint16(in.RemoteCsvDelay)
	maxValue := lnwire.MilliSatoshi(in.RemoteMaxValueInFlightMsat)
	maxHtlcs := uint16(in.RemoteMaxHtlcs)

	globalFeatureSet := r.server.featureMgr.Get(feature.SetNodeAnn)

	// Ensure that the initial balance of the remote party (if pushing
	// satoshis) does not exceed the amount the local party has requested
	// for funding.
	//
	// TODO(roasbeef): incorporate base fee?
	if remoteInitialBalance >= localFundingAmt {
		return nil, er.Errorf("amount pushed to remote peer for " +
			"initial state must be below the local funding amount")
	}

	// Ensure that the user doesn't exceed the current soft-limit for
	// channel size. If the funding amount is above the soft-limit, then
	// we'll reject the request.
	wumboEnabled := globalFeatureSet.HasFeature(
		lnwire.WumboChannelsOptional,
	)
	if !wumboEnabled && localFundingAmt > MaxFundingAmount {
		return nil, er.Errorf("funding amount is too large, the max "+
			"channel size is: %v", MaxFundingAmount)
	}

	// Restrict the size of the channel we'll actually open. At a later
	// level, we'll ensure that the output we create after accounting for
	// fees that a dust output isn't created.
	if localFundingAmt < minChanFundingSize {
		return nil, er.Errorf("channel is too small, the minimum "+
			"channel size is: %v SAT", int64(minChanFundingSize))
	}

	// Prevent users from submitting a max-htlc value that would exceed the
	// protocol maximum.
	if maxHtlcs > input.MaxHTLCNumber/2 {
		return nil, er.Errorf("remote-max-htlcs (%v) cannot be "+
			"greater than %v", maxHtlcs, input.MaxHTLCNumber/2)
	}

	// Then, we'll extract the minimum number of confirmations that each
	// output we use to fund the channel's funding transaction should
	// satisfy.
	minConfs, err := lnrpc.ExtractMinConfs(in.MinConfs, in.SpendUnconfirmed)
	if err != nil {
		return nil, err
	}

	// TODO(roasbeef): also return channel ID?

	var nodePubKey *btcec.PublicKey

	// Parse the remote pubkey the NodePubkey field of the request. If it's
	// not present, we'll fallback to the deprecated version that parses the
	// key from a hex string if this is for REST for backwards compatibility.
	switch {

	// Parse the raw bytes of the node key into a pubkey object so we can
	// easily manipulate it.
	case len(in.NodePubkey) > 0:
		nodePubKey, err = btcec.ParsePubKey(in.NodePubkey, btcec.S256())
		if err != nil {
			return nil, err
		}

	// Decode the provided target node's public key, parsing it into a pub
	// key object. For all sync call, byte slices are expected to be encoded
	// as hex strings.
	case isSync:
		keyBytes, err := util.DecodeHex(string(in.NodePubkey))
		if err != nil {
			return nil, err
		}

		nodePubKey, err = btcec.ParsePubKey(keyBytes, btcec.S256())
		if err != nil {
			return nil, err
		}

	default:
		return nil, er.Errorf("NodePubkey is not set")
	}

	// Making a channel to ourselves wouldn't be of any use, so we
	// explicitly disallow them.
	if nodePubKey.IsEqual(r.server.identityECDH.PubKey()) {
		return nil, er.Errorf("cannot open channel to self")
	}

	// Based on the passed fee related parameters, we'll determine an
	// appropriate fee rate for the funding transaction.
	satPerKw := chainfee.SatPerKVByte(in.SatPerByte * 1000).FeePerKWeight()
	feeRate, err := sweep.DetermineFeePerKw(
		r.server.cc.FeeEstimator, sweep.FeePreference{
			ConfTarget: uint32(in.TargetConf),
			FeeRate:    satPerKw,
		},
	)
	if err != nil {
		return nil, err
	}

	log.Debugf("[openchannel]: using fee of %v sat/kw for funding tx",
		int64(feeRate))

	script, err := chancloser.ParseUpfrontShutdownAddress(
		in.CloseAddress, r.cfg.ActiveNetParams.Params,
	)
	if err != nil {
		return nil, er.Errorf("error parsing upfront shutdown: %v",
			err)
	}

	// Instruct the server to trigger the necessary events to attempt to
	// open a new channel. A stream is returned in place, this stream will
	// be used to consume updates of the state of the pending channel.
	return &openChanReq{
		targetPubkey:     nodePubKey,
		chainHash:        *r.cfg.ActiveNetParams.GenesisHash,
		localFundingAmt:  localFundingAmt,
		pushAmt:          lnwire.NewMSatFromSatoshis(remoteInitialBalance),
		minHtlcIn:        minHtlcIn,
		fundingFeePerKw:  feeRate,
		private:          in.Private,
		remoteCsvDelay:   remoteCsvDelay,
		minConfs:         minConfs,
		shutdownScript:   script,
		maxValueInFlight: maxValue,
		maxHtlcs:         maxHtlcs,
		maxLocalCsv:      uint16(in.MaxLocalCsv),
	}, nil
}

// OpenChannel attempts to open a singly funded channel specified in the
// request to a remote peer.
func (r *LightningRPCServer) OpenChannel(in *rpc_pb.OpenChannelRequest,
	updateStream rpc_pb.Lightning_OpenChannelServer) error {

	if err := r.canOpenChannel(); err != nil {
		return er.Native(err)
	}

	req, err := r.parseOpenChannelReq(in, false)
	if err != nil {
		return er.Native(err)
	}

	// If the user has provided a shim, then we'll now augment the based
	// open channel request with this additional logic.
	if in.FundingShim != nil {
		switch {
		// If we have a chan point shim, then this means the funding
		// transaction was crafted externally. In this case we only
		// need to hand a channel point down into the wallet.
		case in.FundingShim.GetChanPointShim() != nil:
			chanPointShim := in.FundingShim.GetChanPointShim()

			// Map the channel point shim into a new
			// chanfunding.CannedAssembler that the wallet will use
			// to obtain the channel point details.
			copy(req.pendingChanID[:], chanPointShim.PendingChanId)
			req.chanFunder, err = newFundingShimAssembler(
				chanPointShim, true, r.server.cc.KeyRing,
			)
			if err != nil {
				return er.Native(err)
			}

		// If we have a PSBT shim, then this means the funding
		// transaction will be crafted outside of the wallet, once the
		// funding multisig output script is known. We'll create an
		// intent that will supervise the multi-step process.
		case in.FundingShim.GetPsbtShim() != nil:
			psbtShim := in.FundingShim.GetPsbtShim()

			// Instruct the wallet to use the new
			// chanfunding.PsbtAssembler to construct the funding
			// transaction.
			copy(req.pendingChanID[:], psbtShim.PendingChanId)
			req.chanFunder, err = newPsbtAssembler(
				in, req.minConfs, psbtShim,
				&r.server.cc.Wallet.Cfg.NetParams,
			)
			if err != nil {
				return er.Native(err)
			}
		}
	}

	updateChan, errChan := r.server.OpenChannel(req)

	var outpoint wire.OutPoint
out:
	for {
		select {
		case err := <-errChan:
			log.Errorf("unable to open channel to NodeKey(%x): %v",
				req.targetPubkey.SerializeCompressed(), err)
			return er.Native(err)
		case fundingUpdate := <-updateChan:
			log.Tracef("[openchannel] sending update: %v",
				fundingUpdate)
			if err := updateStream.Send(fundingUpdate); err != nil {
				return er.Native(er.E(err))
			}

			// If a final channel open update is being sent, then
			// we can break out of our recv loop as we no longer
			// need to process any further updates.
			update, ok := fundingUpdate.Update.(*rpc_pb.OpenStatusUpdate_ChanOpen)
			if ok {
				chanPoint := update.ChanOpen.ChannelPoint
				txid, err := GetChanPointFundingTxid(chanPoint)
				if err != nil {
					return er.Native(err)
				}
				outpoint = wire.OutPoint{
					Hash:  *txid,
					Index: chanPoint.OutputIndex,
				}

				break out
			}
		case <-r.quit:
			return nil
		}
	}

	log.Tracef("[openchannel] success NodeKey(%x), ChannelPoint(%v)",
		req.targetPubkey.SerializeCompressed(), outpoint)
	return nil
}

// OpenChannelSync is a synchronous version of the OpenChannel RPC call. This
// call is meant to be consumed by clients to the REST proxy. As with all other
// sync calls, all byte slices are instead to be populated as hex encoded
// strings.
func (r *LightningRPCServer) OpenChannelSync(ctx context.Context,
	in *rpc_pb.OpenChannelRequest) (*rpc_pb.ChannelPoint, er.R) {

	if err := r.canOpenChannel(); err != nil {
		return nil, err
	}

	req, err := r.parseOpenChannelReq(in, true)
	if err != nil {
		return nil, err
	}

	updateChan, errChan := r.server.OpenChannel(req)
	select {
	// If an error occurs them immediately return the error to the client.
	case err := <-errChan:
		log.Errorf("unable to open channel to NodeKey(%x): %v",
			req.targetPubkey.SerializeCompressed(), err)
		return nil, err

	// Otherwise, wait for the first channel update. The first update sent
	// is when the funding transaction is broadcast to the network.
	case fundingUpdate := <-updateChan:
		log.Tracef("[openchannel] sending update: %v",
			fundingUpdate)

		// Parse out the txid of the pending funding transaction. The
		// sync client can use this to poll against the list of
		// PendingChannels.
		openUpdate := fundingUpdate.Update.(*rpc_pb.OpenStatusUpdate_ChanPending)
		chanUpdate := openUpdate.ChanPending

		return &rpc_pb.ChannelPoint{
			FundingTxid: &rpc_pb.ChannelPoint_FundingTxidBytes{
				FundingTxidBytes: chanUpdate.Txid,
			},
			OutputIndex: chanUpdate.OutputIndex,
		}, nil
	case <-r.quit:
		return nil, nil
	}
}

// GetChanPointFundingTxid returns the given channel point's funding txid in
// raw bytes.
func GetChanPointFundingTxid(chanPoint *rpc_pb.ChannelPoint) (*chainhash.Hash, er.R) {
	var txid []byte

	// A channel point's funding txid can be get/set as a byte slice or a
	// string. In the case it is a string, decode it.
	switch chanPoint.GetFundingTxid().(type) {
	case *rpc_pb.ChannelPoint_FundingTxidBytes:
		txid = chanPoint.GetFundingTxidBytes()
	case *rpc_pb.ChannelPoint_FundingTxidStr:
		s := chanPoint.GetFundingTxidStr()
		h, err := chainhash.NewHashFromStr(s)
		if err != nil {
			return nil, err
		}

		txid = h[:]
	}

	return chainhash.NewHash(txid)
}

// CloseChannel attempts to close an active channel identified by its channel
// point. The actions of this method can additionally be augmented to attempt
// a force close after a timeout period in the case of an inactive peer.
func (r *LightningRPCServer) CloseChannel(in *rpc_pb.CloseChannelRequest,
	updateStream rpc_pb.Lightning_CloseChannelServer) er.R {

	if !r.server.Started() {
		return ErrServerNotActive.Default()
	}

	// If the user didn't specify a channel point, then we'll reject this
	// request all together.
	if in.GetChannelPoint() == nil {
		return er.Errorf("must specify channel point in close channel")
	}

	// If force closing a channel, the fee set in the commitment transaction
	// is used.
	if in.Force && (in.SatPerByte != 0 || in.TargetConf != 0) {
		return er.Errorf("force closing a channel uses a pre-defined fee")
	}

	force := in.Force
	index := in.ChannelPoint.OutputIndex
	txid, err := GetChanPointFundingTxid(in.GetChannelPoint())
	if err != nil {
		log.Errorf("[closechannel] unable to get funding txid: %v", err)
		return err
	}
	chanPoint := wire.NewOutPoint(txid, index)

	log.Tracef("[closechannel] request for ChannelPoint(%v), force=%v",
		chanPoint, force)

	var (
		updateChan chan interface{}
		errChan    chan er.R
	)

	// TODO(roasbeef): if force and peer online then don't force?

	// First, we'll fetch the channel as is, as we'll need to examine it
	// regardless of if this is a force close or not.
	channel, err := r.server.remoteChanDB.FetchChannel(*chanPoint)
	if err != nil {
		return err
	}

	// We can't coop or force close restored channels or channels that have
	// experienced local data loss. Normally we would detect this in the
	// channel arbitrator if the channel has the status
	// ChanStatusLocalDataLoss after connecting to its peer. But if no
	// connection can be established, the channel arbitrator doesn't know it
	// can't be force closed yet.
	if channel.HasChanStatus(channeldb.ChanStatusRestored) ||
		channel.HasChanStatus(channeldb.ChanStatusLocalDataLoss) {

		return er.Errorf("cannot close channel with state: %v",
			channel.ChanStatus())
	}

	// Retrieve the best height of the chain, which we'll use to complete
	// either closing flow.
	bs, err := r.server.cc.ChainIO.BestBlock()
	if err != nil {
		return err
	}

	// If a force closure was requested, then we'll handle all the details
	// around the creation and broadcast of the unilateral closure
	// transaction here rather than going to the switch as we don't require
	// interaction from the peer.
	if force {

		// As we're force closing this channel, as a precaution, we'll
		// ensure that the switch doesn't continue to see this channel
		// as eligible for forwarding HTLC's. If the peer is online,
		// then we'll also purge all of its indexes.
		remotePub := channel.IdentityPub
		if peer, err := r.server.FindPeer(remotePub); err == nil {
			// TODO(roasbeef): actually get the active channel
			// instead too?
			//  * so only need to grab from database
			peer.WipeChannel(&channel.FundingOutpoint)
		} else {
			chanID := lnwire.NewChanIDFromOutPoint(&channel.FundingOutpoint)
			r.server.htlcSwitch.RemoveLink(chanID)
		}

		// With the necessary indexes cleaned up, we'll now force close
		// the channel.
		chainArbitrator := r.server.chainArb
		closingTx, err := chainArbitrator.ForceCloseContract(
			*chanPoint,
		)
		if err != nil {
			log.Errorf("unable to force close transaction: %v", err)
			return err
		}

		closingTxid := closingTx.TxHash()

		// With the transaction broadcast, we send our first update to
		// the client.
		updateChan = make(chan interface{}, 2)
		updateChan <- &peer.PendingUpdate{
			Txid: closingTxid[:],
		}

		errChan = make(chan er.R, 1)
		notifier := r.server.cc.ChainNotifier
		go peer.WaitForChanToClose(uint32(bs.Height), notifier, errChan, chanPoint,
			&closingTxid, closingTx.TxOut[0].PkScript, func() {
				// Respond to the local subsystem which
				// requested the channel closure.
				updateChan <- &peer.ChannelCloseUpdate{
					ClosingTxid: closingTxid[:],
					Success:     true,
				}
			})
	} else {
		// If this is a frozen channel, then we only allow the co-op
		// close to proceed if we were the responder to this channel if
		// the absolute thaw height has not been met.
		if channel.IsInitiator {
			absoluteThawHeight, err := channel.AbsoluteThawHeight()
			if err != nil {
				return err
			}
			if uint32(bs.Height) < absoluteThawHeight {
				return er.Errorf("cannot co-op close frozen "+
					"channel as initiator until height=%v, "+
					"(current_height=%v)",
					absoluteThawHeight, bs.Height)
			}
		}

		// If the link is not known by the switch, we cannot gracefully close
		// the channel.
		channelID := lnwire.NewChanIDFromOutPoint(chanPoint)
		if _, err := r.server.htlcSwitch.GetLink(channelID); err != nil {
			log.Debugf("Trying to non-force close offline channel with "+
				"chan_point=%v", chanPoint)
			return er.Errorf("unable to gracefully close channel while peer "+
				"is offline (try force closing it instead): %v", err)
		}

		// Based on the passed fee related parameters, we'll determine
		// an appropriate fee rate for the cooperative closure
		// transaction.
		satPerKw := chainfee.SatPerKVByte(
			in.SatPerByte * 1000,
		).FeePerKWeight()
		feeRate, err := sweep.DetermineFeePerKw(
			r.server.cc.FeeEstimator, sweep.FeePreference{
				ConfTarget: uint32(in.TargetConf),
				FeeRate:    satPerKw,
			},
		)
		if err != nil {
			return err
		}

		log.Debugf("Target sat/kw for closing transaction: %v",
			int64(feeRate))

		// Before we attempt the cooperative channel closure, we'll
		// examine the channel to ensure that it doesn't have a
		// lingering HTLC.
		if len(channel.ActiveHtlcs()) != 0 {
			return er.Errorf("cannot co-op close channel " +
				"with active htlcs")
		}

		// Otherwise, the caller has requested a regular interactive
		// cooperative channel closure. So we'll forward the request to
		// the htlc switch which will handle the negotiation and
		// broadcast details.

		var deliveryScript lnwire.DeliveryAddress

		// If a delivery address to close out to was specified, decode it.
		if len(in.DeliveryAddress) > 0 {
			// Decode the address provided.
			addr, err := btcutil.DecodeAddress(
				in.DeliveryAddress, r.cfg.ActiveNetParams.Params,
			)
			if err != nil {
				return er.Errorf("invalid delivery address: %v", err)
			}

			// Create a script to pay out to the address provided.
			deliveryScript, err = txscript.PayToAddrScript(addr)
			if err != nil {
				return err
			}
		}

		updateChan, errChan = r.server.htlcSwitch.CloseLink(
			chanPoint, htlcswitch.CloseRegular, feeRate, deliveryScript,
		)
	}
out:
	for {
		select {
		case err := <-errChan:
			log.Errorf("[closechannel] unable to close "+
				"ChannelPoint(%v): %v", chanPoint, err)
			return err
		case closingUpdate := <-updateChan:
			rpcClosingUpdate, err := createRPCCloseUpdate(
				closingUpdate,
			)
			if err != nil {
				return err
			}

			log.Tracef("[closechannel] sending update: %v",
				rpcClosingUpdate)

			if err := updateStream.Send(rpcClosingUpdate); err != nil {
				return er.E(err)
			}

			// If a final channel closing updates is being sent,
			// then we can break out of our dispatch loop as we no
			// longer need to process any further updates.
			switch closeUpdate := closingUpdate.(type) {
			case *peer.ChannelCloseUpdate:
				h, _ := chainhash.NewHash(closeUpdate.ClosingTxid)
				log.Infof("[closechannel] close completed: "+
					"txid(%v)", h)
				break out
			}
		case <-r.quit:
			return nil
		}
	}

	return nil
}

func createRPCCloseUpdate(update interface{}) (
	*rpc_pb.CloseStatusUpdate, er.R) {

	switch u := update.(type) {
	case *peer.ChannelCloseUpdate:
		return &rpc_pb.CloseStatusUpdate{
			Update: &rpc_pb.CloseStatusUpdate_ChanClose{
				ChanClose: &rpc_pb.ChannelCloseUpdate{
					ClosingTxid: u.ClosingTxid,
				},
			},
		}, nil
	case *peer.PendingUpdate:
		return &rpc_pb.CloseStatusUpdate{
			Update: &rpc_pb.CloseStatusUpdate_ClosePending{
				ClosePending: &rpc_pb.PendingUpdate{
					Txid:        u.Txid,
					OutputIndex: u.OutputIndex,
				},
			},
		}, nil
	}

	return nil, er.New("unknown close status update")
}

// abandonChanFromGraph attempts to remove a channel from the channel graph. If
// we can't find the chanID in the graph, then we assume it has already been
// removed, and will return a nop.
func abandonChanFromGraph(chanGraph *channeldb.ChannelGraph,
	chanPoint *wire.OutPoint) er.R {

	// First, we'll obtain the channel ID. If we can't locate this, then
	// it's the case that the channel may have already been removed from
	// the graph, so we'll return a nil error.
	chanID, err := chanGraph.ChannelID(chanPoint)
	switch {
	case channeldb.ErrEdgeNotFound.Is(err):
		return nil
	case err != nil:
		return err
	}

	// If the channel ID is still in the graph, then that means the channel
	// is still open, so we'll now move to purge it from the graph.
	return chanGraph.DeleteChannelEdges(chanID)
}

// AbandonChannel removes all channel state from the database except for a
// close summary. This method can be used to get rid of permanently unusable
// channels due to bugs fixed in newer versions of lnd.
func (r *LightningRPCServer) AbandonChannel(in *rpc_pb.AbandonChannelRequest) (
	*rpc_pb.AbandonChannelResponse,
	er.R,
) {
	// We'll parse out the arguments to we can obtain the chanPoint of the
	// target channel.
	txid, err := GetChanPointFundingTxid(in.GetChannelPoint())
	if err != nil {
		return nil, err
	}
	index := in.ChannelPoint.OutputIndex
	chanPoint := wire.NewOutPoint(txid, index)

	// When we remove the channel from the database, we need to set a close
	// height, so we'll just use the current best known height.
	bs, err := r.server.cc.ChainIO.BestBlock()
	if err != nil {
		return nil, err
	}

	dbChan, err := r.server.remoteChanDB.FetchChannel(*chanPoint)
	switch {
	// If the channel isn't found in the set of open channels, then we can
	// continue on as it can't be loaded into the link/peer.
	case channeldb.ErrChannelNotFound.Is(err):
		break

	// If the channel is still known to be open, then before we modify any
	// on-disk state, we'll remove the channel from the switch and peer
	// state if it's been loaded in.
	case err == nil:
		// If the user requested the more safe version that only allows
		// the removal of externally (shim) funded channels that are
		// still pending, we enforce this option now that we know the
		// state of the channel.
		//
		// TODO(guggero): Properly store the funding type (wallet, shim,
		// PSBT) on the channel so we don't need to use the thaw height.
		isShimFunded := dbChan.ThawHeight > 0
		isPendingShimFunded := isShimFunded && dbChan.IsPending
		if in.PendingFundingShimOnly && !isPendingShimFunded {
			return nil, er.Errorf("channel %v is not externally "+
				"funded or not pending", chanPoint)
		}

		// We'll mark the channel as borked before we remove the state
		// from the switch/peer so it won't be loaded back in if the
		// peer reconnects.
		if err := dbChan.MarkBorked(); err != nil {
			return nil, err
		}
		remotePub := dbChan.IdentityPub
		if peer, err := r.server.FindPeer(remotePub); err == nil {
			peer.WipeChannel(chanPoint)
		}

	default:
		return nil, err
	}

	// Abandoning a channel is a three step process: remove from the open
	// channel state, remove from the graph, remove from the contract
	// court. Between any step it's possible that the users restarts the
	// process all over again. As a result, each of the steps below are
	// intended to be idempotent.
	err = r.server.remoteChanDB.AbandonChannel(chanPoint, uint32(bs.Height))
	if err != nil {
		return nil, err
	}
	err = abandonChanFromGraph(
		r.server.localChanDB.ChannelGraph(), chanPoint,
	)
	if err != nil {
		return nil, err
	}
	err = r.server.chainArb.ResolveContract(*chanPoint)
	if err != nil {
		return nil, err
	}

	// If this channel was in the process of being closed, but didn't fully
	// close, then it's possible that the nursery is hanging on to some
	// state. To err on the side of caution, we'll now attempt to wipe any
	// state for this channel from the nursery.
	err = r.server.utxoNursery.cfg.Store.RemoveChannel(chanPoint)
	if err != nil && !ErrContractNotFound.Is(err) {
		return nil, err
	}

	// Finally, notify the backup listeners that the channel can be removed
	// from any channel backups.
	r.server.channelNotifier.NotifyClosedChannelEvent(*chanPoint)

	return &rpc_pb.AbandonChannelResponse{}, nil
}

func (r *LightningRPCServer) GetInfo(ctx context.Context,
	in *rpc_pb.GetInfoRequest) (*rpc_pb.GetInfoResponse, error) {
	res, err := r.GetInfo0(ctx, in)
	return res, er.Native(err)
}

// GetInfo returns general information concerning the lightning node including
// its identity pubkey, alias, the chains it is connected to, and information
// concerning the number of open+pending channels.
func (r *LightningRPCServer) GetInfo0(ctx context.Context,
	in *rpc_pb.GetInfoRequest) (*rpc_pb.GetInfoResponse, er.R) {

	serverPeers := r.server.Peers()

	openChannels, err := r.server.remoteChanDB.FetchAllOpenChannels()
	if err != nil {
		return nil, err
	}

	var activeChannels uint32
	for _, channel := range openChannels {
		chanID := lnwire.NewChanIDFromOutPoint(&channel.FundingOutpoint)
		if r.server.htlcSwitch.HasActiveLink(chanID) {
			activeChannels++
		}
	}

	inactiveChannels := uint32(len(openChannels)) - activeChannels

	pendingChannels, err := r.server.remoteChanDB.FetchPendingChannels()
	if err != nil {
		return nil, er.Errorf("unable to get retrieve pending "+
			"channels: %v", err)
	}
	nPendingChannels := uint32(len(pendingChannels))

	idPub := r.server.identityECDH.PubKey().SerializeCompressed()
	idPubHex := hex.EncodeToString(idPub)

	bs, err := r.server.cc.ChainIO.BestBlock()
	if err != nil {
		return nil, er.Errorf("unable to get best block info: %v", err)
	}

	isSynced, bestHeaderTimestamp, err := r.server.cc.Wallet.IsSynced()
	if err != nil {
		return nil, er.Errorf("unable to sync PoV of the wallet "+
			"with current best block in the main chain: %v", err)
	}

	network := lncfg.NormalizeNetwork(r.cfg.ActiveNetParams.Name)
	activeChains := make([]*rpc_pb.Chain, r.cfg.registeredChains.NumActiveChains())
	for i, chain := range r.cfg.registeredChains.ActiveChains() {
		activeChains[i] = &rpc_pb.Chain{
			Chain:   chain.String(),
			Network: network,
		}

	}

	// Check if external IP addresses were provided to lnd and use them
	// to set the URIs.
	nodeAnn, err := r.server.genNodeAnnouncement(false)
	if err != nil {
		return nil, er.Errorf("unable to retrieve current fully signed "+
			"node announcement: %v", err)
	}
	addrs := nodeAnn.Addresses
	uris := make([]string, len(addrs))
	for i, addr := range addrs {
		uris[i] = fmt.Sprintf("%s@%s", idPubHex, addr.String())
	}

	isGraphSynced := r.server.authGossiper.SyncManager().IsGraphSynced()

	features := make(map[uint32]*rpc_pb.Feature)
	sets := r.server.featureMgr.ListSets()

	for _, set := range sets {
		// Get the a list of lnrpc features for each set we support.
		featureVector := r.server.featureMgr.Get(set)
		rpcFeatures := invoicesrpc.CreateRPCFeatures(featureVector)

		// Add the features to our map of features, allowing over writing of
		// existing values because features in different sets with the same bit
		// are duplicated across sets.
		for bit, feature := range rpcFeatures {
			features[bit] = feature
		}
	}

	// TODO(roasbeef): add synced height n stuff
	return &rpc_pb.GetInfoResponse{
		IdentityPubkey:      idPubHex,
		NumPendingChannels:  nPendingChannels,
		NumActiveChannels:   activeChannels,
		NumInactiveChannels: inactiveChannels,
		NumPeers:            uint32(len(serverPeers)),
		BlockHeight:         uint32(bs.Height),
		BlockHash:           bs.Hash.String(),
		SyncedToChain:       isSynced,
		Testnet:             chainreg.IsTestnet(&r.cfg.ActiveNetParams),
		Chains:              activeChains,
		Uris:                uris,
		Alias:               nodeAnn.Alias.String(),
		Color:               routing.EncodeHexColor(nodeAnn.RGBColor),
		BestHeaderTimestamp: int64(bestHeaderTimestamp),
		Version:             version.Version(),
		SyncedToGraph:       isGraphSynced,
		Features:            features,
	}, nil
}

// GetRecoveryInfo returns a boolean indicating whether the wallet is started
// in recovery mode, whether the recovery is finished, and the progress made
// so far.
func (r *LightningRPCServer) GetRecoveryInfo(ctx context.Context,
	in *rpc_pb.GetRecoveryInfoRequest) (*rpc_pb.GetRecoveryInfoResponse, error) {

	isRecoveryMode, progress, err := r.server.cc.Wallet.GetRecoveryInfo()
	if err != nil {
		return nil, er.Native(er.Errorf("unable to get wallet recovery info: %v", err))
	}

	log.Debugf("[getrecoveryinfo] is recovery mode=%v, progress=%v",
		isRecoveryMode, progress)

	return &rpc_pb.GetRecoveryInfoResponse{
		RecoveryMode:     isRecoveryMode,
		RecoveryFinished: progress == 1,
		Progress:         progress,
	}, nil
}

// ListPeers returns a verbose listing of all currently active peers.
func (r *LightningRPCServer) ListPeers(ctx context.Context,
	in *rpc_pb.ListPeersRequest) (*rpc_pb.ListPeersResponse, er.R) {

	log.Tracef("[listpeers] request")

	serverPeers := r.server.Peers()
	resp := &rpc_pb.ListPeersResponse{
		Peers: make([]*rpc_pb.Peer, 0, len(serverPeers)),
	}

	for _, serverPeer := range serverPeers {
		var (
			satSent int64
			satRecv int64
		)

		// In order to display the total number of satoshis of outbound
		// (sent) and inbound (recv'd) satoshis that have been
		// transported through this peer, we'll sum up the sent/recv'd
		// values for each of the active channels we have with the
		// peer.
		chans := serverPeer.ChannelSnapshots()
		for _, c := range chans {
			satSent += int64(c.TotalMSatSent.ToSatoshis())
			satRecv += int64(c.TotalMSatReceived.ToSatoshis())
		}

		nodePub := serverPeer.PubKey()

		// Retrieve the peer's sync type. If we don't currently have a
		// syncer for the peer, then we'll default to a passive sync.
		// This can happen if the RPC is called while a peer is
		// initializing.
		syncer, ok := r.server.authGossiper.SyncManager().GossipSyncer(
			nodePub,
		)

		var lnrpcSyncType rpc_pb.Peer_SyncType
		if !ok {
			log.Warnf("Gossip syncer for peer=%x not found",
				nodePub)
			lnrpcSyncType = rpc_pb.Peer_UNKNOWN_SYNC
		} else {
			syncType := syncer.SyncType()
			switch syncType {
			case discovery.ActiveSync:
				lnrpcSyncType = rpc_pb.Peer_ACTIVE_SYNC
			case discovery.PassiveSync:
				lnrpcSyncType = rpc_pb.Peer_PASSIVE_SYNC
			default:
				return nil, er.Errorf("unhandled sync type %v",
					syncType)
			}
		}

		features := invoicesrpc.CreateRPCFeatures(
			serverPeer.RemoteFeatures(),
		)

		rpcPeer := &rpc_pb.Peer{
			PubKey:    nodePub[:],
			Address:   serverPeer.Conn().RemoteAddr().String(),
			Inbound:   serverPeer.Inbound(),
			BytesRecv: serverPeer.BytesReceived(),
			BytesSent: serverPeer.BytesSent(),
			SatSent:   satSent,
			SatRecv:   satRecv,
			PingTime:  serverPeer.PingTime(),
			SyncType:  lnrpcSyncType,
			Features:  features,
		}

		var peerErrors []interface{}

		// If we only want the most recent error, get the most recent
		// error from the buffer and add it to our list of errors if
		// it is non-nil. If we want all the stored errors, simply
		// add the full list to our set of errors.
		if in.LatestError {
			latestErr := serverPeer.ErrorBuffer().Latest()
			if latestErr != nil {
				peerErrors = []interface{}{latestErr}
			}
		} else {
			peerErrors = serverPeer.ErrorBuffer().List()
		}

		// Add the relevant peer errors to our response.
		for _, error := range peerErrors {
			tsError := error.(*peer.TimestampedError)

			rpcErr := &rpc_pb.TimestampedError{
				Timestamp: uint64(tsError.Timestamp.Unix()),
				Error:     tsError.Error.Message(),
			}

			rpcPeer.Errors = append(rpcPeer.Errors, rpcErr)
		}

		// If the server has started, we can query the event store
		// for our peer's flap count. If we do so when the server has
		// not started, the request will block.
		if r.server.Started() {
			vertex, err := route.NewVertexFromBytes(nodePub[:])
			if err != nil {
				return nil, err
			}

			flap, ts, err := r.server.chanEventStore.FlapCount(
				vertex,
			)
			if err != nil {
				return nil, err
			}

			// If our timestamp is non-nil, we have values for our
			// peer's flap count, so we set them.
			if ts != nil {
				rpcPeer.FlapCount = int32(flap)
				rpcPeer.LastFlapNs = ts.UnixNano()
			}
		}

		resp.Peers = append(resp.Peers, rpcPeer)
	}

	log.Debugf("[listpeers] yielded %v peers", serverPeers)

	return resp, nil
}

// SubscribePeerEvents returns a uni-directional stream (server -> client)
// for notifying the client of peer online and offline events.
func (r *LightningRPCServer) SubscribePeerEvents(req *rpc_pb.PeerEventSubscription,
	eventStream rpc_pb.Lightning_SubscribePeerEventsServer) error {

	peerEventSub, err := r.server.peerNotifier.SubscribePeerEvents()
	if err != nil {
		return er.Native(err)
	}
	defer peerEventSub.Cancel()

	for {
		select {
		// A new update has been sent by the peer notifier, we'll
		// marshal it into the form expected by the gRPC client, then
		// send it off to the client.
		case e := <-peerEventSub.Updates():
			var event *rpc_pb.PeerEvent

			switch peerEvent := e.(type) {
			case peernotifier.PeerOfflineEvent:
				event = &rpc_pb.PeerEvent{
					PubKey: peerEvent.PubKey[:],
					Type:   rpc_pb.PeerEvent_PEER_OFFLINE,
				}

			case peernotifier.PeerOnlineEvent:
				event = &rpc_pb.PeerEvent{
					PubKey: peerEvent.PubKey[:],
					Type:   rpc_pb.PeerEvent_PEER_ONLINE,
				}

			default:
				return er.Native(er.Errorf("unexpected peer event: %v", event))
			}

			if err := eventStream.Send(event); err != nil {
				return er.Native(er.E(err))
			}
		case <-r.quit:
			return nil
		}
	}
}

// WalletBalance returns total unspent outputs(confirmed and unconfirmed), all
// confirmed unspent outputs and all unconfirmed unspent outputs under control
// by the wallet. This method can be modified by having the request specify
// only witness outputs should be factored into the final output sum.
// TODO(roasbeef): add async hooks into wallet balance changes
func (r *LightningRPCServer) WalletBalance(ctx context.Context,
	_ *rpc_pb.Null) (*rpc_pb.WalletBalanceResponse, er.R) {

	// Get total balance, from txs that have >= 0 confirmations.
	totalBal, err := r.server.cc.Wallet.ConfirmedBalance(0)
	if err != nil {
		return nil, err
	}

	// Get confirmed balance, from txs that have >= 1 confirmations.
	// TODO(halseth): get both unconfirmed and confirmed balance in one
	// call, as this is racy.
	confirmedBal, err := r.server.cc.Wallet.ConfirmedBalance(1)
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

func (r *LightningRPCServer) GetAddressBalances(
	ctx context.Context,
	in *rpc_pb.GetAddressBalancesRequest,
) (*rpc_pb.GetAddressBalancesResponse, er.R) {
	if be, ok := r.server.cc.Wc.(*btcwallet.BtcWallet); !ok {
		return nil, er.New("GetAddressBalances only possible with BtcWallet")
	} else if adb, err := be.InternalWallet().CalculateAddressBalances(in.Minconf, in.Showzerobalance); err != nil {
		return nil, err
	} else {
		resp := make([]*rpc_pb.GetAddressBalancesResponseAddr, 0, len(adb))
		for k, v := range adb {
			resp = append(resp, &rpc_pb.GetAddressBalancesResponseAddr{
				Address:         k.EncodeAddress(),
				Total:           v.Total.ToBTC(),
				Stotal:          int64(v.Total),
				Spendable:       v.Spendable.ToBTC(),
				Sspendable:      int64(v.Spendable),
				Immaturereward:  v.ImmatureReward.ToBTC(),
				Simmaturereward: int64(v.ImmatureReward),
				Unconfirmed:     v.Unconfirmed.ToBTC(),
				Sunconfirmed:    int64(v.Unconfirmed),
				Outputcount:     v.OutputCount,
			})
		}
		return &rpc_pb.GetAddressBalancesResponse{Addrs: resp}, nil
	}
}

// ChannelBalance returns the total available channel flow across all open
// channels in satoshis.
func (r *LightningRPCServer) ChannelBalance(ctx context.Context,
	in *rpc_pb.Null) (
	*rpc_pb.ChannelBalanceResponse, er.R) {

	var (
		localBalance             lnwire.MilliSatoshi
		remoteBalance            lnwire.MilliSatoshi
		unsettledLocalBalance    lnwire.MilliSatoshi
		unsettledRemoteBalance   lnwire.MilliSatoshi
		pendingOpenLocalBalance  lnwire.MilliSatoshi
		pendingOpenRemoteBalance lnwire.MilliSatoshi
	)

	openChannels, err := r.server.remoteChanDB.FetchAllOpenChannels()
	if err != nil {
		return nil, err
	}

	for _, channel := range openChannels {
		c := channel.LocalCommitment
		localBalance += c.LocalBalance
		remoteBalance += c.RemoteBalance

		// Add pending htlc amount.
		for _, htlc := range c.Htlcs {
			if htlc.Incoming {
				unsettledLocalBalance += htlc.Amt
			} else {
				unsettledRemoteBalance += htlc.Amt
			}
		}
	}

	pendingChannels, err := r.server.remoteChanDB.FetchPendingChannels()
	if err != nil {
		return nil, err
	}

	for _, channel := range pendingChannels {
		c := channel.LocalCommitment
		pendingOpenLocalBalance += c.LocalBalance
		pendingOpenRemoteBalance += c.RemoteBalance
	}

	log.Debugf("[channelbalance] local_balance=%v remote_balance=%v "+
		"unsettled_local_balance=%v unsettled_remote_balance=%v "+
		"pending_open_local_balance=%v pending_open_remove_balance",
		localBalance, remoteBalance, unsettledLocalBalance,
		unsettledRemoteBalance, pendingOpenLocalBalance,
		pendingOpenRemoteBalance)

	return &rpc_pb.ChannelBalanceResponse{
		LocalBalance: &rpc_pb.Amount{
			Sat:  uint64(localBalance.ToSatoshis()),
			Msat: uint64(localBalance),
		},
		RemoteBalance: &rpc_pb.Amount{
			Sat:  uint64(remoteBalance.ToSatoshis()),
			Msat: uint64(remoteBalance),
		},
		UnsettledLocalBalance: &rpc_pb.Amount{
			Sat:  uint64(unsettledLocalBalance.ToSatoshis()),
			Msat: uint64(unsettledLocalBalance),
		},
		UnsettledRemoteBalance: &rpc_pb.Amount{
			Sat:  uint64(unsettledRemoteBalance.ToSatoshis()),
			Msat: uint64(unsettledRemoteBalance),
		},
		PendingOpenLocalBalance: &rpc_pb.Amount{
			Sat:  uint64(pendingOpenLocalBalance.ToSatoshis()),
			Msat: uint64(pendingOpenLocalBalance),
		},
		PendingOpenRemoteBalance: &rpc_pb.Amount{
			Sat:  uint64(pendingOpenRemoteBalance.ToSatoshis()),
			Msat: uint64(pendingOpenRemoteBalance),
		},

		// Deprecated fields.
		Balance:            int64(localBalance.ToSatoshis()),
		PendingOpenBalance: int64(pendingOpenLocalBalance.ToSatoshis()),
	}, nil
}

// PendingChannels returns a list of all the channels that are currently
// considered "pending". A channel is pending if it has finished the funding
// workflow and is waiting for confirmations for the funding txn, or is in the
// process of closure, either initiated cooperatively or non-cooperatively.
func (r *LightningRPCServer) PendingChannels(ctx context.Context,
	_ *rpc_pb.Null) (*rpc_pb.PendingChannelsResponse, er.R) {

	log.Debugf("[pendingchannels]")

	resp := &rpc_pb.PendingChannelsResponse{}

	// rpcInitiator returns the correct lnrpc initiator for channels where
	// we have a record of the opening channel.
	rpcInitiator := func(isInitiator bool) rpc_pb.Initiator {
		if isInitiator {
			return rpc_pb.Initiator_INITIATOR_LOCAL
		}

		return rpc_pb.Initiator_INITIATOR_REMOTE
	}

	// First, we'll populate the response with all the channels that are
	// soon to be opened. We can easily fetch this data from the database
	// and map the db struct to the proto response.
	pendingOpenChannels, err := r.server.remoteChanDB.FetchPendingChannels()
	if err != nil {
		log.Errorf("unable to fetch pending channels: %v", err)
		return nil, err
	}
	resp.PendingOpenChannels = make([]*rpc_pb.PendingChannelsResponse_PendingOpenChannel,
		len(pendingOpenChannels))
	for i, pendingChan := range pendingOpenChannels {
		pub := pendingChan.IdentityPub.SerializeCompressed()

		// As this is required for display purposes, we'll calculate
		// the weight of the commitment transaction. We also add on the
		// estimated weight of the witness to calculate the weight of
		// the transaction if it were to be immediately unilaterally
		// broadcast.
		// TODO(roasbeef): query for funding tx from wallet, display
		// that also?
		localCommitment := pendingChan.LocalCommitment
		utx := btcutil.NewTx(localCommitment.CommitTx)
		commitBaseWeight := blockchain.GetTransactionWeight(utx)
		commitWeight := commitBaseWeight + input.WitnessCommitmentTxWeight

		resp.PendingOpenChannels[i] = &rpc_pb.PendingChannelsResponse_PendingOpenChannel{
			Channel: &rpc_pb.PendingChannelsResponse_PendingChannel{
				RemoteNodePub:        pub,
				ChannelPoint:         pendingChan.FundingOutpoint.String(),
				Capacity:             int64(pendingChan.Capacity),
				LocalBalance:         int64(localCommitment.LocalBalance.ToSatoshis()),
				RemoteBalance:        int64(localCommitment.RemoteBalance.ToSatoshis()),
				LocalChanReserveSat:  int64(pendingChan.LocalChanCfg.ChanReserve),
				RemoteChanReserveSat: int64(pendingChan.RemoteChanCfg.ChanReserve),
				Initiator:            rpcInitiator(pendingChan.IsInitiator),
				CommitmentType:       rpcCommitmentType(pendingChan.ChanType),
			},
			CommitWeight: commitWeight,
			CommitFee:    int64(localCommitment.CommitFee),
			FeePerKw:     int64(localCommitment.FeePerKw),
			// TODO(roasbeef): need to track confirmation height
		}
	}

	bs, err := r.server.cc.ChainIO.BestBlock()
	if err != nil {
		return nil, err
	}

	// Next, we'll examine the channels that are soon to be closed so we
	// can populate these fields within the response.
	pendingCloseChannels, err := r.server.remoteChanDB.FetchClosedChannels(true)
	if err != nil {
		log.Errorf("unable to fetch closed channels: %v", err)
		return nil, err
	}
	for _, pendingClose := range pendingCloseChannels {
		// First construct the channel struct itself, this will be
		// needed regardless of how this channel was closed.
		pub := pendingClose.RemotePub.SerializeCompressed()
		chanPoint := pendingClose.ChanPoint

		// Create the pending channel. If this channel was closed before
		// we started storing historical channel data, we will not know
		// who initiated the channel, so we set the initiator field to
		// unknown.
		channel := &rpc_pb.PendingChannelsResponse_PendingChannel{
			RemoteNodePub:  pub,
			ChannelPoint:   chanPoint.String(),
			Capacity:       int64(pendingClose.Capacity),
			LocalBalance:   int64(pendingClose.SettledBalance),
			CommitmentType: rpc_pb.CommitmentType_UNKNOWN_COMMITMENT_TYPE,
			Initiator:      rpc_pb.Initiator_INITIATOR_UNKNOWN,
		}

		// Lookup the channel in the historical channel bucket to obtain
		// initiator information. If the historical channel bucket was
		// not found, or the channel itself, this channel was closed
		// in a version before we started persisting historical
		// channels, so we silence the error.
		historical, err := r.server.remoteChanDB.FetchHistoricalChannel(
			&pendingClose.ChanPoint,
		)
		switch {
		// If the channel was closed in a version that did not record
		// historical channels, ignore the error.
		case channeldb.ErrNoHistoricalBucket.Is(err):
		case channeldb.ErrChannelNotFound.Is(err):

		case err == nil:
			channel.Initiator = rpcInitiator(historical.IsInitiator)
			channel.CommitmentType = rpcCommitmentType(
				historical.ChanType,
			)

		// If the error is non-nil, and not due to older versions of lnd
		// not persisting historical channels, return it.
		default:
			return nil, err
		}

		closeTXID := pendingClose.ClosingTXID.String()

		switch pendingClose.CloseType {

		// A coop closed channel should never be in the "pending close"
		// state. If a node upgraded from an older lnd version in the
		// middle of a their channel confirming, it will be in this
		// state. We log a warning that the channel will not be included
		// in the now deprecated pending close channels field.
		case channeldb.CooperativeClose:
			log.Warnf("channel %v cooperatively closed and "+
				"in pending close state",
				pendingClose.ChanPoint)

		// If the channel was force closed, then we'll need to query
		// the utxoNursery for additional information.
		// TODO(halseth): distinguish remote and local case?
		case channeldb.LocalForceClose, channeldb.RemoteForceClose:
			forceClose := &rpc_pb.PendingChannelsResponse_ForceClosedChannel{
				Channel:     channel,
				ClosingTxid: closeTXID,
			}

			// Fetch reports from both nursery and resolvers. At the
			// moment this is not an atomic snapshot. This is
			// planned to be resolved when the nursery is removed
			// and channel arbitrator will be the single source for
			// these kind of reports.
			err := r.nurseryPopulateForceCloseResp(
				&chanPoint, bs.Height, forceClose,
			)
			if err != nil {
				return nil, err
			}

			err = r.arbitratorPopulateForceCloseResp(
				&chanPoint, bs.Height, forceClose,
			)
			if err != nil {
				return nil, err
			}

			resp.TotalLimboBalance += int64(forceClose.LimboBalance)

			resp.PendingForceClosingChannels = append(
				resp.PendingForceClosingChannels,
				forceClose,
			)
		}
	}

	// We'll also fetch all channels that are open, but have had their
	// commitment broadcasted, meaning they are waiting for the closing
	// transaction to confirm.
	waitingCloseChans, err := r.server.remoteChanDB.FetchWaitingCloseChannels()
	if err != nil {
		log.Errorf("unable to fetch channels waiting close: %v",
			err)
		return nil, err
	}

	for _, waitingClose := range waitingCloseChans {
		pub := waitingClose.IdentityPub.SerializeCompressed()
		chanPoint := waitingClose.FundingOutpoint

		var commitments rpc_pb.PendingChannelsResponse_Commitments

		// Report local commit. May not be present when DLP is active.
		if waitingClose.LocalCommitment.CommitTx != nil {
			commitments.LocalTxid =
				waitingClose.LocalCommitment.CommitTx.TxHash().
					String()

			commitments.LocalCommitFeeSat = uint64(
				waitingClose.LocalCommitment.CommitFee,
			)
		}

		// Report remote commit. May not be present when DLP is active.
		if waitingClose.RemoteCommitment.CommitTx != nil {
			commitments.RemoteTxid =
				waitingClose.RemoteCommitment.CommitTx.TxHash().
					String()

			commitments.RemoteCommitFeeSat = uint64(
				waitingClose.RemoteCommitment.CommitFee,
			)
		}

		// Report the remote pending commit if any.
		remoteCommitDiff, err := waitingClose.RemoteCommitChainTip()

		switch {

		// Don't set hash if there is no pending remote commit.
		case channeldb.ErrNoPendingCommit.Is(err):

		// An unexpected error occurred.
		case err != nil:
			return nil, err

		// There is a pending remote commit. Set its hash in the
		// response.
		default:
			hash := remoteCommitDiff.Commitment.CommitTx.TxHash()
			commitments.RemotePendingTxid = hash.String()
			commitments.RemoteCommitFeeSat = uint64(
				remoteCommitDiff.Commitment.CommitFee,
			)
		}

		channel := &rpc_pb.PendingChannelsResponse_PendingChannel{
			RemoteNodePub:        pub,
			ChannelPoint:         chanPoint.String(),
			Capacity:             int64(waitingClose.Capacity),
			LocalBalance:         int64(waitingClose.LocalCommitment.LocalBalance.ToSatoshis()),
			RemoteBalance:        int64(waitingClose.LocalCommitment.RemoteBalance.ToSatoshis()),
			LocalChanReserveSat:  int64(waitingClose.LocalChanCfg.ChanReserve),
			RemoteChanReserveSat: int64(waitingClose.RemoteChanCfg.ChanReserve),
			Initiator:            rpcInitiator(waitingClose.IsInitiator),
			CommitmentType:       rpcCommitmentType(waitingClose.ChanType),
		}

		waitingCloseResp := &rpc_pb.PendingChannelsResponse_WaitingCloseChannel{
			Channel:      channel,
			LimboBalance: channel.LocalBalance,
			Commitments:  &commitments,
		}

		// A close tx has been broadcasted, all our balance will be in
		// limbo until it confirms.
		resp.WaitingCloseChannels = append(
			resp.WaitingCloseChannels, waitingCloseResp,
		)

		resp.TotalLimboBalance += channel.LocalBalance
	}

	return resp, nil
}

// arbitratorPopulateForceCloseResp populates the pending channels response
// message with channel resolution information from the contract resolvers.
func (r *LightningRPCServer) arbitratorPopulateForceCloseResp(chanPoint *wire.OutPoint,
	currentHeight int32,
	forceClose *rpc_pb.PendingChannelsResponse_ForceClosedChannel) er.R {

	// Query for contract resolvers state.
	arbitrator, err := r.server.chainArb.GetChannelArbitrator(*chanPoint)
	if err != nil {
		return err
	}
	reports := arbitrator.Report()

	for _, report := range reports {
		switch report.Type {

		// For a direct output, populate/update the top level
		// response properties.
		case contractcourt.ReportOutputUnencumbered:
			// Populate the maturity height fields for the direct
			// commitment output to us.
			forceClose.MaturityHeight = report.MaturityHeight

			// If the transaction has been confirmed, then we can
			// compute how many blocks it has left.
			if forceClose.MaturityHeight != 0 {
				forceClose.BlocksTilMaturity =
					int32(forceClose.MaturityHeight) -
						currentHeight
			}

		// Add htlcs to the PendingHtlcs response property.
		case contractcourt.ReportOutputIncomingHtlc,
			contractcourt.ReportOutputOutgoingHtlc:

			// Don't report details on htlcs that are no longer in
			// limbo.
			if report.LimboBalance == 0 {
				break
			}

			incoming := report.Type == contractcourt.ReportOutputIncomingHtlc
			htlc := &rpc_pb.PendingHTLC{
				Incoming:       incoming,
				Amount:         int64(report.Amount),
				Outpoint:       report.Outpoint.String(),
				MaturityHeight: report.MaturityHeight,
				Stage:          report.Stage,
			}

			if htlc.MaturityHeight != 0 {
				htlc.BlocksTilMaturity =
					int32(htlc.MaturityHeight) - currentHeight
			}

			forceClose.PendingHtlcs = append(forceClose.PendingHtlcs, htlc)

		case contractcourt.ReportOutputAnchor:
			// There are three resolution states for the anchor:
			// limbo, lost and recovered. Derive the current state
			// from the limbo and recovered balances.
			switch {

			case report.RecoveredBalance != 0:
				forceClose.Anchor = rpc_pb.PendingChannelsResponse_ForceClosedChannel_RECOVERED

			case report.LimboBalance != 0:
				forceClose.Anchor = rpc_pb.PendingChannelsResponse_ForceClosedChannel_LIMBO

			default:
				forceClose.Anchor = rpc_pb.PendingChannelsResponse_ForceClosedChannel_LOST
			}

		default:
			return er.Errorf("unknown report output type: %v", report.Type)
		}

		forceClose.LimboBalance += int64(report.LimboBalance)
		forceClose.RecoveredBalance += int64(report.RecoveredBalance)
	}

	return nil
}

// nurseryPopulateForceCloseResp populates the pending channels response
// message with contract resolution information from utxonursery.
func (r *LightningRPCServer) nurseryPopulateForceCloseResp(chanPoint *wire.OutPoint,
	currentHeight int32,
	forceClose *rpc_pb.PendingChannelsResponse_ForceClosedChannel) er.R {

	// Query for the maturity state for this force closed channel. If we
	// didn't have any time-locked outputs, then the nursery may not know of
	// the contract.
	nurseryInfo, err := r.server.utxoNursery.NurseryReport(chanPoint)
	if ErrContractNotFound.Is(err) {
		return nil
	}
	if err != nil {
		return er.Errorf("unable to obtain "+
			"nursery report for ChannelPoint(%v): %v",
			chanPoint, err)
	}

	// If the nursery knows of this channel, then we can populate
	// information detailing exactly how much funds are time locked and also
	// the height in which we can ultimately sweep the funds into the
	// wallet.
	forceClose.LimboBalance = int64(nurseryInfo.limboBalance)
	forceClose.RecoveredBalance = int64(nurseryInfo.recoveredBalance)

	for _, htlcReport := range nurseryInfo.htlcs {
		// TODO(conner) set incoming flag appropriately after handling
		// incoming incubation
		htlc := &rpc_pb.PendingHTLC{
			Incoming:       false,
			Amount:         int64(htlcReport.amount),
			Outpoint:       htlcReport.outpoint.String(),
			MaturityHeight: htlcReport.maturityHeight,
			Stage:          htlcReport.stage,
		}

		if htlc.MaturityHeight != 0 {
			htlc.BlocksTilMaturity =
				int32(htlc.MaturityHeight) -
					currentHeight
		}

		forceClose.PendingHtlcs = append(forceClose.PendingHtlcs,
			htlc)
	}

	return nil
}

// ClosedChannels returns a list of all the channels have been closed.
// This does not include channels that are still in the process of closing.
func (r *LightningRPCServer) ClosedChannels(ctx context.Context,
	in *rpc_pb.ClosedChannelsRequest) (*rpc_pb.ClosedChannelsResponse,
	er.R) {

	// Show all channels when no filter flags are set.
	filterResults := in.Cooperative || in.LocalForce ||
		in.RemoteForce || in.Breach || in.FundingCanceled ||
		in.Abandoned

	resp := &rpc_pb.ClosedChannelsResponse{}

	dbChannels, err := r.server.remoteChanDB.FetchClosedChannels(false)
	if err != nil {
		return nil, err
	}

	// In order to make the response easier to parse for clients, we'll
	// sort the set of closed channels by their closing height before
	// serializing the proto response.
	sort.Slice(dbChannels, func(i, j int) bool {
		return dbChannels[i].CloseHeight < dbChannels[j].CloseHeight
	})

	for _, dbChannel := range dbChannels {
		if dbChannel.IsPending {
			continue
		}

		switch dbChannel.CloseType {
		case channeldb.CooperativeClose:
			if filterResults && !in.Cooperative {
				continue
			}
		case channeldb.LocalForceClose:
			if filterResults && !in.LocalForce {
				continue
			}
		case channeldb.RemoteForceClose:
			if filterResults && !in.RemoteForce {
				continue
			}
		case channeldb.BreachClose:
			if filterResults && !in.Breach {
				continue
			}
		case channeldb.FundingCanceled:
			if filterResults && !in.FundingCanceled {
				continue
			}
		case channeldb.Abandoned:
			if filterResults && !in.Abandoned {
				continue
			}
		}

		channel, err := r.createRPCClosedChannel(dbChannel)
		if err != nil {
			return nil, err
		}

		resp.Channels = append(resp.Channels, channel)
	}

	return resp, nil
}

// ListChannels returns a description of all the open channels that this node
// is a participant in.
func (r *LightningRPCServer) ListChannels(ctx context.Context,
	in *rpc_pb.ListChannelsRequest) (*rpc_pb.ListChannelsResponse, er.R) {

	if in.ActiveOnly && in.InactiveOnly {
		return nil, er.Errorf("either `active_only` or " +
			"`inactive_only` can be set, but not both")
	}

	if in.PublicOnly && in.PrivateOnly {
		return nil, er.Errorf("either `public_only` or " +
			"`private_only` can be set, but not both")
	}

	if len(in.Peer) > 0 && len(in.Peer) != 33 {
		_, err := route.NewVertexFromBytes(in.Peer)
		return nil, er.Errorf("invalid `peer` key: %v", err)
	}

	resp := &rpc_pb.ListChannelsResponse{}

	graph := r.server.localChanDB.ChannelGraph()

	dbChannels, err := r.server.remoteChanDB.FetchAllOpenChannels()
	if err != nil {
		return nil, err
	}

	log.Debugf("[listchannels] fetched %v channels from DB",
		len(dbChannels))

	for _, dbChannel := range dbChannels {
		nodePub := dbChannel.IdentityPub
		nodePubBytes := nodePub.SerializeCompressed()
		chanPoint := dbChannel.FundingOutpoint

		// If the caller requested channels for a target node, skip any
		// that don't match the provided pubkey.
		if len(in.Peer) > 0 && !bytes.Equal(nodePubBytes, in.Peer) {
			continue
		}

		var peerOnline bool
		if _, err := r.server.FindPeer(nodePub); err == nil {
			peerOnline = true
		}

		channelID := lnwire.NewChanIDFromOutPoint(&chanPoint)
		var linkActive bool
		if link, err := r.server.htlcSwitch.GetLink(channelID); err == nil {
			// A channel is only considered active if it is known
			// by the switch *and* able to forward
			// incoming/outgoing payments.
			linkActive = link.EligibleToForward()
		}

		// Next, we'll determine whether we should add this channel to
		// our list depending on the type of channels requested to us.
		isActive := peerOnline && linkActive
		channel, err := createRPCOpenChannel(r, graph, dbChannel, isActive)
		if err != nil {
			return nil, err
		}

		// We'll only skip returning this channel if we were requested
		// for a specific kind and this channel doesn't satisfy it.
		switch {
		case in.ActiveOnly && !isActive:
			continue
		case in.InactiveOnly && isActive:
			continue
		case in.PublicOnly && channel.Private:
			continue
		case in.PrivateOnly && !channel.Private:
			continue
		}

		resp.Channels = append(resp.Channels, channel)
	}

	return resp, nil
}

// rpcCommitmentType takes the channel type and converts it to an rpc commitment
// type value.
func rpcCommitmentType(chanType channeldb.ChannelType) rpc_pb.CommitmentType {
	// Extract the commitment type from the channel type flags. We must
	// first check whether it has anchors, since in that case it would also
	// be tweakless.
	if chanType.HasAnchors() {
		return rpc_pb.CommitmentType_ANCHORS
	}

	if chanType.IsTweakless() {
		return rpc_pb.CommitmentType_STATIC_REMOTE_KEY
	}

	return rpc_pb.CommitmentType_LEGACY
}

// createChannelConstraint creates a *rpc_pb.ChannelConstraints using the
// *Channeldb.ChannelConfig.
func createChannelConstraint(
	chanCfg *channeldb.ChannelConfig) *rpc_pb.ChannelConstraints {

	return &rpc_pb.ChannelConstraints{
		CsvDelay:          uint32(chanCfg.CsvDelay),
		ChanReserveSat:    uint64(chanCfg.ChanReserve),
		DustLimitSat:      uint64(chanCfg.DustLimit),
		MaxPendingAmtMsat: uint64(chanCfg.MaxPendingAmount),
		MinHtlcMsat:       uint64(chanCfg.MinHTLC),
		MaxAcceptedHtlcs:  uint32(chanCfg.MaxAcceptedHtlcs),
	}
}

// createRPCOpenChannel creates an *rpc_pb.Channel from the *channeldb.Channel.
func createRPCOpenChannel(r *LightningRPCServer, graph *channeldb.ChannelGraph,
	dbChannel *channeldb.OpenChannel, isActive bool) (*rpc_pb.Channel, er.R) {

	nodePub := dbChannel.IdentityPub
	nodeID := nodePub.SerializeCompressed()
	chanPoint := dbChannel.FundingOutpoint

	// Next, we'll determine whether the channel is public or not.
	isPublic := dbChannel.ChannelFlags&lnwire.FFAnnounceChannel != 0

	// As this is required for display purposes, we'll calculate
	// the weight of the commitment transaction. We also add on the
	// estimated weight of the witness to calculate the weight of
	// the transaction if it were to be immediately unilaterally
	// broadcast.
	localCommit := dbChannel.LocalCommitment
	utx := btcutil.NewTx(localCommit.CommitTx)
	commitBaseWeight := blockchain.GetTransactionWeight(utx)
	commitWeight := commitBaseWeight + input.WitnessCommitmentTxWeight

	localBalance := localCommit.LocalBalance
	remoteBalance := localCommit.RemoteBalance

	// As an artifact of our usage of mSAT internally, either party
	// may end up in a state where they're holding a fractional
	// amount of satoshis which can't be expressed within the
	// actual commitment output. Since we round down when going
	// from mSAT -> SAT, we may at any point be adding an
	// additional SAT to miners fees. As a result, we display a
	// commitment fee that accounts for this externally.
	var sumOutputs btcutil.Amount
	for _, txOut := range localCommit.CommitTx.TxOut {
		sumOutputs += btcutil.Amount(txOut.Value)
	}
	externalCommitFee := dbChannel.Capacity - sumOutputs

	// Extract the commitment type from the channel type flags.
	commitmentType := rpcCommitmentType(dbChannel.ChanType)

	channel := &rpc_pb.Channel{
		Active:                isActive,
		Private:               !isPublic,
		RemotePubkey:          nodeID,
		ChannelPoint:          chanPoint.String(),
		ChanId:                dbChannel.ShortChannelID.ToUint64(),
		Capacity:              int64(dbChannel.Capacity),
		LocalBalance:          int64(localBalance.ToSatoshis()),
		RemoteBalance:         int64(remoteBalance.ToSatoshis()),
		CommitFee:             int64(externalCommitFee),
		CommitWeight:          commitWeight,
		FeePerKw:              int64(localCommit.FeePerKw),
		TotalSatoshisSent:     int64(dbChannel.TotalMSatSent.ToSatoshis()),
		TotalSatoshisReceived: int64(dbChannel.TotalMSatReceived.ToSatoshis()),
		NumUpdates:            localCommit.CommitHeight,
		PendingHtlcs:          make([]*rpc_pb.HTLC, len(localCommit.Htlcs)),
		Initiator:             dbChannel.IsInitiator,
		ChanStatusFlags:       dbChannel.ChanStatus().String(),
		StaticRemoteKey:       commitmentType == rpc_pb.CommitmentType_STATIC_REMOTE_KEY,
		CommitmentType:        commitmentType,
		ThawHeight:            dbChannel.ThawHeight,
		LocalConstraints: createChannelConstraint(
			&dbChannel.LocalChanCfg,
		),
		RemoteConstraints: createChannelConstraint(
			&dbChannel.RemoteChanCfg,
		),
		// TODO: remove the following deprecated fields
		CsvDelay:             uint32(dbChannel.LocalChanCfg.CsvDelay),
		LocalChanReserveSat:  int64(dbChannel.LocalChanCfg.ChanReserve),
		RemoteChanReserveSat: int64(dbChannel.RemoteChanCfg.ChanReserve),
	}

	for i, htlc := range localCommit.Htlcs {
		var rHash [32]byte
		copy(rHash[:], htlc.RHash[:])

		circuitMap := r.server.htlcSwitch.CircuitLookup()

		var forwardingChannel, forwardingHtlcIndex uint64
		switch {
		case htlc.Incoming:
			circuit := circuitMap.LookupCircuit(
				htlcswitch.CircuitKey{
					ChanID: dbChannel.ShortChannelID,
					HtlcID: htlc.HtlcIndex,
				},
			)
			if circuit != nil && circuit.Outgoing != nil {
				forwardingChannel = circuit.Outgoing.ChanID.
					ToUint64()

				forwardingHtlcIndex = circuit.Outgoing.HtlcID
			}

		case !htlc.Incoming:
			circuit := circuitMap.LookupOpenCircuit(
				htlcswitch.CircuitKey{
					ChanID: dbChannel.ShortChannelID,
					HtlcID: htlc.HtlcIndex,
				},
			)

			// If the incoming channel id is the special hop.Source
			// value, the htlc index is a local payment identifier.
			// In this case, report nothing.
			if circuit != nil &&
				circuit.Incoming.ChanID != hop.Source {

				forwardingChannel = circuit.Incoming.ChanID.
					ToUint64()

				forwardingHtlcIndex = circuit.Incoming.HtlcID
			}
		}

		channel.PendingHtlcs[i] = &rpc_pb.HTLC{
			Incoming:            htlc.Incoming,
			Amount:              int64(htlc.Amt.ToSatoshis()),
			HashLock:            rHash[:],
			ExpirationHeight:    htlc.RefundTimeout,
			HtlcIndex:           htlc.HtlcIndex,
			ForwardingChannel:   forwardingChannel,
			ForwardingHtlcIndex: forwardingHtlcIndex,
		}

		// Add the Pending Htlc Amount to UnsettledBalance field.
		channel.UnsettledBalance += channel.PendingHtlcs[i].Amount
	}

	// Lookup our balances at height 0, because they will reflect any
	// push amounts that may have been present when this channel was
	// created.
	localBalance, remoteBalance, err := dbChannel.BalancesAtHeight(0)
	if err != nil {
		return nil, err
	}

	// If we initiated opening the channel, the zero height remote balance
	// is the push amount. Otherwise, our starting balance is the push
	// amount. If there is no push amount, these values will simply be zero.
	if dbChannel.IsInitiator {
		channel.PushAmountSat = uint64(remoteBalance.ToSatoshis())
	} else {
		channel.PushAmountSat = uint64(localBalance.ToSatoshis())
	}

	if len(dbChannel.LocalShutdownScript) > 0 {
		_, addresses, _, err := txscript.ExtractPkScriptAddrs(
			dbChannel.LocalShutdownScript, r.cfg.ActiveNetParams.Params,
		)
		if err != nil {
			return nil, err
		}

		// We only expect one upfront shutdown address for a channel. If
		// LocalShutdownScript is non-zero, there should be one payout
		// address set.
		if len(addresses) != 1 {
			return nil, er.Errorf("expected one upfront shutdown "+
				"address, got: %v", len(addresses))
		}

		channel.CloseAddress = addresses[0].String()
	}

	// If the server hasn't fully started yet, it's possible that the
	// channel event store hasn't either, so it won't be able to consume any
	// requests until then. To prevent blocking, we'll just omit the uptime
	// related fields for now.
	if !r.server.Started() {
		return channel, nil
	}

	peer, err := route.NewVertexFromBytes(nodePub.SerializeCompressed())
	if err != nil {
		return nil, err
	}

	// Query the event store for additional information about the channel.
	// Do not fail if it is not available, because there is a potential
	// race between a channel being added to our node and the event store
	// being notified of it.
	outpoint := dbChannel.FundingOutpoint
	info, err := r.server.chanEventStore.GetChanInfo(outpoint, peer)
	switch {
	// If the store does not know about the channel, we just log it.
	case chanfitness.ErrChannelNotFound.Is(err):
		log.Infof("channel: %v not found by channel event store",
			outpoint)

	// If we got our channel info, we further populate the channel.
	case err == nil:
		channel.Uptime = int64(info.Uptime.Seconds())
		channel.Lifetime = int64(info.Lifetime.Seconds())

	// If we get an unexpected error, we return it.
	default:
		return nil, err
	}

	return channel, nil
}

// createRPCClosedChannel creates an *rpc_pb.ClosedChannelSummary from a
// *channeldb.ChannelCloseSummary.
func (r *LightningRPCServer) createRPCClosedChannel(
	dbChannel *channeldb.ChannelCloseSummary) (*rpc_pb.ChannelCloseSummary, er.R) {

	nodePub := dbChannel.RemotePub
	nodeID := nodePub.SerializeCompressed()

	var (
		closeType      rpc_pb.ChannelCloseSummary_ClosureType
		openInit       rpc_pb.Initiator
		closeInitiator rpc_pb.Initiator
		err            er.R
	)

	// Lookup local and remote cooperative initiators. If these values
	// are not known they will just return unknown.
	openInit, closeInitiator, err = r.getInitiators(
		&dbChannel.ChanPoint,
	)
	if err != nil {
		return nil, err
	}

	// Convert the close type to rpc type.
	switch dbChannel.CloseType {
	case channeldb.CooperativeClose:
		closeType = rpc_pb.ChannelCloseSummary_COOPERATIVE_CLOSE
	case channeldb.LocalForceClose:
		closeType = rpc_pb.ChannelCloseSummary_LOCAL_FORCE_CLOSE
	case channeldb.RemoteForceClose:
		closeType = rpc_pb.ChannelCloseSummary_REMOTE_FORCE_CLOSE
	case channeldb.BreachClose:
		closeType = rpc_pb.ChannelCloseSummary_BREACH_CLOSE
	case channeldb.FundingCanceled:
		closeType = rpc_pb.ChannelCloseSummary_FUNDING_CANCELED
	case channeldb.Abandoned:
		closeType = rpc_pb.ChannelCloseSummary_ABANDONED
	}

	channel := &rpc_pb.ChannelCloseSummary{
		Capacity:          int64(dbChannel.Capacity),
		RemotePubkey:      nodeID,
		CloseHeight:       dbChannel.CloseHeight,
		CloseType:         closeType,
		ChannelPoint:      dbChannel.ChanPoint.String(),
		ChanId:            dbChannel.ShortChanID.ToUint64(),
		SettledBalance:    int64(dbChannel.SettledBalance),
		TimeLockedBalance: int64(dbChannel.TimeLockedBalance),
		ChainHash:         dbChannel.ChainHash.String(),
		ClosingTxHash:     dbChannel.ClosingTXID.String(),
		OpenInitiator:     openInit,
		CloseInitiator:    closeInitiator,
	}

	reports, err := r.server.remoteChanDB.FetchChannelReports(
		*r.cfg.ActiveNetParams.GenesisHash, &dbChannel.ChanPoint,
	)
	switch {
	// If the channel does not have its resolver outcomes stored,
	// ignore it.
	case channeldb.ErrNoChainHashBucket.Is(err):
		fallthrough
	case channeldb.ErrNoChannelSummaries.Is(err):
		return channel, nil

	// If there is no error, fallthrough the switch to process reports.
	case err == nil:

	// If another error occurred, return it.
	default:
		return nil, err
	}

	for _, report := range reports {
		rpcResolution, err := rpcChannelResolution(report)
		if err != nil {
			return nil, err
		}

		channel.Resolutions = append(channel.Resolutions, rpcResolution)
	}

	return channel, nil
}

func rpcChannelResolution(report *channeldb.ResolverReport) (*rpc_pb.Resolution,
	er.R) {

	res := &rpc_pb.Resolution{
		AmountSat: uint64(report.Amount),
		Outpoint: &rpc_pb.OutPoint{
			OutputIndex: report.OutPoint.Index,
			TxidStr:     report.OutPoint.Hash.String(),
			TxidBytes:   report.OutPoint.Hash[:],
		},
	}

	if report.SpendTxID != nil {
		res.SweepTxid = report.SpendTxID.String()
	}

	switch report.ResolverType {
	case channeldb.ResolverTypeAnchor:
		res.ResolutionType = rpc_pb.ResolutionType_ANCHOR

	case channeldb.ResolverTypeIncomingHtlc:
		res.ResolutionType = rpc_pb.ResolutionType_INCOMING_HTLC

	case channeldb.ResolverTypeOutgoingHtlc:
		res.ResolutionType = rpc_pb.ResolutionType_OUTGOING_HTLC

	case channeldb.ResolverTypeCommit:
		res.ResolutionType = rpc_pb.ResolutionType_COMMIT

	default:
		return nil, er.Errorf("unknown resolver type: %v",
			report.ResolverType)
	}

	switch report.ResolverOutcome {
	case channeldb.ResolverOutcomeClaimed:
		res.Outcome = rpc_pb.ResolutionOutcome_CLAIMED

	case channeldb.ResolverOutcomeUnclaimed:
		res.Outcome = rpc_pb.ResolutionOutcome_UNCLAIMED

	case channeldb.ResolverOutcomeAbandoned:
		res.Outcome = rpc_pb.ResolutionOutcome_ABANDONED

	case channeldb.ResolverOutcomeFirstStage:
		res.Outcome = rpc_pb.ResolutionOutcome_FIRST_STAGE

	case channeldb.ResolverOutcomeTimeout:
		res.Outcome = rpc_pb.ResolutionOutcome_TIMEOUT

	default:
		return nil, er.Errorf("unknown outcome: %v",
			report.ResolverOutcome)
	}

	return res, nil
}

// getInitiators returns an initiator enum that provides information about the
// party that initiated channel's open and close. This information is obtained
// from the historical channel bucket, so unknown values are returned when the
// channel is not present (which indicates that it was closed before we started
// writing channels to the historical close bucket).
func (r *LightningRPCServer) getInitiators(chanPoint *wire.OutPoint) (
	rpc_pb.Initiator,
	rpc_pb.Initiator, er.R) {

	var (
		openInitiator  = rpc_pb.Initiator_INITIATOR_UNKNOWN
		closeInitiator = rpc_pb.Initiator_INITIATOR_UNKNOWN
	)

	// To get the close initiator for cooperative closes, we need
	// to get the channel status from the historical channel bucket.
	histChan, err := r.server.remoteChanDB.FetchHistoricalChannel(chanPoint)
	switch {
	// The node has upgraded from a version where we did not store
	// historical channels, and has not closed a channel since. Do
	// not return an error, initiator values are unknown.
	case channeldb.ErrNoHistoricalBucket.Is(err):
		return openInitiator, closeInitiator, nil

	// The channel was closed before we started storing historical
	// channels. Do  not return an error, initiator values are unknown.
	case channeldb.ErrChannelNotFound.Is(err):
		return openInitiator, closeInitiator, nil

	case err != nil:
		return 0, 0, err
	}

	// If we successfully looked up the channel, determine initiator based
	// on channels status.
	if histChan.IsInitiator {
		openInitiator = rpc_pb.Initiator_INITIATOR_LOCAL
	} else {
		openInitiator = rpc_pb.Initiator_INITIATOR_REMOTE
	}

	localInit := histChan.HasChanStatus(
		channeldb.ChanStatusLocalCloseInitiator,
	)

	remoteInit := histChan.HasChanStatus(
		channeldb.ChanStatusRemoteCloseInitiator,
	)

	switch {
	// There is a possible case where closes were attempted by both parties.
	// We return the initiator as both in this case to provide full
	// information about the close.
	case localInit && remoteInit:
		closeInitiator = rpc_pb.Initiator_INITIATOR_BOTH

	case localInit:
		closeInitiator = rpc_pb.Initiator_INITIATOR_LOCAL

	case remoteInit:
		closeInitiator = rpc_pb.Initiator_INITIATOR_REMOTE
	}

	return openInitiator, closeInitiator, nil
}

// SubscribeChannelEvents returns a uni-directional stream (server -> client)
// for notifying the client of newly active, inactive or closed channels.
func (r *LightningRPCServer) SubscribeChannelEvents(req *rpc_pb.ChannelEventSubscription,
	updateStream rpc_pb.Lightning_SubscribeChannelEventsServer) error {

	channelEventSub, err := r.server.channelNotifier.SubscribeChannelEvents()
	if err != nil {
		return er.Native(err)
	}

	// Ensure that the resources for the client is cleaned up once either
	// the server, or client exits.
	defer channelEventSub.Cancel()

	graph := r.server.localChanDB.ChannelGraph()

	for {
		select {
		// A new update has been sent by the channel router, we'll
		// marshal it into the form expected by the gRPC client, then
		// send it off to the client(s).
		case e := <-channelEventSub.Updates():
			var update *rpc_pb.ChannelEventUpdate
			switch event := e.(type) {
			case channelnotifier.PendingOpenChannelEvent:
				update = &rpc_pb.ChannelEventUpdate{
					Type: rpc_pb.ChannelEventUpdate_PENDING_OPEN_CHANNEL,
					Channel: &rpc_pb.ChannelEventUpdate_PendingOpenChannel{
						PendingOpenChannel: &rpc_pb.PendingUpdate{
							Txid:        event.ChannelPoint.Hash[:],
							OutputIndex: event.ChannelPoint.Index,
						},
					},
				}
			case channelnotifier.OpenChannelEvent:
				channel, err := createRPCOpenChannel(r, graph,
					event.Channel, true)
				if err != nil {
					return er.Native(err)
				}

				update = &rpc_pb.ChannelEventUpdate{
					Type: rpc_pb.ChannelEventUpdate_OPEN_CHANNEL,
					Channel: &rpc_pb.ChannelEventUpdate_OpenChannel{
						OpenChannel: channel,
					},
				}

			case channelnotifier.ClosedChannelEvent:
				closedChannel, err := r.createRPCClosedChannel(
					event.CloseSummary,
				)
				if err != nil {
					return er.Native(err)
				}

				update = &rpc_pb.ChannelEventUpdate{
					Type: rpc_pb.ChannelEventUpdate_CLOSED_CHANNEL,
					Channel: &rpc_pb.ChannelEventUpdate_ClosedChannel{
						ClosedChannel: closedChannel,
					},
				}

			case channelnotifier.ActiveChannelEvent:
				update = &rpc_pb.ChannelEventUpdate{
					Type: rpc_pb.ChannelEventUpdate_ACTIVE_CHANNEL,
					Channel: &rpc_pb.ChannelEventUpdate_ActiveChannel{
						ActiveChannel: &rpc_pb.ChannelPoint{
							FundingTxid: &rpc_pb.ChannelPoint_FundingTxidBytes{
								FundingTxidBytes: event.ChannelPoint.Hash[:],
							},
							OutputIndex: event.ChannelPoint.Index,
						},
					},
				}

			case channelnotifier.InactiveChannelEvent:
				update = &rpc_pb.ChannelEventUpdate{
					Type: rpc_pb.ChannelEventUpdate_INACTIVE_CHANNEL,
					Channel: &rpc_pb.ChannelEventUpdate_InactiveChannel{
						InactiveChannel: &rpc_pb.ChannelPoint{
							FundingTxid: &rpc_pb.ChannelPoint_FundingTxidBytes{
								FundingTxidBytes: event.ChannelPoint.Hash[:],
							},
							OutputIndex: event.ChannelPoint.Index,
						},
					},
				}

			// Completely ignore ActiveLinkEvent as this is explicitly not
			// exposed to the RPC.
			case channelnotifier.ActiveLinkEvent:
				continue

			default:
				return er.Native(er.Errorf("unexpected channel event update: %v", event))
			}

			if err := updateStream.Send(update); err != nil {
				return er.Native(er.E(err))
			}
		case <-r.quit:
			return nil
		}
	}
}

// paymentStream enables different types of payment streams, such as:
// rpc_pb.Lightning_SendPaymentServer and rpc_pb.Lightning_SendToRouteServer to
// execute sendPayment. We use this struct as a sort of bridge to enable code
// re-use between SendPayment and SendToRoute.
type paymentStream struct {
	recv func() (*rpcPaymentRequest, er.R)
	send func(*rpc_pb.SendResponse) er.R
}

// rpcPaymentRequest wraps rpc_pb.SendRequest so that routes from
// rpc_pb.SendToRouteRequest can be passed to sendPayment.
type rpcPaymentRequest struct {
	*rpc_pb.SendRequest
	route *route.Route
}

// SendPayment dispatches a bi-directional streaming RPC for sending payments
// through the Lightning Network. A single RPC invocation creates a persistent
// bi-directional stream allowing clients to rapidly send payments through the
// Lightning Network with a single persistent connection.
func (r *LightningRPCServer) SendPayment(stream rpc_pb.Lightning_SendPaymentServer) error {
	var lock sync.Mutex

	return er.Native(r.sendPayment(&paymentStream{
		recv: func() (*rpcPaymentRequest, er.R) {
			req, err := stream.Recv()
			if err != nil {
				return nil, er.E(err)
			}

			return &rpcPaymentRequest{
				SendRequest: req,
			}, nil
		},
		send: func(r *rpc_pb.SendResponse) er.R {
			// Calling stream.Send concurrently is not safe.
			lock.Lock()
			defer lock.Unlock()
			return er.E(stream.Send(r))
		},
	}))
}

// SendToRoute dispatches a bi-directional streaming RPC for sending payments
// through the Lightning Network via predefined routes passed in. A single RPC
// invocation creates a persistent bi-directional stream allowing clients to
// rapidly send payments through the Lightning Network with a single persistent
// connection.
func (r *LightningRPCServer) SendToRoute(stream rpc_pb.Lightning_SendToRouteServer) error {
	var lock sync.Mutex

	return er.Native(r.sendPayment(&paymentStream{
		recv: func() (*rpcPaymentRequest, er.R) {
			req, err := stream.Recv()
			if err != nil {
				return nil, er.E(err)
			}

			return r.unmarshallSendToRouteRequest(req)
		},
		send: func(r *rpc_pb.SendResponse) er.R {
			// Calling stream.Send concurrently is not safe.
			lock.Lock()
			defer lock.Unlock()
			return er.E(stream.Send(r))
		},
	}))
}

// unmarshallSendToRouteRequest unmarshalls an rpc sendtoroute request
func (r *LightningRPCServer) unmarshallSendToRouteRequest(
	req *rpc_pb.SendToRouteRequest) (*rpcPaymentRequest, er.R) {

	if req.Route == nil {
		return nil, er.Errorf("unable to send, no route provided")
	}

	route, err := r.routerBackend.UnmarshallRoute(req.Route)
	if err != nil {
		return nil, err
	}

	return &rpcPaymentRequest{
		SendRequest: &rpc_pb.SendRequest{
			PaymentHash: req.PaymentHash,
		},
		route: route,
	}, nil
}

// rpcPaymentIntent is a small wrapper struct around the of values we can
// receive from a client over RPC if they wish to send a payment. We'll either
// extract these fields from a payment request (which may include routing
// hints), or we'll get a fully populated route from the user that we'll pass
// directly to the channel router for dispatching.
type rpcPaymentIntent struct {
	msat               lnwire.MilliSatoshi
	feeLimit           lnwire.MilliSatoshi
	cltvLimit          uint32
	dest               route.Vertex
	rHash              [32]byte
	cltvDelta          uint16
	routeHints         [][]zpay32.HopHint
	outgoingChannelIDs []uint64
	lastHop            *route.Vertex
	destFeatures       *lnwire.FeatureVector
	paymentAddr        *[32]byte
	payReq             []byte

	destCustomRecords record.CustomSet

	route *route.Route
}

// extractPaymentIntent attempts to parse the complete details required to
// dispatch a client from the information presented by an RPC client. There are
// three ways a client can specify their payment details: a payment request,
// via manual details, or via a complete route.
func (r *LightningRPCServer) extractPaymentIntent(rpcPayReq *rpcPaymentRequest) (rpcPaymentIntent, er.R) {
	payIntent := rpcPaymentIntent{}

	// If a route was specified, then we can use that directly.
	if rpcPayReq.route != nil {
		copy(payIntent.rHash[:], rpcPayReq.PaymentHash)

		payIntent.route = rpcPayReq.route
		return payIntent, nil
	}

	// If there are no routes specified, pass along a outgoing channel
	// restriction if specified. The main server rpc does not support
	// multiple channel restrictions.
	if rpcPayReq.OutgoingChanId != 0 {
		payIntent.outgoingChannelIDs = []uint64{
			rpcPayReq.OutgoingChanId,
		}
	}

	// Pass along a last hop restriction if specified.
	if len(rpcPayReq.LastHopPubkey) > 0 {
		lastHop, err := route.NewVertexFromBytes(
			rpcPayReq.LastHopPubkey,
		)
		if err != nil {
			return payIntent, err
		}
		payIntent.lastHop = &lastHop
	}

	// Take the CLTV limit from the request if set, otherwise use the max.
	cltvLimit, err := routerrpc.ValidateCLTVLimit(
		rpcPayReq.CltvLimit, r.cfg.MaxOutgoingCltvExpiry,
	)
	if err != nil {
		return payIntent, err
	}
	payIntent.cltvLimit = cltvLimit

	customRecords := record.CustomSet(rpcPayReq.DestCustomRecords)
	if err := customRecords.Validate(); err != nil {
		return payIntent, err
	}
	payIntent.destCustomRecords = customRecords

	validateDest := func(dest route.Vertex) er.R {
		if rpcPayReq.AllowSelfPayment {
			return nil
		}

		if dest == r.selfNode {
			return er.New("self-payments not allowed")
		}

		return nil
	}

	// If the payment request field isn't blank, then the details of the
	// invoice are encoded entirely within the encoded payReq.  So we'll
	// attempt to decode it, populating the payment accordingly.
	if rpcPayReq.PaymentRequest != "" {
		payReq, err := zpay32.Decode(
			rpcPayReq.PaymentRequest, r.cfg.ActiveNetParams.Params,
		)
		if err != nil {
			return payIntent, err
		}

		// Next, we'll ensure that this payreq hasn't already expired.
		err = routerrpc.ValidatePayReqExpiry(payReq)
		if err != nil {
			return payIntent, err
		}

		// If the amount was not included in the invoice, then we let
		// the payee specify the amount of satoshis they wish to send.
		// We override the amount to pay with the amount provided from
		// the payment request.
		if payReq.MilliSat == nil {
			amt, err := lnrpc.UnmarshallAmt(
				rpcPayReq.Amt, rpcPayReq.AmtMsat,
			)
			if err != nil {
				return payIntent, err
			}
			if amt == 0 {
				return payIntent, er.New("amount must be " +
					"specified when paying a zero amount " +
					"invoice")
			}

			payIntent.msat = amt
		} else {
			payIntent.msat = *payReq.MilliSat
		}

		// Calculate the fee limit that should be used for this payment.
		payIntent.feeLimit = lnrpc.CalculateFeeLimit(
			rpcPayReq.FeeLimit, payIntent.msat,
		)

		copy(payIntent.rHash[:], payReq.PaymentHash[:])
		destKey := payReq.Destination.SerializeCompressed()
		copy(payIntent.dest[:], destKey)
		payIntent.cltvDelta = uint16(payReq.MinFinalCLTVExpiry())
		payIntent.routeHints = payReq.RouteHints
		payIntent.payReq = []byte(rpcPayReq.PaymentRequest)
		payIntent.destFeatures = payReq.Features
		payIntent.paymentAddr = payReq.PaymentAddr

		if err := validateDest(payIntent.dest); err != nil {
			return payIntent, err
		}

		return payIntent, nil
	}

	// At this point, a destination MUST be specified, so we'll convert it
	// into the proper representation now. The destination will either be
	// encoded as raw bytes, or via a hex string.
	var pubBytes []byte
	if len(rpcPayReq.Dest) != 0 {
		pubBytes = rpcPayReq.Dest
	}
	if len(pubBytes) != 33 {
		return payIntent, er.New("invalid key length")
	}
	copy(payIntent.dest[:], pubBytes)

	if err := validateDest(payIntent.dest); err != nil {
		return payIntent, err
	}

	// Otherwise, If the payment request field was not specified
	// (and a custom route wasn't specified), construct the payment
	// from the other fields.
	payIntent.msat, err = lnrpc.UnmarshallAmt(
		rpcPayReq.Amt, rpcPayReq.AmtMsat,
	)
	if err != nil {
		return payIntent, err
	}

	// Calculate the fee limit that should be used for this payment.
	payIntent.feeLimit = lnrpc.CalculateFeeLimit(
		rpcPayReq.FeeLimit, payIntent.msat,
	)

	if rpcPayReq.FinalCltvDelta != 0 {
		payIntent.cltvDelta = uint16(rpcPayReq.FinalCltvDelta)
	} else {
		// If no final cltv delta is given, assume the default that we
		// use when creating an invoice. We do not assume the default of
		// 9 blocks that is defined in BOLT-11, because this is never
		// enough for other lnd nodes.
		payIntent.cltvDelta = uint16(r.cfg.Bitcoin.TimeLockDelta)
	}

	copy(payIntent.rHash[:], rpcPayReq.PaymentHash)

	// Unmarshal any custom destination features.
	payIntent.destFeatures, err = routerrpc.UnmarshalFeatures(
		rpcPayReq.DestFeatures,
	)
	if err != nil {
		return payIntent, err
	}

	return payIntent, nil
}

type paymentIntentResponse struct {
	Route    *route.Route
	Preimage [32]byte
	Err      er.R
}

// dispatchPaymentIntent attempts to fully dispatch an RPC payment intent.
// We'll either pass the payment as a whole to the channel router, or give it a
// pre-built route. The first error this method returns denotes if we were
// unable to save the payment. The second error returned denotes if the payment
// didn't succeed.
func (r *LightningRPCServer) dispatchPaymentIntent(
	payIntent *rpcPaymentIntent) (*paymentIntentResponse, er.R) {

	// Construct a payment request to send to the channel router. If the
	// payment is successful, the route chosen will be returned. Otherwise,
	// we'll get a non-nil error.
	var (
		preImage  [32]byte
		route     *route.Route
		routerErr er.R
	)

	// If a route was specified, then we'll pass the route directly to the
	// router, otherwise we'll create a payment session to execute it.
	if payIntent.route == nil {
		payment := &routing.LightningPayment{
			Target:             payIntent.dest,
			Amount:             payIntent.msat,
			FinalCLTVDelta:     payIntent.cltvDelta,
			FeeLimit:           payIntent.feeLimit,
			CltvLimit:          payIntent.cltvLimit,
			PaymentHash:        payIntent.rHash,
			RouteHints:         payIntent.routeHints,
			OutgoingChannelIDs: payIntent.outgoingChannelIDs,
			LastHop:            payIntent.lastHop,
			PaymentRequest:     payIntent.payReq,
			PayAttemptTimeout:  routing.DefaultPayAttemptTimeout,
			DestCustomRecords:  payIntent.destCustomRecords,
			DestFeatures:       payIntent.destFeatures,
			PaymentAddr:        payIntent.paymentAddr,

			// Don't enable multi-part payments on the main rpc.
			// Users need to use routerrpc for that.
			MaxParts: 1,
		}

		preImage, route, routerErr = r.server.chanRouter.SendPayment(
			payment,
		)
	} else {
		var attempt *channeldb.HTLCAttempt
		attempt, routerErr = r.server.chanRouter.SendToRoute(
			payIntent.rHash, payIntent.route,
		)

		if routerErr == nil {
			preImage = attempt.Settle.Preimage
		}

		route = payIntent.route
	}

	// If the route failed, then we'll return a nil save err, but a non-nil
	// routing err.
	if routerErr != nil {
		log.Warnf("Unable to send payment: %v", routerErr)

		return &paymentIntentResponse{
			Err: routerErr,
		}, nil
	}

	return &paymentIntentResponse{
		Route:    route,
		Preimage: preImage,
	}, nil
}

// sendPayment takes a paymentStream (a source of pre-built routes or payment
// requests) and continually attempt to dispatch payment requests written to
// the write end of the stream. Responses will also be streamed back to the
// client via the write end of the stream. This method is by both SendToRoute
// and SendPayment as the logic is virtually identical.
func (r *LightningRPCServer) sendPayment(stream *paymentStream) er.R {
	payChan := make(chan *rpcPaymentIntent)
	errChan := make(chan er.R, 1)

	// We don't allow payments to be sent while the daemon itself is still
	// syncing as we may be trying to sent a payment over a "stale"
	// channel.
	if !r.server.Started() {
		return ErrServerNotActive.Default()
	}

	// TODO(roasbeef): check payment filter to see if already used?

	// In order to limit the level of concurrency and prevent a client from
	// attempting to OOM the server, we'll set up a semaphore to create an
	// upper ceiling on the number of outstanding payments.
	const numOutstandingPayments = 2000
	htlcSema := make(chan struct{}, numOutstandingPayments)
	for i := 0; i < numOutstandingPayments; i++ {
		htlcSema <- struct{}{}
	}

	// We keep track of the running goroutines and set up a quit signal we
	// can use to request them to exit if the method returns because of an
	// encountered error.
	var wg sync.WaitGroup
	reqQuit := make(chan struct{})
	defer close(reqQuit)

	// Launch a new goroutine to handle reading new payment requests from
	// the client. This way we can handle errors independently of blocking
	// and waiting for the next payment request to come through.
	// TODO(joostjager): Callers expect result to come in in the same order
	// as the request were sent, but this is far from guarantueed in the
	// code below.
	wg.Add(1)
	go func() {
		defer wg.Done()

		for {
			select {
			case <-reqQuit:
				return

			default:
				// Receive the next pending payment within the
				// stream sent by the client. If we read the
				// EOF sentinel, then the client has closed the
				// stream, and we can exit normally.
				nextPayment, err := stream.recv()
				if er.EOF.Is(err) {
					close(payChan)
					return
				} else if err != nil {
					log.Errorf("Failed receiving from "+
						"stream: %v", err)

					select {
					case errChan <- err:
					default:
					}
					return
				}

				// Populate the next payment, either from the
				// payment request, or from the explicitly set
				// fields. If the payment proto wasn't well
				// formed, then we'll send an error reply and
				// wait for the next payment.
				payIntent, err := r.extractPaymentIntent(
					nextPayment,
				)
				if err != nil {
					if err := stream.send(&rpc_pb.SendResponse{
						PaymentError: err.Message(),
						PaymentHash:  payIntent.rHash[:],
					}); err != nil {
						log.Errorf("Failed "+
							"sending on "+
							"stream: %v", err)

						select {
						case errChan <- err:
						default:
						}
						return
					}
					continue
				}

				// If the payment was well formed, then we'll
				// send to the dispatch goroutine, or exit,
				// which ever comes first.
				select {
				case payChan <- &payIntent:
				case <-reqQuit:
					return
				}
			}
		}
	}()

sendLoop:
	for {
		select {

		// If we encounter and error either during sending or
		// receiving, we return directly, closing the stream.
		case err := <-errChan:
			return err

		case <-r.quit:
			return er.New("rpc server shutting down")

		case payIntent, ok := <-payChan:
			// If the receive loop is done, we break the send loop
			// and wait for the ongoing payments to finish before
			// exiting.
			if !ok {
				break sendLoop
			}

			// We launch a new goroutine to execute the current
			// payment so we can continue to serve requests while
			// this payment is being dispatched.
			wg.Add(1)
			go func() {
				defer wg.Done()

				// Attempt to grab a free semaphore slot, using
				// a defer to eventually release the slot
				// regardless of payment success.
				select {
				case <-htlcSema:
				case <-reqQuit:
					return
				}
				defer func() {
					htlcSema <- struct{}{}
				}()

				resp, saveErr := r.dispatchPaymentIntent(
					payIntent,
				)

				switch {
				// If we were unable to save the state of the
				// payment, then we'll return the error to the
				// user, and terminate.
				case saveErr != nil:
					log.Errorf("Failed dispatching "+
						"payment intent: %v", saveErr)

					select {
					case errChan <- saveErr:
					default:
					}
					return

				// If we receive payment error than, instead of
				// terminating the stream, send error response
				// to the user.
				case resp.Err != nil:
					err := stream.send(&rpc_pb.SendResponse{
						PaymentError: resp.Err.Message(),
						PaymentHash:  payIntent.rHash[:],
					})
					if err != nil {
						log.Errorf("Failed "+
							"sending error "+
							"response: %v", err)

						select {
						case errChan <- err:
						default:
						}
					}
					return
				}

				backend := r.routerBackend
				marshalledRouted, err := backend.MarshallRoute(
					resp.Route,
				)
				if err != nil {
					errChan <- err
					return
				}

				err = stream.send(&rpc_pb.SendResponse{
					PaymentHash:     payIntent.rHash[:],
					PaymentPreimage: resp.Preimage[:],
					PaymentRoute:    marshalledRouted,
				})
				if err != nil {
					log.Errorf("Failed sending "+
						"response: %v", err)

					select {
					case errChan <- err:
					default:
					}
					return
				}
			}()
		}
	}

	// Wait for all goroutines to finish before closing the stream.
	wg.Wait()
	return nil
}

// SendPaymentSync is the synchronous non-streaming version of SendPayment.
// This RPC is intended to be consumed by clients of the REST proxy.
// Additionally, this RPC expects the destination's public key and the payment
// hash (if any) to be encoded as hex strings.
func (r *LightningRPCServer) SendPaymentSync(ctx context.Context,
	nextPayment *rpc_pb.SendRequest) (*rpc_pb.SendResponse, er.R) {

	return r.sendPaymentSync(ctx, &rpcPaymentRequest{SendRequest: nextPayment})
}

// SendToRouteSync is the synchronous non-streaming version of SendToRoute.
// This RPC is intended to be consumed by clients of the REST proxy.
// Additionally, this RPC expects the payment hash (if any) to be encoded as
// hex strings.
func (r *LightningRPCServer) SendToRouteSync(ctx context.Context,
	req *rpc_pb.SendToRouteRequest) (*rpc_pb.SendResponse, error) {

	if req.Route == nil {
		return nil, er.Native(er.Errorf("unable to send, no routes provided"))
	}

	paymentRequest, err := r.unmarshallSendToRouteRequest(req)
	if err != nil {
		return nil, er.Native(err)
	}

	res, err := r.sendPaymentSync(ctx, paymentRequest)
	return res, er.Native(err)
}

// sendPaymentSync is the synchronous variant of sendPayment. It will block and
// wait until the payment has been fully completed.
func (r *LightningRPCServer) sendPaymentSync(ctx context.Context,
	nextPayment *rpcPaymentRequest) (*rpc_pb.SendResponse, er.R) {

	// We don't allow payments to be sent while the daemon itself is still
	// syncing as we may be trying to sent a payment over a "stale"
	// channel.
	if !r.server.Started() {
		return nil, ErrServerNotActive.Default()
	}

	// First we'll attempt to map the proto describing the next payment to
	// an intent that we can pass to local sub-systems.
	payIntent, err := r.extractPaymentIntent(nextPayment)
	if err != nil {
		return nil, err
	}

	// With the payment validated, we'll now attempt to dispatch the
	// payment.
	resp, saveErr := r.dispatchPaymentIntent(&payIntent)
	switch {
	case saveErr != nil:
		return nil, saveErr

	case resp.Err != nil:
		return &rpc_pb.SendResponse{
			PaymentError: resp.Err.Message(),
			PaymentHash:  payIntent.rHash[:],
		}, nil
	}

	rpcRoute, err := r.routerBackend.MarshallRoute(resp.Route)
	if err != nil {
		return nil, err
	}

	return &rpc_pb.SendResponse{
		PaymentHash:     payIntent.rHash[:],
		PaymentPreimage: resp.Preimage[:],
		PaymentRoute:    rpcRoute,
	}, nil
}

// AddInvoice attempts to add a new invoice to the invoice database. Any
// duplicated invoices are rejected, therefore all invoices *must* have a
// unique payment preimage.
func (r *LightningRPCServer) AddInvoice(ctx context.Context,
	invoice *rpc_pb.Invoice) (*rpc_pb.AddInvoiceResponse, er.R) {
	
	defaultDelta := r.cfg.Bitcoin.TimeLockDelta
	if r.cfg.registeredChains.PrimaryChain() == chainreg.LitecoinChain {
		defaultDelta = r.cfg.Litecoin.TimeLockDelta
	}

	addInvoiceCfg := &invoicesrpc.AddInvoiceConfig{
		AddInvoice:        r.server.invoices.AddInvoice,
		IsChannelActive:   r.server.htlcSwitch.HasActiveLink,
		ChainParams:       r.cfg.ActiveNetParams.Params,
		NodeSigner:        r.server.nodeSigner,
		DefaultCLTVExpiry: defaultDelta,
		ChanDB:            r.server.remoteChanDB,
		Graph:             r.server.localChanDB.ChannelGraph(),
		GenInvoiceFeatures: func() *lnwire.FeatureVector {
			return r.server.featureMgr.Get(feature.SetInvoice)
		},
	}
	
	value, err := lnrpc.UnmarshallAmt(invoice.Value, invoice.ValueMsat)
	if err != nil {
		return nil, err
	}

	// Convert the passed routing hints to the required format.
		routeHints, err := invoicesrpc.CreateZpay32HopHints(invoice.RouteHints)
	if err != nil {
		return nil, err
	}
	addInvoiceData := &invoicesrpc.AddInvoiceData{
		Memo:            invoice.Memo,
		Value:           value,
		DescriptionHash: invoice.DescriptionHash,
		Expiry:          invoice.Expiry,
		FallbackAddr:    invoice.FallbackAddr,
		CltvExpiry:      invoice.CltvExpiry,
		Private:         invoice.Private,
		RouteHints:      routeHints,
	}
	
	if invoice.RPreimage != nil {
		preimage, err := lntypes.MakePreimage(invoice.RPreimage)
		if err != nil {
			return nil, err
		}
		addInvoiceData.Preimage = &preimage
	}
	
	hash, dbInvoice, err := invoicesrpc.AddInvoice(
		ctx, addInvoiceCfg, addInvoiceData,
	)
	if err != nil {
		return nil, err
	}
	
	return &rpc_pb.AddInvoiceResponse{
		AddIndex:       dbInvoice.AddIndex,
		PaymentRequest: string(dbInvoice.PaymentRequest),
		RHash:          hash[:],
	}, nil
}

// LookupInvoice attempts to look up an invoice according to its payment hash.
// The passed payment hash *must* be exactly 32 bytes, if not an error is
// returned.
func (r *LightningRPCServer) LookupInvoice(ctx context.Context,
	req *rpc_pb.PaymentHash) (*rpc_pb.Invoice, er.R) {

	var (
		payHash [32]byte
		rHash   []byte
		err     er.R
	)

	// If the RHash as a raw string was provided, then decode that and use
	// that directly. Otherwise, we use the raw bytes provided.
	if req.RHash != nil && len(req.RHash) > 0 {
		rHash, err = util.DecodeHex(string(req.RHash))
		if err != nil {
			log.Info("unable to decode hex string, will take rHash as bytes: %v", err.Message())
			rHash = req.RHash
		}
	} else {
		rHash = req.RHash
	}

	// Ensure that the payment hash is *exactly* 32-bytes.
	if len(rHash) != 0 && len(rHash) != 32 {
		return nil, er.Errorf("payment hash must be exactly "+
			"32 bytes, is instead %v", len(rHash))
	}
	copy(payHash[:], rHash)

	log.Tracef("[lookupinvoice] searching for invoice %x", payHash[:])

	invoice, err := r.server.invoices.LookupInvoice(payHash)
	if err != nil {
		return nil, err
	}

	log.Tracef("[lookupinvoice] located invoice %v",
		log.C(func() string {
			return spew.Sdump(invoice)
		}))

	rpcInvoice, err := invoicesrpc.CreateRPCInvoice(
		&invoice, r.cfg.ActiveNetParams.Params,
	)
	if err != nil {
		return nil, err
	}

	return rpcInvoice, nil
}

// ListInvoices returns a list of all the invoices currently stored within the
// database. Any active debug invoices are ignored.
func (r *LightningRPCServer) ListInvoices(ctx context.Context,
	req *rpc_pb.ListInvoiceRequest) (*rpc_pb.ListInvoiceResponse, er.R) {

	// If the number of invoices was not specified, then we'll default to
	// returning the latest 100 invoices.
	if req.NumMaxInvoices == 0 {
		req.NumMaxInvoices = 100
	}

	// Next, we'll map the proto request into a format that is understood by
	// the database.
	q := channeldb.InvoiceQuery{
		IndexOffset:    req.IndexOffset,
		NumMaxInvoices: req.NumMaxInvoices,
		PendingOnly:    req.PendingOnly,
		Reversed:       req.Reversed,
	}
	invoiceSlice, err := r.server.remoteChanDB.QueryInvoices(q)
	if err != nil {
		return nil, er.Errorf("unable to query invoices: %v", err)
	}

	// Before returning the response, we'll need to convert each invoice
	// into it's proto representation.
	resp := &rpc_pb.ListInvoiceResponse{
		Invoices:         make([]*rpc_pb.Invoice, len(invoiceSlice.Invoices)),
		FirstIndexOffset: invoiceSlice.FirstIndexOffset,
		LastIndexOffset:  invoiceSlice.LastIndexOffset,
	}
	for i, invoice := range invoiceSlice.Invoices {
		invoice := invoice
		resp.Invoices[i], err = invoicesrpc.CreateRPCInvoice(
			&invoice, r.cfg.ActiveNetParams.Params,
		)
		if err != nil {
			return nil, err
		}
	}

	return resp, nil
}

// SubscribeInvoices returns a uni-directional stream (server -> client) for
// notifying the client of newly added/settled invoices.
func (r *LightningRPCServer) SubscribeInvoices(req *rpc_pb.InvoiceSubscription,
	updateStream rpc_pb.Lightning_SubscribeInvoicesServer) error {

	invoiceClient, err := r.server.invoices.SubscribeNotifications(
		req.AddIndex, req.SettleIndex,
	)
	if err != nil {
		return er.Native(err)
	}
	defer invoiceClient.Cancel()

	for {
		select {
		case newInvoice := <-invoiceClient.NewInvoices:
			rpcInvoice, err := invoicesrpc.CreateRPCInvoice(
				newInvoice, r.cfg.ActiveNetParams.Params,
			)
			if err != nil {
				return er.Native(err)
			}

			if err := updateStream.Send(rpcInvoice); err != nil {
				return er.Native(er.E(err))
			}

		case settledInvoice := <-invoiceClient.SettledInvoices:
			rpcInvoice, err := invoicesrpc.CreateRPCInvoice(
				settledInvoice, r.cfg.ActiveNetParams.Params,
			)
			if err != nil {
				return er.Native(err)
			}

			if err := updateStream.Send(rpcInvoice); err != nil {
				return er.Native(er.E(err))
			}

		case <-r.quit:
			return nil
		}
	}
}

/* TODO(cjd): This should be implemented where the data actually exists rather than
interfaces pointing to interfaces pointing to interfaces.

// SubscribeTransactions creates a uni-directional stream (server -> client) in
// which any newly discovered transactions relevant to the wallet are sent
// over.
func (r *LightningRPCServer) SubscribeTransactions(req *rpc_pb.GetTransactionsRequest,
	updateStream rpc_pb.Lightning_SubscribeTransactionsServer) error {

	txClient, err := r.server.cc.Wallet.SubscribeTransactions()
	if err != nil {
		return er.Native(err)
	}
	defer txClient.Cancel()

	for {
		select {
		case tx := <-txClient.ConfirmedTransactions():
			destAddresses := make([]string, 0, len(tx.DestAddresses))
			for _, destAddress := range tx.DestAddresses {
				destAddresses = append(destAddresses, destAddress.EncodeAddress())
			}
			detail := &rpc_pb.Transaction{
				TxHash:           tx.Hash.String(),
				Amount:           int64(tx.Value),
				NumConfirmations: tx.NumConfirmations,
				BlockHash:        tx.BlockHash.String(),
				BlockHeight:      tx.BlockHeight,
				TimeStamp:        tx.Timestamp,
				TotalFees:        tx.TotalFees,
				DestAddresses:    destAddresses,
				RawTxHex:         tx.RawTx,
			}
			if err := updateStream.Send(detail); err != nil {
				return err
			}

		case tx := <-txClient.UnconfirmedTransactions():
			var destAddresses []string
			for _, destAddress := range tx.DestAddresses {
				destAddresses = append(destAddresses, destAddress.EncodeAddress())
			}
			detail := &rpc_pb.Transaction{
				TxHash:        tx.Hash.String(),
				Amount:        int64(tx.Value),
				TimeStamp:     tx.Timestamp,
				TotalFees:     tx.TotalFees,
				DestAddresses: destAddresses,
				RawTxHex:      tx.RawTx,
			}
			if err := updateStream.Send(detail); err != nil {
				return err
			}

		case <-r.quit:
			return nil
		}
	}
}
*/

// DescribeGraph returns a description of the latest graph state from the PoV
// of the node. The graph information is partitioned into two components: all
// the nodes/vertexes, and all the edges that connect the vertexes themselves.
// As this is a directed graph, the edges also contain the node directional
// specific routing policy which includes: the time lock delta, fee
// information, etc.
func (r *LightningRPCServer) DescribeGraph(ctx context.Context,
	req *rpc_pb.ChannelGraphRequest) (*rpc_pb.ChannelGraph, er.R) {

	resp := &rpc_pb.ChannelGraph{}
	includeUnannounced := req.IncludeUnannounced

	// Obtain the pointer to the global singleton channel graph, this will
	// provide a consistent view of the graph due to bolt db's
	// transactional model.
	graph := r.server.localChanDB.ChannelGraph()

	// First iterate through all the known nodes (connected or unconnected
	// within the graph), collating their current state into the RPC
	// response.
	err := graph.ForEachNode(func(_ kvdb.RTx, node *channeldb.LightningNode) er.R {
		nodeAddrs := make([]*rpc_pb.NodeAddress, 0)
		for _, addr := range node.Addresses {
			nodeAddr := &rpc_pb.NodeAddress{
				Network: addr.Network(),
				Addr:    addr.String(),
			}
			nodeAddrs = append(nodeAddrs, nodeAddr)
		}

		lnNode := &rpc_pb.LightningNode{
			LastUpdate: uint32(node.LastUpdate.Unix()),
			PubKey:     []byte(hex.EncodeToString(node.PubKeyBytes[:])),
			Addresses:  nodeAddrs,
			Alias:      node.Alias,
			Color:      routing.EncodeHexColor(node.Color),
			Features:   invoicesrpc.CreateRPCFeatures(node.Features),
		}

		resp.Nodes = append(resp.Nodes, lnNode)

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Next, for each active channel we know of within the graph, create a
	// similar response which details both the edge information as well as
	// the routing policies of th nodes connecting the two edges.
	err = graph.ForEachChannel(func(edgeInfo *channeldb.ChannelEdgeInfo,
		c1, c2 *channeldb.ChannelEdgePolicy) er.R {

		// Do not include unannounced channels unless specifically
		// requested. Unannounced channels include both private channels as
		// well as public channels whose authentication proof were not
		// confirmed yet, hence were not announced.
		if !includeUnannounced && edgeInfo.AuthProof == nil {
			return nil
		}

		edge := marshalDbEdge(edgeInfo, c1, c2)
		resp.Edges = append(resp.Edges, edge)

		return nil
	})
	if err != nil && !channeldb.ErrGraphNoEdgesFound.Is(err) {
		return nil, err
	}

	return resp, nil
}

func marshalDbEdge(edgeInfo *channeldb.ChannelEdgeInfo,
	c1, c2 *channeldb.ChannelEdgePolicy) *rpc_pb.ChannelEdge {

	// Order the edges by increasing pubkey.
	if bytes.Compare(edgeInfo.NodeKey2Bytes[:],
		edgeInfo.NodeKey1Bytes[:]) < 0 {

		c2, c1 = c1, c2
	}

	var lastUpdate int64
	if c1 != nil {
		lastUpdate = c1.LastUpdate.Unix()
	}
	if c2 != nil && c2.LastUpdate.Unix() > lastUpdate {
		lastUpdate = c2.LastUpdate.Unix()
	}

	edge := &rpc_pb.ChannelEdge{
		ChannelId: edgeInfo.ChannelID,
		ChanPoint: edgeInfo.ChannelPoint.String(),
		// TODO(roasbeef): update should be on edge info itself
		LastUpdate: uint32(lastUpdate),
		Node1Pub:   edgeInfo.NodeKey1Bytes[:],
		Node2Pub:   edgeInfo.NodeKey2Bytes[:],
		Capacity:   int64(edgeInfo.Capacity),
	}

	if c1 != nil {
		edge.Node1Policy = &rpc_pb.RoutingPolicy{
			TimeLockDelta:    uint32(c1.TimeLockDelta),
			MinHtlc:          int64(c1.MinHTLC),
			MaxHtlcMsat:      uint64(c1.MaxHTLC),
			FeeBaseMsat:      int64(c1.FeeBaseMSat),
			FeeRateMilliMsat: int64(c1.FeeProportionalMillionths),
			Disabled:         c1.ChannelFlags&lnwire.ChanUpdateDisabled != 0,
			LastUpdate:       uint32(c1.LastUpdate.Unix()),
		}
	}

	if c2 != nil {
		edge.Node2Policy = &rpc_pb.RoutingPolicy{
			TimeLockDelta:    uint32(c2.TimeLockDelta),
			MinHtlc:          int64(c2.MinHTLC),
			MaxHtlcMsat:      uint64(c2.MaxHTLC),
			FeeBaseMsat:      int64(c2.FeeBaseMSat),
			FeeRateMilliMsat: int64(c2.FeeProportionalMillionths),
			Disabled:         c2.ChannelFlags&lnwire.ChanUpdateDisabled != 0,
			LastUpdate:       uint32(c2.LastUpdate.Unix()),
		}
	}

	return edge
}

// GetNodeMetrics returns all available node metrics calculated from the
// current channel graph.
func (r *LightningRPCServer) GetNodeMetrics(ctx context.Context,
	req *rpc_pb.NodeMetricsRequest) (*rpc_pb.NodeMetricsResponse, er.R) {

	// Get requested metric types.
	getCentrality := false
	for _, t := range req.Types {
		if t == rpc_pb.NodeMetricType_BETWEENNESS_CENTRALITY {
			getCentrality = true
		}
	}

	// Only centrality can be requested for now.
	if !getCentrality {
		return nil, nil
	}

	resp := &rpc_pb.NodeMetricsResponse{
		BetweennessCentrality: make(map[string]*rpc_pb.FloatMetric),
	}

	// Obtain the pointer to the global singleton channel graph, this will
	// provide a consistent view of the graph due to bolt db's
	// transactional model.
	graph := r.server.localChanDB.ChannelGraph()

	// Calculate betweenness centrality if requested. Note that depending on the
	// graph size, this may take up to a few minutes.
	channelGraph := autopilot.ChannelGraphFromDatabase(graph)
	centralityMetric, err := autopilot.NewBetweennessCentralityMetric(
		runtime.NumCPU(),
	)
	if err != nil {
		return nil, err
	}
	if err := centralityMetric.Refresh(channelGraph); err != nil {
		return nil, err
	}

	// Fill normalized and non normalized centrality.
	centrality := centralityMetric.GetMetric(true)
	for nodeID, val := range centrality {
		resp.BetweennessCentrality[hex.EncodeToString(nodeID[:])] =
			&rpc_pb.FloatMetric{
				NormalizedValue: val,
			}
	}

	centrality = centralityMetric.GetMetric(false)
	for nodeID, val := range centrality {
		resp.BetweennessCentrality[hex.EncodeToString(nodeID[:])].Value = val
	}

	return resp, nil
}

// GetChanInfo returns the latest authenticated network announcement for the
// given channel identified by its channel ID: an 8-byte integer which uniquely
// identifies the location of transaction's funding output within the block
// chain.
func (r *LightningRPCServer) GetChanInfo(ctx context.Context,
	in *rpc_pb.ChanInfoRequest) (*rpc_pb.ChannelEdge, er.R) {

	graph := r.server.localChanDB.ChannelGraph()

	edgeInfo, edge1, edge2, err := graph.FetchChannelEdgesByID(in.ChanId)
	if err != nil {
		return nil, err
	}

	// Convert the database's edge format into the network/RPC edge format
	// which couples the edge itself along with the directional node
	// routing policies of each node involved within the channel.
	channelEdge := marshalDbEdge(edgeInfo, edge1, edge2)

	return channelEdge, nil
}

// GetNodeInfo returns the latest advertised and aggregate authenticated
// channel information for the specified node identified by its public key.
func (r *LightningRPCServer) GetNodeInfo(ctx context.Context,
	in *rpc_pb.NodeInfoRequest) (*rpc_pb.NodeInfo, er.R) {

	graph := r.server.localChanDB.ChannelGraph()

	// First, parse the hex-encoded public key into a full in-memory public
	// key object we can work with for querying.
	pubKey, err := route.NewVertexFromStr(string(in.PubKey))
	if err != nil {
		return nil, err
	}

	// With the public key decoded, attempt to fetch the node corresponding
	// to this public key. If the node cannot be found, then an error will
	// be returned.
	node, err := graph.FetchLightningNode(nil, pubKey)
	if err != nil {
		return nil, err
	}

	// With the node obtained, we'll now iterate through all its out going
	// edges to gather some basic statistics about its out going channels.
	var (
		numChannels   uint32
		totalCapacity btcutil.Amount
		channels      []*rpc_pb.ChannelEdge
	)

	if err := node.ForEachChannel(nil, func(_ kvdb.RTx,
		edge *channeldb.ChannelEdgeInfo,
		c1, c2 *channeldb.ChannelEdgePolicy) er.R {

		numChannels++
		totalCapacity += edge.Capacity

		// Only populate the node's channels if the user requested them.
		if in.IncludeChannels {
			// Do not include unannounced channels - private
			// channels or public channels whose authentication
			// proof were not confirmed yet.
			if edge.AuthProof == nil {
				return nil
			}

			// Convert the database's edge format into the
			// network/RPC edge format.
			channelEdge := marshalDbEdge(edge, c1, c2)
			channels = append(channels, channelEdge)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	nodeAddrs := make([]*rpc_pb.NodeAddress, 0)
	for _, addr := range node.Addresses {
		nodeAddr := &rpc_pb.NodeAddress{
			Network: addr.Network(),
			Addr:    addr.String(),
		}
		nodeAddrs = append(nodeAddrs, nodeAddr)
	}

	features := invoicesrpc.CreateRPCFeatures(node.Features)

	return &rpc_pb.NodeInfo{
		Node: &rpc_pb.LightningNode{
			LastUpdate: uint32(node.LastUpdate.Unix()),
			PubKey:     in.PubKey,
			Addresses:  nodeAddrs,
			Alias:      node.Alias,
			Color:      routing.EncodeHexColor(node.Color),
			Features:   features,
		},
		NumChannels:   numChannels,
		TotalCapacity: int64(totalCapacity),
		Channels:      channels,
	}, nil
}

// QueryRoutes attempts to query the daemons' Channel Router for a possible
// route to a target destination capable of carrying a specific amount of
// satoshis within the route's flow. The retuned route contains the full
// details required to craft and send an HTLC, also including the necessary
// information that should be present within the Sphinx packet encapsulated
// within the HTLC.
//
// TODO(roasbeef): should return a slice of routes in reality
//  * create separate PR to send based on well formatted route
func (r *LightningRPCServer) QueryRoutes(ctx context.Context,
	in *rpc_pb.QueryRoutesRequest) (*rpc_pb.QueryRoutesResponse, er.R) {
	return r.routerBackend.QueryRoutes(ctx, in)
}

// GetNetworkInfo returns some basic stats about the known channel graph from
// the PoV of the node.
func (r *LightningRPCServer) GetNetworkInfo(_ *rpc_pb.Null) (*rpc_pb.NetworkInfo, er.R) {

	graph := r.server.localChanDB.ChannelGraph()

	var (
		numNodes             uint32
		numChannels          uint32
		maxChanOut           uint32
		totalNetworkCapacity btcutil.Amount
		minChannelSize       btcutil.Amount = math.MaxInt64
		maxChannelSize       btcutil.Amount
		medianChanSize       btcutil.Amount
	)

	// We'll use this map to de-duplicate channels during our traversal.
	// This is needed since channels are directional, so there will be two
	// edges for each channel within the graph.
	seenChans := make(map[uint64]struct{})

	// We also keep a list of all encountered capacities, in order to
	// calculate the median channel size.
	var allChans []btcutil.Amount

	// We'll run through all the known nodes in the within our view of the
	// network, tallying up the total number of nodes, and also gathering
	// each node so we can measure the graph diameter and degree stats
	// below.
	if err := graph.ForEachNode(func(tx kvdb.RTx, node *channeldb.LightningNode) er.R {
		// Increment the total number of nodes with each iteration.
		numNodes++

		// For each channel we'll compute the out degree of each node,
		// and also update our running tallies of the min/max channel
		// capacity, as well as the total channel capacity. We pass
		// through the db transaction from the outer view so we can
		// re-use it within this inner view.
		var outDegree uint32
		if err := node.ForEachChannel(tx, func(_ kvdb.RTx,
			edge *channeldb.ChannelEdgeInfo, _, _ *channeldb.ChannelEdgePolicy) er.R {

			// Bump up the out degree for this node for each
			// channel encountered.
			outDegree++

			// If we've already seen this channel, then we'll
			// return early to ensure that we don't double-count
			// stats.
			if _, ok := seenChans[edge.ChannelID]; ok {
				return nil
			}

			// Compare the capacity of this channel against the
			// running min/max to see if we should update the
			// extrema.
			chanCapacity := edge.Capacity
			if chanCapacity < minChannelSize {
				minChannelSize = chanCapacity
			}
			if chanCapacity > maxChannelSize {
				maxChannelSize = chanCapacity
			}

			// Accumulate the total capacity of this channel to the
			// network wide-capacity.
			totalNetworkCapacity += chanCapacity

			numChannels++

			seenChans[edge.ChannelID] = struct{}{}
			allChans = append(allChans, edge.Capacity)
			return nil
		}); err != nil {
			return err
		}

		// Finally, if the out degree of this node is greater than what
		// we've seen so far, update the maxChanOut variable.
		if outDegree > maxChanOut {
			maxChanOut = outDegree
		}

		return nil
	}); err != nil {
		return nil, err
	}

	// Query the graph for the current number of zombie channels.
	numZombies, err := graph.NumZombies()
	if err != nil {
		return nil, err
	}

	// Find the median.
	medianChanSize = autopilot.Median(allChans)

	// If we don't have any channels, then reset the minChannelSize to zero
	// to avoid outputting NaN in encoded JSON.
	if numChannels == 0 {
		minChannelSize = 0
	}

	// TODO(roasbeef): graph diameter

	// TODO(roasbeef): also add oldest channel?
	netInfo := &rpc_pb.NetworkInfo{
		MaxOutDegree:         maxChanOut,
		AvgOutDegree:         float64(2*numChannels) / float64(numNodes),
		NumNodes:             numNodes,
		NumChannels:          numChannels,
		TotalNetworkCapacity: int64(totalNetworkCapacity),
		AvgChannelSize:       float64(totalNetworkCapacity) / float64(numChannels),

		MinChannelSize:       int64(minChannelSize),
		MaxChannelSize:       int64(maxChannelSize),
		MedianChannelSizeSat: int64(medianChanSize),
		NumZombieChans:       numZombies,
	}

	// Similarly, if we don't have any channels, then we'll also set the
	// average channel size to zero in order to avoid weird JSON encoding
	// outputs.
	if numChannels == 0 {
		netInfo.AvgChannelSize = 0
	}

	return netInfo, nil
}

// StopDaemon will send a shutdown request to the interrupt handler, triggering
// a graceful shutdown of the daemon.
func (r *LightningRPCServer) StopDaemon(ctx context.Context, _ *rpc_pb.Null) (*rpc_pb.Null, er.R) {
	os.Exit(0)
	return nil, nil
}

// SubscribeChannelGraph launches a streaming RPC that allows the caller to
// receive notifications upon any changes the channel graph topology from the
// review of the responding node. Events notified include: new nodes coming
// online, nodes updating their authenticated attributes, new channels being
// advertised, updates in the routing policy for a directional channel edge,
// and finally when prior channels are closed on-chain.
func (r *LightningRPCServer) SubscribeChannelGraph(req *rpc_pb.GraphTopologySubscription,
	updateStream rpc_pb.Lightning_SubscribeChannelGraphServer) error {

	// First, we start by subscribing to a new intent to receive
	// notifications from the channel router.
	client, err := r.server.chanRouter.SubscribeTopology()
	if err != nil {
		return er.Native(err)
	}

	// Ensure that the resources for the topology update client is cleaned
	// up once either the server, or client exists.
	defer client.Cancel()

	for {
		select {
		
		// A new update has been sent by the channel router, we'll
		// marshal it into the form expected by the gRPC client, then
		// send it off.
		case topChange, ok := <-client.TopologyChanges:
			// If the second value from the channel read is nil,
			// then this means that the channel router is exiting
			// or the notification client was canceled. So we'll
			// exit early.
			if !ok {
				return er.Native(er.New("server shutting down"))
			}

			// Convert the struct from the channel router into the
			// form expected by the gRPC service then send it off
			// to the client.
			graphUpdate := marshallTopologyChange(topChange)
			if err := updateStream.Send(graphUpdate); err != nil {
				return er.Native(er.E(err))
			}

		// The server is quitting, so we'll exit immediately. Returning
		// nil will close the clients read end of the stream.
		case <-r.quit:
			return nil
		}
	}
}

// marshallTopologyChange performs a mapping from the topology change struct
// returned by the router to the form of notifications expected by the current
// gRPC service.
func marshallTopologyChange(topChange *routing.TopologyChange) *rpc_pb.GraphTopologyUpdate {

	// encodeKey is a simple helper function that converts a live public
	// key into a hex-encoded version of the compressed serialization for
	// the public key.
	encodeKey := func(k *btcec.PublicKey) string {
		return hex.EncodeToString(k.SerializeCompressed())
	}

	nodeUpdates := make([]*rpc_pb.NodeUpdate, len(topChange.NodeUpdates))
	for i, nodeUpdate := range topChange.NodeUpdates {
		addrs := make([]string, len(nodeUpdate.Addresses))
		for i, addr := range nodeUpdate.Addresses {
			addrs[i] = addr.String()
		}

		nodeUpdates[i] = &rpc_pb.NodeUpdate{
			Addresses:      addrs,
			IdentityKey:    encodeKey(nodeUpdate.IdentityKey),
			GlobalFeatures: nodeUpdate.GlobalFeatures,
			Alias:          nodeUpdate.Alias,
			Color:          nodeUpdate.Color,
		}
	}

	channelUpdates := make([]*rpc_pb.ChannelEdgeUpdate, len(topChange.ChannelEdgeUpdates))
	for i, channelUpdate := range topChange.ChannelEdgeUpdates {
		channelUpdates[i] = &rpc_pb.ChannelEdgeUpdate{
			ChanId: channelUpdate.ChanID,
			ChanPoint: &rpc_pb.ChannelPoint{
				FundingTxid: &rpc_pb.ChannelPoint_FundingTxidBytes{
					FundingTxidBytes: channelUpdate.ChanPoint.Hash[:],
				},
				OutputIndex: channelUpdate.ChanPoint.Index,
			},
			Capacity: int64(channelUpdate.Capacity),
			RoutingPolicy: &rpc_pb.RoutingPolicy{
				TimeLockDelta:    uint32(channelUpdate.TimeLockDelta),
				MinHtlc:          int64(channelUpdate.MinHTLC),
				MaxHtlcMsat:      uint64(channelUpdate.MaxHTLC),
				FeeBaseMsat:      int64(channelUpdate.BaseFee),
				FeeRateMilliMsat: int64(channelUpdate.FeeRate),
				Disabled:         channelUpdate.Disabled,
			},
			AdvertisingNode: encodeKey(channelUpdate.AdvertisingNode),
			ConnectingNode:  encodeKey(channelUpdate.ConnectingNode),
		}
	}

	closedChans := make([]*rpc_pb.ClosedChannelUpdate, len(topChange.ClosedChannels))
	for i, closedChan := range topChange.ClosedChannels {
		closedChans[i] = &rpc_pb.ClosedChannelUpdate{
			ChanId:       closedChan.ChanID,
			Capacity:     int64(closedChan.Capacity),
			ClosedHeight: closedChan.ClosedHeight,
			ChanPoint: &rpc_pb.ChannelPoint{
				FundingTxid: &rpc_pb.ChannelPoint_FundingTxidBytes{
					FundingTxidBytes: closedChan.ChanPoint.Hash[:],
				},
				OutputIndex: closedChan.ChanPoint.Index,
			},
		}
	}

	return &rpc_pb.GraphTopologyUpdate{
		NodeUpdates:    nodeUpdates,
		ChannelUpdates: channelUpdates,
		ClosedChans:    closedChans,
	}
}

// ListPayments returns a list of outgoing payments determined by a paginated
// database query.
func (r *LightningRPCServer) ListPayments(ctx context.Context,
	req *rpc_pb.ListPaymentsRequest) (*rpc_pb.ListPaymentsResponse, er.R) {

	log.Debugf("[ListPayments]")

	query := channeldb.PaymentsQuery{
		IndexOffset:       req.IndexOffset,
		MaxPayments:       req.MaxPayments,
		Reversed:          req.Reversed,
		IncludeIncomplete: req.IncludeIncomplete,
	}

	// If the maximum number of payments wasn't specified, then we'll
	// default to return the maximal number of payments representable.
	if req.MaxPayments == 0 {
		query.MaxPayments = math.MaxUint64
	}

	paymentsQuerySlice, err := r.server.remoteChanDB.QueryPayments(query)
	if err != nil {
		return nil, err
	}

	paymentsResp := &rpc_pb.ListPaymentsResponse{
		LastIndexOffset:  paymentsQuerySlice.LastIndexOffset,
		FirstIndexOffset: paymentsQuerySlice.FirstIndexOffset,
	}

	for _, payment := range paymentsQuerySlice.Payments {
		payment := payment

		rpcPayment, err := r.routerBackend.MarshallPayment(payment)
		if err != nil {
			return nil, err
		}

		paymentsResp.Payments = append(
			paymentsResp.Payments, rpcPayment,
		)
	}

	return paymentsResp, nil
}

// DeleteAllPayments deletes all outgoing payments from DB.
func (r *LightningRPCServer) DeleteAllPayments(ctx context.Context,
	_ *rpc_pb.DeleteAllPaymentsRequest) (*rpc_pb.DeleteAllPaymentsResponse, error) {

	log.Debugf("[DeleteAllPayments]")

	if err := r.server.remoteChanDB.DeletePayments(); err != nil {
		return nil, er.Native(err)
	}

	return &rpc_pb.DeleteAllPaymentsResponse{}, nil
}

// DebugLevel allows a caller to programmatically set the logging verbosity of
// lnd. The logging can be targeted according to a coarse daemon-wide logging
// level, or in a granular fashion to specify the logging for a target
// sub-system.
func (r *LightningRPCServer) DebugLevel(ctx context.Context,
	req *rpc_pb.DebugLevelRequest) (*rpc_pb.Null, er.R) {

	log.Infof("[debuglevel] changing debug level to: %v", req.LevelSpec)

	// Otherwise, we'll attempt to set the logging level using the
	// specified level spec.
	err := log.SetLogLevels(req.LevelSpec)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

// DecodePayReq takes an encoded payment request string and attempts to decode
// it, returning a full description of the conditions encoded within the
// payment request.
func (r *LightningRPCServer) DecodePayReq(ctx context.Context,
	req *rpc_pb.PayReqString) (*rpc_pb.PayReq, er.R) {

	log.Tracef("[decodepayreq] decoding: %v", req.PayReq)

	// Fist we'll attempt to decode the payment request string, if the
	// request is invalid or the checksum doesn't match, then we'll exit
	// here with an error.
	payReq, err := zpay32.Decode(req.PayReq, r.cfg.ActiveNetParams.Params)
	if err != nil {
		return nil, err
	}

	// Let the fields default to empty strings.
	desc := ""
	if payReq.Description != nil {
		desc = *payReq.Description
	}

	descHash := []byte("")
	if payReq.DescriptionHash != nil {
		descHash = payReq.DescriptionHash[:]
	}

	fallbackAddr := ""
	if payReq.FallbackAddr != nil {
		fallbackAddr = payReq.FallbackAddr.String()
	}

	// Expiry time will default to 3600 seconds if not specified
	// explicitly.
	expiry := int64(payReq.Expiry().Seconds())

	// Convert between the `lnrpc` and `routing` types.
	routeHints := invoicesrpc.CreateRPCRouteHints(payReq.RouteHints)

	var amtSat, amtMsat int64
	if payReq.MilliSat != nil {
		amtSat = int64(payReq.MilliSat.ToSatoshis())
		amtMsat = int64(*payReq.MilliSat)
	}

	// Extract the payment address from the payment request, if present.
	var paymentAddr []byte
	if payReq.PaymentAddr != nil {
		paymentAddr = payReq.PaymentAddr[:]
	}

	dest := payReq.Destination.SerializeCompressed()
	return &rpc_pb.PayReq{
		Destination:     dest,
		PaymentHash:     payReq.PaymentHash[:],
		NumSatoshis:     amtSat,
		NumMsat:         amtMsat,
		Timestamp:       payReq.Timestamp.Unix(),
		Description:     desc,
		DescriptionHash: descHash[:],
		FallbackAddr:    fallbackAddr,
		Expiry:          expiry,
		CltvExpiry:      int64(payReq.MinFinalCLTVExpiry()),
		RouteHints:      routeHints,
		PaymentAddr:     paymentAddr,
		Features:        invoicesrpc.CreateRPCFeatures(payReq.Features),
	}, nil
}

// feeBase is the fixed point that fee rate computation are performed over.
// Nodes on the network advertise their fee rate using this point as a base.
// This means that the minimal possible fee rate if 1e-6, or 0.000001, or
// 0.0001%.
const feeBase = 1000000

// FeeReport allows the caller to obtain a report detailing the current fee
// schedule enforced by the node globally for each channel.
func (r *LightningRPCServer) FeeReport(ctx context.Context,
	_ *rpc_pb.Null) (*rpc_pb.FeeReportResponse, er.R) {

	// TODO(roasbeef): use UnaryInterceptor to add automated logging

	log.Debugf("[feereport]")

	channelGraph := r.server.localChanDB.ChannelGraph()
	selfNode, err := channelGraph.SourceNode()
	if err != nil {
		return nil, err
	}

	var feeReports []*rpc_pb.ChannelFeeReport
	err = selfNode.ForEachChannel(nil, func(_ kvdb.RTx, chanInfo *channeldb.ChannelEdgeInfo,
		edgePolicy, _ *channeldb.ChannelEdgePolicy) er.R {

		// Self node should always have policies for its channels.
		if edgePolicy == nil {
			return er.Errorf("no policy for outgoing channel %v ",
				chanInfo.ChannelID)
		}

		// We'll compute the effective fee rate by converting from a
		// fixed point fee rate to a floating point fee rate. The fee
		// rate field in the database the amount of mSAT charged per
		// 1mil mSAT sent, so will divide by this to get the proper fee
		// rate.
		feeRateFixedPoint := edgePolicy.FeeProportionalMillionths
		feeRate := float64(feeRateFixedPoint) / float64(feeBase)

		// TODO(roasbeef): also add stats for revenue for each channel
		feeReports = append(feeReports, &rpc_pb.ChannelFeeReport{
			ChanId:       chanInfo.ChannelID,
			ChannelPoint: chanInfo.ChannelPoint.String(),
			BaseFeeMsat:  int64(edgePolicy.FeeBaseMSat),
			FeePerMil:    int64(feeRateFixedPoint),
			FeeRate:      feeRate,
		})

		return nil
	})
	if err != nil {
		return nil, err
	}

	fwdEventLog := r.server.remoteChanDB.ForwardingLog()

	// computeFeeSum is a helper function that computes the total fees for
	// a particular time slice described by a forwarding event query.
	computeFeeSum := func(query channeldb.ForwardingEventQuery) (lnwire.MilliSatoshi, er.R) {

		var totalFees lnwire.MilliSatoshi

		// We'll continue to fetch the next query and accumulate the
		// fees until the next query returns no events.
		for {
			timeSlice, err := fwdEventLog.Query(query)
			if err != nil {
				return 0, err
			}

			// If the timeslice is empty, then we'll return as
			// we've retrieved all the entries in this range.
			if len(timeSlice.ForwardingEvents) == 0 {
				break
			}

			// Otherwise, we'll tally up an accumulate the total
			// fees for this time slice.
			for _, event := range timeSlice.ForwardingEvents {
				fee := event.AmtIn - event.AmtOut
				totalFees += fee
			}

			// We'll now take the last offset index returned as
			// part of this response, and modify our query to start
			// at this index. This has a pagination effect in the
			// case that our query bounds has more than 100k
			// entries.
			query.IndexOffset = timeSlice.LastIndexOffset
		}

		return totalFees, nil
	}

	now := time.Now()

	// Before we perform the queries below, we'll instruct the switch to
	// flush any pending events to disk. This ensure we get a complete
	// snapshot at this particular time.
	if err := r.server.htlcSwitch.FlushForwardingEvents(); err != nil {
		return nil, er.Errorf("unable to flush forwarding "+
			"events: %v", err)
	}

	// In addition to returning the current fee schedule for each channel.
	// We'll also perform a series of queries to obtain the total fees
	// earned over the past day, week, and month.
	dayQuery := channeldb.ForwardingEventQuery{
		StartTime:    now.Add(-time.Hour * 24),
		EndTime:      now,
		NumMaxEvents: 1000,
	}
	dayFees, err := computeFeeSum(dayQuery)
	if err != nil {
		return nil, er.Errorf("unable to retrieve day fees: %v", err)
	}

	weekQuery := channeldb.ForwardingEventQuery{
		StartTime:    now.Add(-time.Hour * 24 * 7),
		EndTime:      now,
		NumMaxEvents: 1000,
	}
	weekFees, err := computeFeeSum(weekQuery)
	if err != nil {
		return nil, er.Errorf("unable to retrieve day fees: %v", err)
	}

	monthQuery := channeldb.ForwardingEventQuery{
		StartTime:    now.Add(-time.Hour * 24 * 30),
		EndTime:      now,
		NumMaxEvents: 1000,
	}
	monthFees, err := computeFeeSum(monthQuery)
	if err != nil {
		return nil, er.Errorf("unable to retrieve day fees: %v", err)
	}

	return &rpc_pb.FeeReportResponse{
		ChannelFees: feeReports,
		DayFeeSum:   uint64(dayFees.ToSatoshis()),
		WeekFeeSum:  uint64(weekFees.ToSatoshis()),
		MonthFeeSum: uint64(monthFees.ToSatoshis()),
	}, nil
}

// minFeeRate is the smallest permitted fee rate within the network. This is
// derived by the fact that fee rates are computed using a fixed point of
// 1,000,000. As a result, the smallest representable fee rate is 1e-6, or
// 0.000001, or 0.0001%.
const minFeeRate = 1e-6

// UpdateChannelPolicy allows the caller to update the channel forwarding policy
// for all channels globally, or a particular channel.
func (r *LightningRPCServer) UpdateChannelPolicy(ctx context.Context,
	req *rpc_pb.PolicyUpdateRequest) (*rpc_pb.PolicyUpdateResponse, er.R) {

	var targetChans []wire.OutPoint
	switch scope := req.Scope.(type) {
	// If the request is targeting all active channels, then we don't need
	// target any channels by their channel point.
	case *rpc_pb.PolicyUpdateRequest_Global:

	// Otherwise, we're targeting an individual channel by its channel
	// point.
	case *rpc_pb.PolicyUpdateRequest_ChanPoint:
		txid, err := GetChanPointFundingTxid(scope.ChanPoint)
		if err != nil {
			return nil, err
		}
		targetChans = append(targetChans, wire.OutPoint{
			Hash:  *txid,
			Index: scope.ChanPoint.OutputIndex,
		})
	default:
		return nil, er.Errorf("unknown scope: %v", scope)
	}

	switch {
	// As a sanity check, if the fee isn't zero, we'll ensure that the
	// passed fee rate is below 1e-6, or the lowest allowed non-zero fee
	// rate expressible within the protocol.
	case req.FeeRate != 0 && req.FeeRate < minFeeRate:
		return nil, er.Errorf("fee rate of %v is too small, min fee "+
			"rate is %v", req.FeeRate, minFeeRate)

	// We'll also ensure that the user isn't setting a CLTV delta that
	// won't give outgoing HTLCs enough time to fully resolve if needed.
	case req.TimeLockDelta < minTimeLockDelta:
		return nil, er.Errorf("time lock delta of %v is too small, "+
			"minimum supported is %v", req.TimeLockDelta,
			minTimeLockDelta)
	}

	// We'll also need to convert the floating point fee rate we accept
	// over RPC to the fixed point rate that we use within the protocol. We
	// do this by multiplying the passed fee rate by the fee base. This
	// gives us the fixed point, scaled by 1 million that's used within the
	// protocol.
	feeRateFixed := uint32(req.FeeRate * feeBase)
	baseFeeMsat := lnwire.MilliSatoshi(req.BaseFeeMsat)
	feeSchema := routing.FeeSchema{
		BaseFee: baseFeeMsat,
		FeeRate: feeRateFixed,
	}

	maxHtlc := lnwire.MilliSatoshi(req.MaxHtlcMsat)
	var minHtlc *lnwire.MilliSatoshi
	if req.MinHtlcMsatSpecified {
		min := lnwire.MilliSatoshi(req.MinHtlcMsat)
		minHtlc = &min
	}

	chanPolicy := routing.ChannelPolicy{
		FeeSchema:     feeSchema,
		TimeLockDelta: req.TimeLockDelta,
		MaxHTLC:       maxHtlc,
		MinHTLC:       minHtlc,
	}

	log.Debugf("[updatechanpolicy] updating channel policy base_fee=%v, "+
		"rate_float=%v, rate_fixed=%v, time_lock_delta: %v, "+
		"min_htlc=%v, max_htlc=%v, targets=%v",
		req.BaseFeeMsat, req.FeeRate, feeRateFixed, req.TimeLockDelta,
		minHtlc, maxHtlc,
		spew.Sdump(targetChans))

	// With the scope resolved, we'll now send this to the local channel
	// manager so it can propagate the new policy for our target channel(s).
	err := r.server.localChanMgr.UpdatePolicy(chanPolicy, targetChans...)
	if err != nil {
		return nil, err
	}

	return &rpc_pb.PolicyUpdateResponse{}, nil
}

// ForwardingHistory allows the caller to query the htlcswitch for a record of
// all HTLC's forwarded within the target time range, and integer offset within
// that time range. If no time-range is specified, then the first chunk of the
// past 24 hrs of forwarding history are returned.

// A list of forwarding events are returned. The size of each forwarding event
// is 40 bytes, and the max message size able to be returned in gRPC is 4 MiB.
// In order to safely stay under this max limit, we'll return 50k events per
// response.  Each response has the index offset of the last entry. The index
// offset can be provided to the request to allow the caller to skip a series
// of records.
func (r *LightningRPCServer) ForwardingHistory(ctx context.Context,
	req *rpc_pb.ForwardingHistoryRequest) (*rpc_pb.ForwardingHistoryResponse, er.R) {

	log.Debugf("[forwardinghistory]")

	// Before we perform the queries below, we'll instruct the switch to
	// flush any pending events to disk. This ensure we get a complete
	// snapshot at this particular time.
	if err := r.server.htlcSwitch.FlushForwardingEvents(); err != nil {
		return nil, er.Errorf("unable to flush forwarding "+
			"events: %v", err)
	}

	var (
		startTime, endTime time.Time

		numEvents uint32
	)

	// startTime defaults to the Unix epoch (0 unixtime, or midnight 01-01-1970).
	startTime = time.Unix(int64(req.StartTime), 0)

	// If the end time wasn't specified, assume a default end time of now.
	if req.EndTime == 0 {
		now := time.Now()
		endTime = now
	} else {
		endTime = time.Unix(int64(req.EndTime), 0)
	}

	// If the number of events wasn't specified, then we'll default to
	// returning the last 100 events.
	numEvents = req.NumMaxEvents
	if numEvents == 0 {
		numEvents = 100
	}

	// Next, we'll map the proto request into a format that is understood by
	// the forwarding log.
	eventQuery := channeldb.ForwardingEventQuery{
		StartTime:    startTime,
		EndTime:      endTime,
		IndexOffset:  req.IndexOffset,
		NumMaxEvents: numEvents,
	}
	timeSlice, err := r.server.remoteChanDB.ForwardingLog().Query(eventQuery)
	if err != nil {
		return nil, er.Errorf("unable to query forwarding log: %v", err)
	}

	// TODO(roasbeef): add settlement latency?
	//  * use FPE on all records?

	// With the events retrieved, we'll now map them into the proper proto
	// response.
	//
	// TODO(roasbeef): show in ns for the outside?
	resp := &rpc_pb.ForwardingHistoryResponse{
		ForwardingEvents: make([]*rpc_pb.ForwardingEvent, len(timeSlice.ForwardingEvents)),
		LastOffsetIndex:  timeSlice.LastIndexOffset,
	}
	for i, event := range timeSlice.ForwardingEvents {
		amtInMsat := event.AmtIn
		amtOutMsat := event.AmtOut
		feeMsat := event.AmtIn - event.AmtOut

		resp.ForwardingEvents[i] = &rpc_pb.ForwardingEvent{
			Timestamp:  uint64(event.Timestamp.Unix()),
			ChanIdIn:   event.IncomingChanID.ToUint64(),
			ChanIdOut:  event.OutgoingChanID.ToUint64(),
			AmtIn:      uint64(amtInMsat.ToSatoshis()),
			AmtOut:     uint64(amtOutMsat.ToSatoshis()),
			Fee:        uint64(feeMsat.ToSatoshis()),
			FeeMsat:    uint64(feeMsat),
			AmtInMsat:  uint64(amtInMsat),
			AmtOutMsat: uint64(amtOutMsat),
		}
	}

	return resp, nil
}

// ExportChannelBackup attempts to return an encrypted static channel backup
// for the target channel identified by it channel point. The backup is
// encrypted with a key generated from the aezeed seed of the user. The
// returned backup can either be restored using the RestoreChannelBackup method
// once lnd is running, or via the InitWallet and UnlockWallet methods from the
// WalletUnlocker service.
func (r *LightningRPCServer) ExportChannelBackup(ctx context.Context,
	in *rpc_pb.ExportChannelBackupRequest) (*rpc_pb.ChannelBackup, er.R) {

	// First, we'll convert the lnrpc channel point into a wire.OutPoint
	// that we can manipulate.
	txid, err := GetChanPointFundingTxid(in.ChanPoint)
	if err != nil {
		return nil, err
	}
	chanPoint := wire.OutPoint{
		Hash:  *txid,
		Index: in.ChanPoint.OutputIndex,
	}

	// Next, we'll attempt to fetch a channel backup for this channel from
	// the database. If this channel has been closed, or the outpoint is
	// unknown, then we'll return an error
	unpackedBackup, err := chanbackup.FetchBackupForChan(
		chanPoint, r.server.remoteChanDB,
	)
	if err != nil {
		return nil, err
	}

	// At this point, we have an unpacked backup (plaintext) so we'll now
	// attempt to serialize and encrypt it in order to create a packed
	// backup.
	packedBackups, err := chanbackup.PackStaticChanBackups(
		[]chanbackup.Single{*unpackedBackup},
		r.server.cc.KeyRing,
	)
	if err != nil {
		return nil, er.Errorf("packing of back ups failed: %v", err)
	}

	// Before we proceed, we'll ensure that we received a backup for this
	// channel, otherwise, we'll bail out.
	packedBackup, ok := packedBackups[chanPoint]
	if !ok {
		return nil, er.Errorf("expected single backup for "+
			"ChannelPoint(%v), got %v", chanPoint,
			len(packedBackup))
	}

	return &rpc_pb.ChannelBackup{
		ChanPoint:  in.ChanPoint,
		ChanBackup: packedBackup,
	}, nil
}

// VerifyChanBackup allows a caller to verify the integrity of a channel backup
// snapshot. This method will accept both either a packed Single or a packed
// Multi. Specifying both will result in an error.
func (r *LightningRPCServer) VerifyChanBackup(ctx context.Context,
	in *rpc_pb.ChanBackupSnapshot) (*rpc_pb.VerifyChanBackupResponse, er.R) {

	switch {
	// If neither a Single or Multi has been specified, then we have nothing
	// to verify.
	case in.GetSingleChanBackups() == nil && in.GetMultiChanBackup() == nil:
		return nil, er.New("either a Single or Multi channel " +
			"backup must be specified")

	// Either a Single or a Multi must be specified, but not both.
	case in.GetSingleChanBackups() != nil && in.GetMultiChanBackup() != nil:
		return nil, er.New("either a Single or Multi channel " +
			"backup must be specified, but not both")

	// If a Single is specified then we'll only accept one of them to allow
	// the caller to map the valid/invalid state for each individual Single.
	case in.GetSingleChanBackups() != nil:
		chanBackupsProtos := in.GetSingleChanBackups().ChanBackups
		if len(chanBackupsProtos) != 1 {
			return nil, er.New("only one Single is accepted " +
				"at a time")
		}

		// First, we'll convert the raw byte slice into a type we can
		// work with a bit better.
		chanBackup := chanbackup.PackedSingles(
			[][]byte{chanBackupsProtos[0].ChanBackup},
		)

		// With our PackedSingles created, we'll attempt to unpack the
		// backup. If this fails, then we know the backup is invalid for
		// some reason.
		_, err := chanBackup.Unpack(r.server.cc.KeyRing)
		if err != nil {
			return nil, er.Errorf("invalid single channel "+
				"backup: %v", err)
		}

	case in.GetMultiChanBackup() != nil:
		// We'll convert the raw byte slice into a PackedMulti that we
		// can easily work with.
		packedMultiBackup := in.GetMultiChanBackup().MultiChanBackup
		packedMulti := chanbackup.PackedMulti(packedMultiBackup)

		// We'll now attempt to unpack the Multi. If this fails, then we
		// know it's invalid.
		_, err := packedMulti.Unpack(r.server.cc.KeyRing)
		if err != nil {
			return nil, er.Errorf("invalid multi channel backup: "+
				"%v", err)
		}
	}

	return &rpc_pb.VerifyChanBackupResponse{}, nil
}

// createBackupSnapshot converts the passed Single backup into a snapshot which
// contains individual packed single backups, as well as a single packed multi
// backup.
func (r *LightningRPCServer) createBackupSnapshot(backups []chanbackup.Single) (
	*rpc_pb.ChanBackupSnapshot, er.R) {

	// Once we have the set of back ups, we'll attempt to pack them all
	// into a series of single channel backups.
	singleChanPackedBackups, err := chanbackup.PackStaticChanBackups(
		backups, r.server.cc.KeyRing,
	)
	if err != nil {
		return nil, er.Errorf("unable to pack set of chan "+
			"backups: %v", err)
	}

	// Now that we have our set of single packed backups, we'll morph that
	// into a form that the proto response requires.
	numBackups := len(singleChanPackedBackups)
	singleBackupResp := &rpc_pb.ChannelBackups{
		ChanBackups: make([]*rpc_pb.ChannelBackup, 0, numBackups),
	}
	for chanPoint, singlePackedBackup := range singleChanPackedBackups {
		txid := chanPoint.Hash
		rpcChanPoint := &rpc_pb.ChannelPoint{
			FundingTxid: &rpc_pb.ChannelPoint_FundingTxidBytes{
				FundingTxidBytes: txid[:],
			},
			OutputIndex: chanPoint.Index,
		}

		singleBackupResp.ChanBackups = append(
			singleBackupResp.ChanBackups,
			&rpc_pb.ChannelBackup{
				ChanPoint:  rpcChanPoint,
				ChanBackup: singlePackedBackup,
			},
		)
	}

	// In addition, to the set of single chan backups, we'll also create a
	// single multi-channel backup which can be serialized into a single
	// file for safe storage.
	var b bytes.Buffer
	unpackedMultiBackup := chanbackup.Multi{
		StaticBackups: backups,
	}
	err = unpackedMultiBackup.PackToWriter(&b, r.server.cc.KeyRing)
	if err != nil {
		return nil, er.Errorf("unable to multi-pack backups: %v", err)
	}

	multiBackupResp := &rpc_pb.MultiChanBackup{
		MultiChanBackup: b.Bytes(),
	}
	for _, singleBackup := range singleBackupResp.ChanBackups {
		multiBackupResp.ChanPoints = append(
			multiBackupResp.ChanPoints, singleBackup.ChanPoint,
		)
	}

	return &rpc_pb.ChanBackupSnapshot{
		SingleChanBackups: singleBackupResp,
		MultiChanBackup:   multiBackupResp,
	}, nil
}

// ExportAllChannelBackups returns static channel backups for all existing
// channels known to lnd. A set of regular singular static channel backups for
// each channel are returned. Additionally, a multi-channel backup is returned
// as well, which contains a single encrypted blob containing the backups of
// each channel.
func (r *LightningRPCServer) ExportAllChannelBackups(ctx context.Context,
	in *rpc_pb.ChanBackupExportRequest) (*rpc_pb.ChanBackupSnapshot, error) {

	// First, we'll attempt to read back ups for ALL currently opened
	// channels from disk.
	allUnpackedBackups, err := chanbackup.FetchStaticChanBackups(
		r.server.remoteChanDB,
	)
	if err != nil {
		return nil, er.Native(er.Errorf("unable to fetch all static chan "+
			"backups: %v", err))
	}

	// With the backups assembled, we'll create a full snapshot.
	res, err := r.createBackupSnapshot(allUnpackedBackups)
	return res, er.Native(err)
}

// RestoreChannelBackups accepts a set of singular channel backups, or a single
// encrypted multi-chan backup and attempts to recover any funds remaining
// within the channel. If we're able to unpack the backup, then the new channel
// will be shown under listchannels, as well as pending channels.
func (r *LightningRPCServer) RestoreChannelBackups(ctx context.Context,
	in *rpc_pb.RestoreChanBackupRequest) (*rpc_pb.RestoreBackupResponse, er.R) {

	// First, we'll make our implementation of the
	// chanbackup.ChannelRestorer interface which we'll use to properly
	// restore either a set of chanbackup.Single or chanbackup.Multi
	// backups.
	chanRestorer := &chanDBRestorer{
		db:         r.server.remoteChanDB,
		secretKeys: r.server.cc.KeyRing,
		chainArb:   r.server.chainArb,
	}

	// We'll accept either a list of Single backups, or a single Multi
	// backup which contains several single backups.
	switch {
	case in.GetChanBackups() != nil:
		chanBackupsProtos := in.GetChanBackups()

		// Now that we know what type of backup we're working with,
		// we'll parse them all out into a more suitable format.
		packedBackups := make([][]byte, 0, len(chanBackupsProtos.ChanBackups))
		for _, chanBackup := range chanBackupsProtos.ChanBackups {
			packedBackups = append(
				packedBackups, chanBackup.ChanBackup,
			)
		}

		// With our backups obtained, we'll now restore them which will
		// write the new backups to disk, and then attempt to connect
		// out to any peers that we know of which were our prior
		// channel peers.
		err := chanbackup.UnpackAndRecoverSingles(
			chanbackup.PackedSingles(packedBackups),
			r.server.cc.KeyRing, chanRestorer, r.server,
		)
		if err != nil {
			return nil, er.Errorf("unable to unpack single "+
				"backups: %v", err)
		}

	case in.GetMultiChanBackup() != nil:
		packedMultiBackup := in.GetMultiChanBackup()

		// With our backups obtained, we'll now restore them which will
		// write the new backups to disk, and then attempt to connect
		// out to any peers that we know of which were our prior
		// channel peers.
		packedMulti := chanbackup.PackedMulti(packedMultiBackup)
		err := chanbackup.UnpackAndRecoverMulti(
			packedMulti, r.server.cc.KeyRing, chanRestorer,
			r.server,
		)
		if err != nil {
			return nil, er.Errorf("unable to unpack chan "+
				"backup: %v", err)
		}
	}

	return &rpc_pb.RestoreBackupResponse{}, nil
}

// SubscribeChannelBackups allows a client to sub-subscribe to the most up to
// date information concerning the state of all channel back ups. Each time a
// new channel is added, we return the new set of channels, along with a
// multi-chan backup containing the backup info for all channels. Each time a
// channel is closed, we send a new update, which contains new new chan back
// ups, but the updated set of encrypted multi-chan backups with the closed
// channel(s) removed.
func (r *LightningRPCServer) SubscribeChannelBackups(req *rpc_pb.ChannelBackupSubscription,
	updateStream rpc_pb.Lightning_SubscribeChannelBackupsServer) er.R {

	// First, we'll subscribe to the primary channel notifier so we can
	// obtain events for new pending/opened/closed channels.
	chanSubscription, err := r.server.channelNotifier.SubscribeChannelEvents()
	if err != nil {
		return err
	}

	defer chanSubscription.Cancel()
	for {
		select {
		// A new event has been sent by the channel notifier, we'll
		// assemble, then sling out a new event to the client.
		case e := <-chanSubscription.Updates():
			// TODO(roasbeef): batch dispatch ntnfs

			switch e.(type) {

			// We only care about new/closed channels, so we'll
			// skip any events for active/inactive channels.
			// To make the subscription behave the same way as the
			// synchronous call and the file based backup, we also
			// include pending channels in the update.
			case channelnotifier.ActiveChannelEvent:
				continue
			case channelnotifier.InactiveChannelEvent:
				continue
			case channelnotifier.ActiveLinkEvent:
				continue
			}

			// Now that we know the channel state has changed,
			// we'll obtains the current set of single channel
			// backups from disk.
			chanBackups, err := chanbackup.FetchStaticChanBackups(
				r.server.remoteChanDB,
			)
			if err != nil {
				return er.Errorf("unable to fetch all "+
					"static chan backups: %v", err)
			}

			// With our backups obtained, we'll pack them into a
			// snapshot and send them back to the client.
			backupSnapshot, err := r.createBackupSnapshot(
				chanBackups,
			)
			if err != nil {
				return err
			}
			errr := updateStream.Send(backupSnapshot)
			if errr != nil {
				return er.E(errr)
			}

		case <-r.quit:
			return nil
		}
	}
}

func (r *LightningRPCServer) FundingStateStep(ctx context.Context,
	in *rpc_pb.FundingTransitionMsg) (*rpc_pb.FundingStateStepResp, error) {
	res, err := r.FundingStateStep0(ctx, in)
	return res, er.Native(err)
}

// FundingStateStep is an advanced funding related call that allows the caller
// to either execute some preparatory steps for a funding workflow, or manually
// progress a funding workflow. The primary way a funding flow is identified is
// via its pending channel ID. As an example, this method can be used to
// specify that we're expecting a funding flow for a particular pending channel
// ID, for which we need to use specific parameters.  Alternatively, this can
// be used to interactively drive PSBT signing for funding for partially
// complete funding transactions.
func (r *LightningRPCServer) FundingStateStep0(ctx context.Context,
	in *rpc_pb.FundingTransitionMsg) (*rpc_pb.FundingStateStepResp, er.R) {

	var pendingChanID [32]byte
	switch {

	// If this is a message to register a new shim that is an external
	// channel point, then we'll contact the wallet to register this new
	// shim. A user will use this method to register a new channel funding
	// workflow which has already been partially negotiated outside of the
	// core protocol.
	case in.GetShimRegister() != nil &&
		in.GetShimRegister().GetChanPointShim() != nil:

		rpcShimIntent := in.GetShimRegister().GetChanPointShim()

		// Using the rpc shim as a template, we'll construct a new
		// chanfunding.Assembler that is able to express proper
		// formulation of this expected channel.
		shimAssembler, err := newFundingShimAssembler(
			rpcShimIntent, false, r.server.cc.KeyRing,
		)
		if err != nil {
			return nil, err
		}
		req := &chanfunding.Request{
			RemoteAmt: btcutil.Amount(rpcShimIntent.Amt),
		}
		shimIntent, err := shimAssembler.ProvisionChannel(req)
		if err != nil {
			return nil, err
		}

		// Once we have the intent, we'll register it with the wallet.
		// Once we receive an incoming funding request that uses this
		// pending channel ID, then this shim will be dispatched in
		// place of our regular funding workflow.
		copy(pendingChanID[:], rpcShimIntent.PendingChanId)
		err = r.server.cc.Wallet.RegisterFundingIntent(
			pendingChanID, shimIntent,
		)
		if err != nil {
			return nil, err
		}

	// There is no need to register a PSBT shim before opening the channel,
	// even though our RPC message structure allows for it. Inform the user
	// by returning a proper error instead of just doing nothing.
	case in.GetShimRegister() != nil &&
		in.GetShimRegister().GetPsbtShim() != nil:

		return nil, er.Errorf("PSBT shim must only be sent when " +
			"opening a channel")

	// If this is a transition to cancel an existing shim, then we'll pass
	// this message along to the wallet, informing it that the intent no
	// longer needs to be considered and should be cleaned up.
	case in.GetShimCancel() != nil:
		log.Debugf("Canceling funding shim for pending_id=%x",
			in.GetShimCancel().PendingChanId)

		copy(pendingChanID[:], in.GetShimCancel().PendingChanId)
		err := r.server.cc.Wallet.CancelFundingIntent(pendingChanID)
		if err != nil {
			return nil, err
		}

	// If this is a transition to verify the PSBT for an existing shim,
	// we'll do so and then store the verified PSBT for later so we can
	// compare it to the final, signed one.
	case in.GetPsbtVerify() != nil:
		log.Debugf("Verifying PSBT for pending_id=%x",
			in.GetPsbtVerify().PendingChanId)

		copy(pendingChanID[:], in.GetPsbtVerify().PendingChanId)
		packet, err := psbt.NewFromRawBytes(
			bytes.NewReader(in.GetPsbtVerify().FundedPsbt), false,
		)
		if err != nil {
			return nil, er.Errorf("error parsing psbt: %v", err)
		}

		errr := r.server.cc.Wallet.PsbtFundingVerify(
			pendingChanID, packet,
		)
		if errr != nil {
			return nil, errr
		}

	// If this is a transition to finalize the PSBT funding flow, we compare
	// the final PSBT to the previously verified one and if nothing
	// unexpected was changed, continue the channel opening process.
	case in.GetPsbtFinalize() != nil:
		msg := in.GetPsbtFinalize()
		log.Debugf("Finalizing PSBT for pending_id=%x",
			msg.PendingChanId)

		copy(pendingChanID[:], in.GetPsbtFinalize().PendingChanId)

		var (
			packet *psbt.Packet
			rawTx  *wire.MsgTx
			err    er.R
		)

		// Either the signed PSBT or the raw transaction need to be set
		// but not both at the same time.
		switch {
		case len(msg.SignedPsbt) > 0 && len(msg.FinalRawTx) > 0:
			return nil, er.Errorf("cannot set both signed PSBT " +
				"and final raw TX at the same time")

		case len(msg.SignedPsbt) > 0:
			packet, err = psbt.NewFromRawBytes(
				bytes.NewReader(in.GetPsbtFinalize().SignedPsbt),
				false,
			)
			if err != nil {
				return nil, er.Errorf("error parsing psbt: %v",
					err)
			}

		case len(msg.FinalRawTx) > 0:
			rawTx = &wire.MsgTx{}
			err = rawTx.Deserialize(bytes.NewReader(msg.FinalRawTx))
			if err != nil {
				return nil, er.Errorf("error parsing final "+
					"raw TX: %v", err)
			}

		default:
			return nil, er.Errorf("PSBT or raw transaction to " +
				"finalize missing")
		}

		err = r.server.cc.Wallet.PsbtFundingFinalize(
			pendingChanID, packet, rawTx,
		)
		if err != nil {
			return nil, err
		}
	}

	// TODO(roasbeef): extend PendingChannels to also show shims

	// TODO(roasbeef): return resulting state? also add a method to query
	// current state?
	return &rpc_pb.FundingStateStepResp{}, nil
}

// Resync
func (r *LightningRPCServer) ReSync(ctx context.Context, req *rpc_pb.ReSyncChainRequest) (*rpc_pb.Null, er.R) {
	fh := req.FromHeight
	if req.FromHeight == 0 {
		fh = -1
	}
	th := req.ToHeight
	if req.ToHeight == 0 {
		th = -1
	}
	var a []string
	if req.Addresses != nil {
		a = req.Addresses
	}
	drop := req.DropDb
	err := r.wallet.ResyncChain(fh, th, a, drop)
	return nil, err
}

// StopResync
func (r *LightningRPCServer) StopReSync(ctx context.Context, req *rpc_pb.Null) (*rpc_pb.Null, er.R) {
	_, err := r.wallet.StopResync()
	return nil, err
}

//	ChangeSeedPassphrase
func (r *LightningRPCServer) ChangeSeedPassphrase(ctx context.Context, req *rpc_pb.ChangeSeedPassphraseRequest) (*rpc_pb.ChangeSeedPassphraseResponse, er.R) {

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

//ImportPrivKey
func (r *LightningRPCServer) ImportPrivKey(ctx context.Context, req *rpc_pb.ImportPrivKeyRequest) (*rpc_pb.ImportPrivKeyResponse, er.R) {
	wif, err := btcutil.DecodeWIF(req.PrivateKey)
	if err != nil {
		return nil, err
	}
	w := r.wallet
	if !wif.IsForNet(w.ChainParams()) {
		// If the wif is for the wrong chain, lets attempt to import it anyway
		var err er.R
		wif, err = btcutil.NewWIF(wif.PrivKey, w.ChainParams(), wif.CompressPubKey)
		if err != nil {
			return nil, err
		}
	}

	scope := waddrmgr.KeyScopeBIP0084
	if req.Legacy {
		scope = waddrmgr.KeyScopeBIP0044
	}

	// Import the private key, handling any errors.
	addr, err := w.ImportPrivateKey(scope, wif, nil, req.Rescan)
	switch {
	case waddrmgr.ErrLocked.Is(err):
		return nil, er.New("ErrRPCWalletUnlockNeeded: -13 Enter the wallet passphrase with walletpassphrase first")
	}

	return &rpc_pb.ImportPrivKeyResponse{
		Address: addr,
	}, err
}

//ListLockUnspent
func (r *LightningRPCServer) ListLockUnspent(ctx context.Context, _ *rpc_pb.Null) (*rpc_pb.ListLockUnspentResponse, er.R) {
	list := r.wallet.LockedOutpoints()
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

//LockUnspent
func (r *LightningRPCServer) LockUnspent(ctx context.Context, req *rpc_pb.LockUnspentRequest) (*rpc_pb.Null, er.R) {
	w := r.wallet
	lockname := "none"
	if req.Lockname != "" {
		lockname = req.Lockname
	}
	transactions := req.Transactions
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

func (r *LightningRPCServer) UnlockUnspent(ctx context.Context, req *rpc_pb.LockUnspentRequest) (*rpc_pb.Null, er.R) {
	w := r.wallet
	if req.Lockname != "" {
		w.ResetLockedOutpoints(&req.Lockname)
	}
	transactions := req.Transactions
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

func (r *LightningRPCServer) UnlockAllUnspent(ctx context.Context, _ *rpc_pb.Null) (*rpc_pb.Null, er.R) {
	r.wallet.ResetLockedOutpoints(nil)
	return nil, nil
}

// makeOutputs creates a slice of transaction outputs from a pair of address
// strings to amounts.  This is used to create the outputs to include in newly
// created transactions from a JSON object describing the output destinations
// and amounts.
func makeOutputs(pairs map[string]btcutil.Amount, vote *waddrmgr.NetworkStewardVote,
	chainParams *chaincfg.Params) ([]*wire.TxOut, er.R) {
	outputs := make([]*wire.TxOut, 0, len(pairs))
	if vote == nil {
		vote = &waddrmgr.NetworkStewardVote{}
	}
	for addrStr, amt := range pairs {
		addr, err := btcutil.DecodeAddress(addrStr, chainParams)
		if err != nil {
			return nil, er.Errorf("cannot decode address: %s", err)
		}

		pkScript, err := txscript.PayToAddrScriptWithVote(addr, vote.VoteFor, vote.VoteAgainst)
		if err != nil {
			return nil, er.Errorf("cannot create txout script: %s", err)
		}

		outputs = append(outputs, wire.NewTxOut(int64(amt), pkScript))
	}
	return outputs, nil
}

func sendOutputs(
	w *wallet.Wallet,
	amounts map[string]btcutil.Amount,
	vote *waddrmgr.NetworkStewardVote,
	fromAddressses *[]string,
	minconf int32,
	feeSatPerKb btcutil.Amount,
	sendMode wallet.SendMode,
	changeAddress *string,
	inputMinHeight int,
	maxInputs int,
) (*txauthor.AuthoredTx, er.R) {
	req := wallet.CreateTxReq{
		Minconf:        minconf,
		FeeSatPerKB:    feeSatPerKb,
		SendMode:       sendMode,
		InputMinHeight: inputMinHeight,
		MaxInputs:      maxInputs,
		Label:          "",
	}
	if inputMinHeight > 0 {
		// TODO(cjd): Ideally we would expose the comparator choice to the
		// API consumer, but this is an API break. When we're using inputMinHeight
		// it's normally because we're trying to do multiple createtransaction
		// requests without double-spending, so it's important to prefer oldest
		// in this case.
		req.InputComparator = wallet.PreferOldest
	}
	var err er.R
	req.Outputs, err = makeOutputs(amounts, vote, w.ChainParams())
	if err != nil {
		return nil, err
	}
	if changeAddress != nil && *changeAddress != "" {
		addr, err := btcutil.DecodeAddress(*changeAddress, w.ChainParams())
		if err != nil {
			return nil, err
		}
		req.ChangeAddress = &addr
	}
	if fromAddressses != nil && len(*fromAddressses) > 0 {
		addrs := make([]btcutil.Address, 0, len(*fromAddressses))
		for _, addrStr := range *fromAddressses {
			addr, err := btcutil.DecodeAddress(addrStr, w.ChainParams())
			if err != nil {
				return nil, err
			}
			addrs = append(addrs, addr)
		}
		req.InputAddresses = addrs
	}
	tx, err := w.SendOutputs(req)
	if err != nil {
		if ruleerror.ErrNegativeTxOutValue.Is(err) {
			return nil, er.New("amount must be positive")
		}
		if waddrmgr.ErrLocked.Is(err) {
			return nil, er.New("Enter the wallet passphrase with walletpassphrase first")
		}
		if btcjson.Err.Is(err) {
			return nil, err
		}
		return nil, btcjson.ErrRPCInternal.New("SendOutputs failed", err)
	}
	return tx, nil
}

//CreateTransaction
func (r *LightningRPCServer) CreateTransaction(ctx context.Context, req *rpc_pb.CreateTransactionRequest) (*rpc_pb.CreateTransactionResponse, er.R) {
	toaddress := req.ToAddress
	amount := req.Amount
	fromaddresses := req.FromAddress

	autolock := req.Autolock

	if amount <= 0 {
		return nil, er.New("amount must be positive")
	}
	if math.IsInf(amount, 1) {
		amount = 0
	}
	minconf := int32(req.MinConf)
	if minconf < 0 {
		return nil, er.New("minconf must be positive")
	}
	inputminheight := 0
	if req.InputMinHeight > 0 {
		inputminheight = int(req.InputMinHeight)
	}
	// Create map of address and amount pairs.
	amt, err := btcutil.NewAmount(float64(amount))
	if err != nil {
		return nil, err
	}
	amounts := map[string]btcutil.Amount{
		toaddress: amt,
	}

	var vote *waddrmgr.NetworkStewardVote
	vote, err = r.wallet.NetworkStewardVote(0, waddrmgr.KeyScopeBIP0044)
	if err != nil {
		return nil, err
	}
	maxinputs := -1
	maxinputs = int(req.MaxInputs)
	sendmode := wallet.SendModeSigned
	if !req.Sign {
		sendmode = wallet.SendModeUnsigned
	}
	tx, err := sendOutputs(r.wallet, amounts, vote, &fromaddresses, minconf, txrules.DefaultRelayFeePerKb, sendmode, &req.ChangeAddress, inputminheight, maxinputs)
	if err != nil {
		return nil, err
	}

	for _, in := range tx.Tx.TxIn {
		op := in.PreviousOutPoint
		r.wallet.LockOutpoint(op, autolock)
	}

	var transaction []byte
	if req.ElectrumFormat {
		b := new(bytes.Buffer)
		if err := tx.Tx.BtcEncode(b, 0, wire.ForceEptfEncoding); err != nil {
			return nil, err
		}
		transaction = b.Bytes()
	} else {
		b := bytes.NewBuffer(make([]byte, 0, tx.Tx.SerializeSize()))
		if err := tx.Tx.Serialize(b); err != nil {
			return nil, err
		}
		transaction = b.Bytes()
	}

	return &rpc_pb.CreateTransactionResponse{
		Transaction: transaction,
	}, nil
}

func decodeAddress(s string, params *chaincfg.Params) (btcutil.Address, er.R) {
	addr, err := btcutil.DecodeAddress(s, params)
	if err != nil {
		msg := fmt.Sprintf("Invalid address %q: decode failed", s)
		return nil, btcjson.ErrRPCInvalidAddressOrKey.New(msg, err)
	}
	if !addr.IsForNet(params) {
		msg := fmt.Sprintf("Invalid address %q: not intended for use on %s",
			addr, params.Name)
		return nil, btcjson.ErrRPCInvalidAddressOrKey.New(msg, nil)
	}
	return addr, nil
}

//DumpPrivKey
func (r *LightningRPCServer) DumpPrivKey(ctx context.Context, req *rpc_pb.DumpPrivKeyRequest) (*rpc_pb.DumpPrivKeyResponse, er.R) {
	addr, err := decodeAddress(req.Address, r.wallet.ChainParams())
	if err != nil {
		return nil, err
	}
	key, err := r.wallet.DumpWIFPrivateKey(addr)
	if waddrmgr.ErrLocked.Is(err) {
		// Address was found, but the private key isn't
		// accessible.
		return nil, er.New("ErrRPCWalletUnlockNeeded -13 Enter the wallet passphrase with walletpassphrase first")
	}
	return &rpc_pb.DumpPrivKeyResponse{
		PrivateKey: key,
	}, nil
}

func (r *LightningRPCServer) GetNewAddress(ctx context.Context, req *rpc_pb.GetNewAddressRequest) (*rpc_pb.GetNewAddressResponse, er.R) {
	scope := waddrmgr.KeyScopeBIP0084
	if req.Legacy {
		scope = waddrmgr.KeyScopeBIP0044
	}
	if addr, err := r.wallet.NewAddress(waddrmgr.DefaultAccountNum, scope); err != nil {
		return nil, err
	} else {
		return &rpc_pb.GetNewAddressResponse{
			Address: addr.EncodeAddress(),
		}, nil
	}
}

// confirms returns the number of confirmations for a transaction in a block at
// height txHeight (or -1 for an unconfirmed tx) given the chain height
// curHeight.
func confirms(txHeight, curHeight int32) int32 {
	switch {
	case txHeight == -1, txHeight > curHeight:
		return 0
	default:
		return curHeight - txHeight + 1
	}
}

func (r *LightningRPCServer) GetTransaction(ctx context.Context, req *rpc_pb.GetTransactionRequest) (*rpc_pb.GetTransactionResponse, er.R) {
	w := r.wallet
	txHash, err := chainhash.NewHashFromStr(req.Txid)
	if err != nil {
		return nil, btcjson.ErrRPCDecodeHexString.New("Transaction hash string decode failed", err)
	}

	details, err := wallet.UnstableAPI(w).TxDetails(txHash)
	if err != nil {
		return nil, err
	}
	if details == nil {
		return nil, btcjson.ErrRPCNoTxInfo.Default()
	}

	syncBlock := w.Manager.SyncedTo()

	// TODO: The serialized transaction is already in the DB, so
	// reserializing can be avoided here.
	var txBuf bytes.Buffer
	txBuf.Grow(details.MsgTx.SerializeSize())
	err = details.MsgTx.Serialize(&txBuf)
	if err != nil {
		return nil, err
	}

	// TODO: Add a "generated" field to this result type.  "generated":true
	// is only added if the transaction is a coinbase.
	transaction := rpc_pb.TransactionResult{
		Txid:            req.Txid,
		Raw:             txBuf.Bytes(),
		Time:            details.Received.Unix(),
		TimeReceived:    details.Received.Unix(),
		WalletConflicts: []string{},
	}

	if details.Block.Height != -1 {
		transaction.BlockHash = details.Block.Hash.String()
		transaction.BlockTime = details.Block.Time.Unix()
		transaction.Confirmations = int64(confirms(details.Block.Height, syncBlock.Height))
	}

	var (
		debitTotal  btcutil.Amount
		creditTotal btcutil.Amount // Excludes change
		fee         btcutil.Amount
		feeF64      float64
	)
	for _, deb := range details.Debits {
		debitTotal += deb.Amount
	}
	for _, cred := range details.Credits {
		if !cred.Change {
			creditTotal += cred.Amount
		}
	}
	// Fee can only be determined if every input is a debit.
	if len(details.Debits) == len(details.MsgTx.TxIn) {
		var outputTotal btcutil.Amount
		for _, output := range details.MsgTx.TxOut {
			outputTotal += btcutil.Amount(output.Value)
		}
		fee = debitTotal - outputTotal
		feeF64 = fee.ToBTC()
	}

	if len(details.Debits) == 0 {
		// Credits must be set later, but since we know the full length
		// of the details slice, allocate it with the correct cap.
		transaction.Details = make([]*rpc_pb.GetTransactionDetailsResult, 0, len(details.Credits))
	} else {
		transaction.Details = make([]*rpc_pb.GetTransactionDetailsResult, 1, len(details.Credits)+1)

		transaction.Details[0] = &rpc_pb.GetTransactionDetailsResult{
			// Fields left zeroed:
			//   InvolvesWatchOnly
			//   Account
			//   Address
			//   Vout
			//
			// TODO(jrick): Address and Vout should always be set,
			// but we're doing the wrong thing here by not matching
			// core.  Instead, gettransaction should only be adding
			// details for transaction outputs, just like
			// listtransactions (but using the short result format).
			Category:    "send",
			Amount:      (-debitTotal).ToBTC(), // negative since it is a send
			AmountUnits: uint64(debitTotal),
		}
		transaction.Fee = feeF64
		transaction.FeeUnits = uint64(fee)
	}

	credCat := wallet.RecvCategory(details, syncBlock.Height, w.ChainParams()).String()
	for _, cred := range details.Credits {
		// Change is ignored.
		if cred.Change {
			continue
		}

		var address string
		_, addrs, _, err := txscript.ExtractPkScriptAddrs(
			details.MsgTx.TxOut[cred.Index].PkScript, w.ChainParams())
		if err == nil && len(addrs) == 1 {
			addr := addrs[0]
			address = addr.EncodeAddress()
		}

		transaction.Details = append(transaction.Details, &rpc_pb.GetTransactionDetailsResult{
			// Fields left zeroed:
			//   InvolvesWatchOnly
			//   Fee
			Address:  address,
			Category: credCat,
			Amount:   cred.Amount.ToBTC(),
			Vout:     cred.Index,
		})
	}
	transaction.Amount = creditTotal.ToBTC()
	transaction.AmountUnits = uint64(creditTotal)

	return &rpc_pb.GetTransactionResponse{
		Transaction: &transaction,
	}, nil
}

func (r *LightningRPCServer) GetNetworkStewardVote(ctx context.Context, _ *rpc_pb.Null) (*rpc_pb.GetNetworkStewardVoteResponse, er.R) {
	vote, err := r.wallet.NetworkStewardVote(waddrmgr.DefaultAccountNum, waddrmgr.KeyScopeBIP0044)
	if err != nil {
		return nil, err
	}
	response := &rpc_pb.GetNetworkStewardVoteResponse{}
	if vote == nil {
		return response, nil
	}
	params := r.wallet.ChainParams()
	if vote.VoteFor != nil {
		response.VoteFor = txscript.PkScriptToAddress(vote.VoteFor, params).EncodeAddress()
	}
	if vote.VoteAgainst != nil {
		response.VoteAgainst = txscript.PkScriptToAddress(vote.VoteAgainst, params).EncodeAddress()
	}
	return response, nil
}

func (r *LightningRPCServer) SetNetworkStewardVote(ctx context.Context, req *rpc_pb.SetNetworkStewardVoteRequest) (*rpc_pb.Null, er.R) {
	vote := waddrmgr.NetworkStewardVote{}
	params := r.wallet.ChainParams()
	if req.VoteFor == "" {
	} else if vf, err := btcutil.DecodeAddress(req.VoteFor, params); err != nil {
		return nil, err
	} else if vfs, err := txscript.PayToAddrScript(vf); err != nil {
		return nil, err
	} else {
		vote.VoteFor = vfs
	}
	if req.VoteAgainst == "" {
	} else if va, err := btcutil.DecodeAddress(req.VoteAgainst, params); err != nil {
		return nil, err
	} else if vas, err := txscript.PayToAddrScript(va); err != nil {
		return nil, err
	} else {
		vote.VoteAgainst = vas
	}
	return nil, r.wallet.PutNetworkStewardVote(waddrmgr.DefaultAccountNum, waddrmgr.KeyScopeBIP0044, &vote)
}

func (r *LightningRPCServer) BcastTransaction(ctx context.Context, req *rpc_pb.BcastTransactionRequest) (*rpc_pb.BcastTransactionResponse, er.R) {
	log.Debugf("[0] BcastTransaction(): req.tx(%d): %s", len(req.Tx), string(req.Tx))
	dst := make([]byte, hex.DecodedLen(len(req.Tx)))
	_, errr := hex.Decode(dst, req.Tx)
	if errr != nil {
		return nil, er.E(errr)
	}

	var msgTx wire.MsgTx

	err := msgTx.Deserialize(bytes.NewReader(dst))
	if err != nil {
		return nil, err
	}

	txidhash, err := r.wallet.ReliablyPublishTransaction(&msgTx, "")
	if err != nil {
		return nil, err
	}

	return &rpc_pb.BcastTransactionResponse{
		TxnHash: txidhash.String(),
	}, err
}

// sendPairs creates and sends payment transactions.
// It returns the transaction hash in string format upon success
// All errors are returned in btcjson.RPCError format
func sendPairs(w *wallet.Wallet, amounts map[string]btcutil.Amount,
	fromAddressses *[]string, minconf int32, feeSatPerKb btcutil.Amount, maxInputs, inputMinHeight int) (string, er.R) {

	vote, err := w.NetworkStewardVote(0, waddrmgr.KeyScopeBIP0044)
	if err != nil {
		return "", err
	}

	tx, err := sendOutputs(w, amounts, vote, fromAddressses, minconf, feeSatPerKb, wallet.SendModeBcasted, nil, inputMinHeight, maxInputs)
	if err != nil {
		return "", err
	}

	txHashStr := tx.Tx.TxHash().String()
	log.Infof("Successfully sent transaction [%s]", log.Txid(txHashStr))
	return txHashStr, nil
}

//SendFrom
func (r *LightningRPCServer) SendFrom(ctx context.Context, req *rpc_pb.SendFromRequest) (*rpc_pb.SendFromResponse, er.R) {
	toaddress := req.ToAddress
	amount := req.Amount
	fromaddresses := req.FromAddress

	if amount <= 0 {
		return nil, er.New("amount must be positive")
	}
	if math.IsInf(amount, 1) {
		amount = 0
	}
	minconf := int32(req.MinConf)
	if minconf < 0 {
		return nil, er.New("minconf must be positive")
	}
	minheight := 0
	if req.MinHeight > 0 {
		minheight = int(req.MinHeight)
	}
	// Create map of address and amount pairs.
	amt, err := btcutil.NewAmount(float64(amount))
	if err != nil {
		return nil, err
	}
	amounts := map[string]btcutil.Amount{
		toaddress: amt,
	}

	maxinputs := -1
	maxinputs = int(req.MaxInputs)

	tx, err := sendPairs(r.wallet, amounts, &fromaddresses, minconf, txrules.DefaultRelayFeePerKb, maxinputs, minheight)
	if err != nil {
		return nil, err
	}

	return &rpc_pb.SendFromResponse{
		TxHash: tx,
	}, nil
}

func (r *LightningRPCServer) transactionGetter() func(txns map[string]*wire.MsgTx) er.R {
	return func(txns map[string]*wire.MsgTx) er.R {
		return walletdb.View(r.wallet.Database(), func(dbtx walletdb.ReadTx) er.R {
			txmgrNs := dbtx.ReadBucket([]byte("wtxmgr"))
			for k := range txns {
				tx, err := r.wallet.TxStore.TxDetails(txmgrNs, chainhash.MustNewHashFromStr(k))
				if err != nil {
					// TxDetails only returns an error if something actually went wrong
					// not found == nil, nil
					return err
				} else if tx != nil {
					txns[k] = &tx.MsgTx
				}
			}
			return nil
		})
	}
}

func (r *LightningRPCServer) DecodeRawTransaction(ctx context.Context, req *rpc_pb.DecodeRawTransactionRequest) (*rpc_pb.TransactionInfo, er.R) {
	// Deserialize the transaction.
	var serializedTx []byte
	if len(req.BinTx) > 0 {
		serializedTx = req.BinTx
	} else {
		hexStr := req.HexTx
		if len(hexStr)%2 != 0 {
			hexStr = "0" + hexStr
		}
		stx, err := util.DecodeHex(hexStr)
		if err != nil {
			return nil, err
		}
		serializedTx = stx
	}

	var mtx wire.MsgTx
	err := mtx.Deserialize(bytes.NewReader(serializedTx))
	if err != nil {
		return nil, err
	}

	txi, err := describetxn.Describe(
		r.transactionGetter(),
		mtx,
		r.cfg.ActiveNetParams.Params,
		req.IncludeVinDetail,
	)
	if err != nil {
		return nil, err
	}
	return txi, nil
}

func isValidIPv6(addr string) bool {
	pattern := `^([0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}$`
	match, _ := regexp.MatchString(pattern, addr)
	return match
}

//NOTE: New RPC functions should be implemented where they are run, in the appropriate modules.
//See GetTransactionsRequest at pktwallet/wallet/wallet.go for example
