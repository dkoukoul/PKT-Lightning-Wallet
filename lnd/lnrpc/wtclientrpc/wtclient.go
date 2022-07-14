package wtclientrpc

import (
	"context"
	"net"
	"strconv"

	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/pkt-cash/pktd/btcec"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/generated/proto/wtclientrpc_pb"
	"github.com/pkt-cash/pktd/lnd/lncfg"
	"github.com/pkt-cash/pktd/lnd/lnwire"
	"github.com/pkt-cash/pktd/lnd/watchtower"
	"github.com/pkt-cash/pktd/lnd/watchtower/wtclient"
	"github.com/pkt-cash/pktd/pktlog/log"
	"google.golang.org/grpc"
)

const (
	// subServerName is the name of the sub rpc server. We'll use this name
	// to register ourselves, and we also require that the main
	// SubServerConfigDispatcher instance recognizes it as the name of our
	// RPC service.
	subServerName = "WatchtowerClientRPC"
)

// ErrWtclientNotActive signals that RPC calls cannot be processed
// because the watchtower client is not active.
var ErrWtclientNotActive = er.GenericErrorType.CodeWithDetail("ErrWtclientNotActive",
	"watchtower client not active")

// WatchtowerClient is the RPC server we'll use to interact with the backing
// active watchtower client.
//
// TODO(wilmer): better name?
type WatchtowerClient struct {
	cfg Config
	wtclientrpc_pb.UnimplementedWatchtowerClientServer
}

// A compile time check to ensure that WatchtowerClient fully implements the
// WatchtowerClientWatchtowerClient gRPC service.
var _ wtclientrpc_pb.WatchtowerClientServer = (*WatchtowerClient)(nil)

// New returns a new instance of the wtclientrpc WatchtowerClient sub-server.
// We also return the set of permissions for the macaroons that we may create
// within this method. If the macaroons we need aren't found in the filepath,
// then we'll create them on start up. If we're unable to locate, or create the
// macaroons we need, then we'll return with an error.
func New(cfg *Config) (*WatchtowerClient, er.R) {
	return &WatchtowerClient{cfg: *cfg}, nil
}

// Start launches any helper goroutines required for the WatchtowerClient to
// function.
//
// NOTE: This is part of the lnrpc.SubWatchtowerClient interface.
func (c *WatchtowerClient) Start() er.R {
	return nil
}

// Stop signals any active goroutines for a graceful closure.
//
// NOTE: This is part of the lnrpc.SubServer interface.
func (c *WatchtowerClient) Stop() er.R {
	return nil
}

// Name returns a unique string representation of the sub-server. This can be
// used to identify the sub-server and also de-duplicate them.
//
// NOTE: This is part of the lnrpc.SubServer interface.
func (c *WatchtowerClient) Name() string {
	return subServerName
}

// RegisterWithRootServer will be called by the root gRPC server to direct a sub
// RPC server to register itself with the main gRPC root server. Until this is
// called, each sub-server won't be able to have requests routed towards it.
//
// NOTE: This is part of the lnrpc.SubServer interface.
func (c *WatchtowerClient) RegisterWithRootServer(grpcServer *grpc.Server) er.R {
	// We make sure that we register it with the main gRPC server to ensure
	// all our methods are routed properly.
	wtclientrpc_pb.RegisterWatchtowerClientServer(grpcServer, c)

	log.Debugf("WatchtowerClient RPC server successfully registered " +
		"with  root gRPC server")

	return nil
}

// RegisterWithRestServer will be called by the root REST mux to direct a sub
// RPC server to register itself with the main REST mux server. Until this is
// called, each sub-server won't be able to have requests routed towards it.
//
// NOTE: This is part of the lnrpc.SubServer interface.
func (c *WatchtowerClient) RegisterWithRestServer(ctx context.Context,
	mux *runtime.ServeMux, dest string, opts []grpc.DialOption) er.R {

	// We make sure that we register it with the main REST server to ensure
	// all our methods are routed properly.
	// err := wtclientrpc_pb.RegisterWatchtowerClientHandlerFromEndpoint(ctx, mux, dest, opts)
	// if err != nil {
	// 	return er.E(err)
	// }

	return nil
}

// isActive returns nil if the watchtower client is initialized so that we can
// process RPC requests.
func (c *WatchtowerClient) isActive() er.R {
	if c.cfg.Active {
		return nil
	}
	return ErrWtclientNotActive.Default()
}

// AddTower adds a new watchtower reachable at the given address and considers
// it for new sessions. If the watchtower already exists, then any new addresses
// included will be considered when dialing it for session negotiations and
// backups.
func (c *WatchtowerClient) AddTower(ctx context.Context,
	req *wtclientrpc_pb.AddTowerRequest) (*wtclientrpc_pb.AddTowerResponse, error) {

	if err := c.isActive(); err != nil {
		return nil, er.Native(err)
	}

	pubKey, err := btcec.ParsePubKey(req.Pubkey, btcec.S256())
	if err != nil {
		return nil, er.Native(err)
	}
	addr, errr := lncfg.ParseAddressString(
		req.Address, strconv.Itoa(watchtower.DefaultPeerPort),
		c.cfg.Resolver,
	)
	if errr != nil {
		return nil, er.Native(er.Errorf("invalid address %v: %v", req.Address, errr))
	}

	towerAddr := &lnwire.NetAddress{
		IdentityKey: pubKey,
		Address:     addr,
	}
	if err := c.cfg.Client.AddTower(towerAddr); err != nil {
		return nil, er.Native(err)
	}

	return &wtclientrpc_pb.AddTowerResponse{}, nil
}

// RemoveTower removes a watchtower from being considered for future session
// negotiations and from being used for any subsequent backups until it's added
// again. If an address is provided, then this RPC only serves as a way of
// removing the address from the watchtower instead.
func (c *WatchtowerClient) RemoveTower(ctx context.Context,
	req *wtclientrpc_pb.RemoveTowerRequest) (*wtclientrpc_pb.RemoveTowerResponse, error) {

	if err := c.isActive(); err != nil {
		return nil, er.Native(err)
	}

	pubKey, err := btcec.ParsePubKey(req.Pubkey, btcec.S256())
	if err != nil {
		return nil, er.Native(err)
	}

	var addr net.Addr
	if req.Address != "" {
		addr, err = lncfg.ParseAddressString(
			req.Address, strconv.Itoa(watchtower.DefaultPeerPort),
			c.cfg.Resolver,
		)
		if err != nil {
			return nil, er.Native(er.Errorf("unable to parse tower "+
				"address %v: %v", req.Address, err))
		}
	}

	if err := c.cfg.Client.RemoveTower(pubKey, addr); err != nil {
		return nil, er.Native(err)
	}

	return &wtclientrpc_pb.RemoveTowerResponse{}, nil
}

// ListTowers returns the list of watchtowers registered with the client.
func (c *WatchtowerClient) ListTowers(ctx context.Context,
	req *wtclientrpc_pb.ListTowersRequest) (*wtclientrpc_pb.ListTowersResponse, error) {

	if err := c.isActive(); err != nil {
		return nil, er.Native(err)
	}

	towers, err := c.cfg.Client.RegisteredTowers()
	if err != nil {
		return nil, er.Native(err)
	}

	rpcTowers := make([]*wtclientrpc_pb.Tower, 0, len(towers))
	for _, tower := range towers {
		rpcTower := marshallTower(tower, req.IncludeSessions)
		rpcTowers = append(rpcTowers, rpcTower)
	}

	return &wtclientrpc_pb.ListTowersResponse{Towers: rpcTowers}, nil
}

// GetTowerInfo retrieves information for a registered watchtower.
func (c *WatchtowerClient) GetTowerInfo(ctx context.Context,
	req *wtclientrpc_pb.GetTowerInfoRequest) (*wtclientrpc_pb.Tower, error) {

	if err := c.isActive(); err != nil {
		return nil, er.Native(err)
	}

	pubKey, err := btcec.ParsePubKey(req.Pubkey, btcec.S256())
	if err != nil {
		return nil, er.Native(err)
	}

	tower, err := c.cfg.Client.LookupTower(pubKey)
	if err != nil {
		return nil, er.Native(err)
	}

	return marshallTower(tower, req.IncludeSessions), nil
}

// Stats returns the in-memory statistics of the client since startup.
func (c *WatchtowerClient) Stats(ctx context.Context,
	req *wtclientrpc_pb.StatsRequest) (*wtclientrpc_pb.StatsResponse, error) {

	if err := c.isActive(); err != nil {
		return nil, er.Native(err)
	}

	stats := c.cfg.Client.Stats()
	return &wtclientrpc_pb.StatsResponse{
		NumBackups:           uint32(stats.NumTasksAccepted),
		NumFailedBackups:     uint32(stats.NumTasksIneligible),
		NumPendingBackups:    uint32(stats.NumTasksReceived),
		NumSessionsAcquired:  uint32(stats.NumSessionsAcquired),
		NumSessionsExhausted: uint32(stats.NumSessionsExhausted),
	}, nil
}

// Policy returns the active watchtower client policy configuration.
func (c *WatchtowerClient) Policy(ctx context.Context,
	req *wtclientrpc_pb.PolicyRequest) (*wtclientrpc_pb.PolicyResponse, error) {

	if err := c.isActive(); err != nil {
		return nil, er.Native(err)
	}

	policy := c.cfg.Client.Policy()
	return &wtclientrpc_pb.PolicyResponse{
		MaxUpdates:      uint32(policy.MaxUpdates),
		SweepSatPerByte: uint32(policy.SweepFeeRate.FeePerKVByte() / 1000),
	}, nil
}

// marshallTower converts a client registered watchtower into its corresponding
// RPC type.
func marshallTower(tower *wtclient.RegisteredTower, includeSessions bool) *wtclientrpc_pb.Tower {
	rpcAddrs := make([]string, 0, len(tower.Addresses))
	for _, addr := range tower.Addresses {
		rpcAddrs = append(rpcAddrs, addr.String())
	}

	var rpcSessions []*wtclientrpc_pb.TowerSession
	if includeSessions {
		rpcSessions = make([]*wtclientrpc_pb.TowerSession, 0, len(tower.Sessions))
		for _, session := range tower.Sessions {
			satPerByte := session.Policy.SweepFeeRate.FeePerKVByte() / 1000
			rpcSessions = append(rpcSessions, &wtclientrpc_pb.TowerSession{
				NumBackups:        uint32(len(session.AckedUpdates)),
				NumPendingBackups: uint32(len(session.CommittedUpdates)),
				MaxBackups:        uint32(session.Policy.MaxUpdates),
				SweepSatPerByte:   uint32(satPerByte),
			})
		}
	}

	return &wtclientrpc_pb.Tower{
		Pubkey:                 tower.IdentityKey.SerializeCompressed(),
		Addresses:              rpcAddrs,
		ActiveSessionCandidate: tower.ActiveSessionCandidate,
		NumSessions:            uint32(len(tower.Sessions)),
		Sessions:               rpcSessions,
	}
}
