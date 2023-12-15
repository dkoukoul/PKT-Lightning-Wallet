package meta

import (
	"os"
	"strconv"

	"github.com/pkt-cash/pktd/btcjson"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/connmgr/banmgr"
	"github.com/pkt-cash/pktd/generated/proto/meta_pb"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/verrpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/neutrino"
	"github.com/pkt-cash/pktd/pktconfig/version"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/pkt-cash/pktd/pktwallet/waddrmgr"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
)

type rpc struct {
	w          *wallet.Wallet
	neutrinoCS *neutrino.ChainService
}

func (r *rpc) getinfo(m *rpc_pb.Null) (*meta_pb.GetInfo2Response, er.R) {
	// Neutrino
	ni := rpc_pb.NeutrinoInfo{}
	neutrinoPeers := r.neutrinoCS.Peers()
	for i := range neutrinoPeers {
		var peerDesc rpc_pb.PeerDesc
		neutrinoPeer := neutrinoPeers[i]

		peerDesc.BytesReceived = neutrinoPeer.BytesReceived()
		peerDesc.BytesSent = neutrinoPeer.BytesSent()
		peerDesc.LastRecv = neutrinoPeer.LastRecv().String()
		peerDesc.LastSend = neutrinoPeer.LastSend().String()
		peerDesc.Connected = neutrinoPeer.Connected()
		peerDesc.Addr = neutrinoPeer.Addr()
		peerDesc.Inbound = neutrinoPeer.Inbound()
		na := neutrinoPeer.NA()
		if na != nil {
			peerDesc.Na = na.IP.String() + ":" + strconv.Itoa(int(na.Port))
		}
		peerDesc.Id = neutrinoPeer.ID()
		peerDesc.UserAgent = neutrinoPeer.UserAgent()
		peerDesc.Services = neutrinoPeer.Services().String()
		peerDesc.VersionKnown = neutrinoPeer.VersionKnown()
		peerDesc.AdvertisedProtoVer = neutrinoPeer.Describe().AdvertisedProtoVer
		peerDesc.ProtocolVersion = neutrinoPeer.ProtocolVersion()
		peerDesc.SendHeadersPreferred = neutrinoPeer.Describe().SendHeadersPreferred
		peerDesc.VerAckReceived = neutrinoPeer.VerAckReceived()
		peerDesc.WitnessEnabled = neutrinoPeer.Describe().WitnessEnabled
		peerDesc.WireEncoding = strconv.Itoa(int(neutrinoPeer.Describe().WireEncoding))
		peerDesc.TimeOffset = neutrinoPeer.TimeOffset()
		peerDesc.TimeConnected = neutrinoPeer.Describe().TimeConnected.String()
		peerDesc.StartingHeight = neutrinoPeer.StartingHeight()
		peerDesc.LastBlock = neutrinoPeer.LastBlock()
		if neutrinoPeer.LastAnnouncedBlock() != nil {
			peerDesc.LastAnnouncedBlock = neutrinoPeer.LastAnnouncedBlock().CloneBytes()
		}
		peerDesc.LastPingNonce = neutrinoPeer.LastPingNonce()
		peerDesc.LastPingTime = neutrinoPeer.LastPingTime().String()
		peerDesc.LastPingMicros = neutrinoPeer.LastPingMicros()

		ni.Peers = append(ni.Peers, &peerDesc)
	}
	r.neutrinoCS.BanMgr().ForEachIp(func(bi banmgr.BanInfo) er.R {
		ban := rpc_pb.NeutrinoBan{}
		ban.Addr = bi.Addr
		ban.Reason = bi.Reason
		ban.EndTime = bi.BanExpiresTime.String()
		ban.BanScore = bi.BanScore

		ni.Bans = append(ni.Bans, &ban)
		return nil
	})

	neutrionoQueries := r.neutrinoCS.GetActiveQueries()
	for i := range neutrionoQueries {
		nq := rpc_pb.NeutrinoQuery{}
		query := neutrionoQueries[i]
		if query.Peer != nil {
			nq.Peer = query.Peer.String()
		} else {
			nq.Peer = "<nil>"
		}
		nq.Command = query.Command
		nq.ReqNum = query.ReqNum
		nq.CreateTime = query.CreateTime
		nq.LastRequestTime = query.LastRequestTime
		nq.LastResponseTime = query.LastResponseTime

		ni.Queries = append(ni.Queries, &nq)
	}

	bb, err := r.neutrinoCS.BestBlock()
	if err != nil {
		return nil, err
	}
	ni.BlockHash = bb.Hash.String()
	ni.Height = bb.Height
	ni.BlockTimestamp = bb.Timestamp.String()
	ni.IsSyncing = !r.neutrinoCS.IsCurrent()

	// Wallet
	var walletInfo *rpc_pb.WalletInfo
	mgrStamp := r.w.Manager.SyncedTo()
	walletStats := &rpc_pb.WalletStats{}
	r.w.ReadStats(func(ws *btcjson.WalletStats) {
		walletStats.MaintenanceInProgress = ws.MaintenanceInProgress
		walletStats.MaintenanceName = ws.MaintenanceName
		walletStats.MaintenanceCycles = int32(ws.MaintenanceCycles)
		walletStats.MaintenanceLastBlockVisited = int32(ws.MaintenanceLastBlockVisited)
		walletStats.Syncing = ws.Syncing
		if ws.SyncStarted != nil {
			walletStats.SyncStarted = ws.SyncStarted.String()
		}
		walletStats.SyncRemainingSeconds = ws.SyncRemainingSeconds
		walletStats.SyncCurrentBlock = ws.SyncCurrentBlock
		walletStats.SyncFrom = ws.SyncFrom
		walletStats.SyncTo = ws.SyncTo
		walletStats.BirthdayBlock = ws.BirthdayBlock
	})
	walletInfo = &rpc_pb.WalletInfo{
		CurrentBlockHash:      mgrStamp.Hash.String(),
		CurrentHeight:         mgrStamp.Height,
		CurrentBlockTimestamp: mgrStamp.Timestamp.String(),
		WalletVersion:         int32(waddrmgr.LatestMgrVersion),
		WalletStats:           walletStats,
	}

	// Get Lightning info TODO
	var lightning *rpc_pb.GetInfoResponse
	// if cc := c.MaybeRpcServer; cc != nil {
	// 	if l, err := cc.GetInfo(context.TODO(), nil); err != nil {
	// 		return nil, er.E(err)
	// 	} else {
	// 		lightning = l
	// 	}
	// }

	return &meta_pb.GetInfo2Response{
		Neutrino:  &ni,
		Wallet:    walletInfo,
		Lightning: lightning,
	}, nil
}

func (r *rpc) DebugLevel(in *rpc_pb.DebugLevelRequest) (*rpc_pb.Null, er.R) {
	log.Infof("[debuglevel] changing debug level to: %v", in.LevelSpec)

	// Otherwise, we'll attempt to set the logging level using the
	// specified level spec.
	err := log.SetLogLevels(in.LevelSpec)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (r *rpc) StopDaemon(in *rpc_pb.Null) (*rpc_pb.Null, er.R) {
	os.Exit(0)
	return nil, nil
}

func (r *rpc) Version(in *rpc_pb.Null) (*verrpc_pb.Version, er.R) {
	return &verrpc_pb.Version{
		Commit:        "UNKNOWN",
		CommitHash:    "UNKNOWN",
		BuildTags:     []string{"UNKNOWN"},
		GoVersion:     "UNKNOWN",
		Version:       version.Version(),
		AppMajor:      uint32(version.AppMajorVersion()),
		AppMinor:      uint32(version.AppMinorVersion()),
		AppPatch:      uint32(version.AppPatchVersion()),
		AppPreRelease: util.If(version.IsPrerelease(), "true", "false"),
	}, nil
}

func Register(
	a *apiv1.Apiv1,
	neutrinoCS *neutrino.ChainService,
	w *wallet.Wallet,
) {
	r := rpc{w: w, neutrinoCS: neutrinoCS}
	apiv1.Endpoint(
		a,
		"getinfo",
		`
		Returns basic information related to the active daemon
	
		GetInfo returns general information concerning the lightning node including
		it's identity pubkey, alias, the chains it is connected to, and information
		concerning the number of open+pending channels.
		`,
		r.getinfo,
	)

	apiv1.Endpoint(
		a,
		"debuglevel",
		`
		Set the debug level

		DebugLevel allows a caller to programmatically set the logging verbosity of
		lnd. The logging can be targeted according to a coarse daemon-wide logging
		level, or in a granular fashion to specify the logging for a target
		sub-system.
		`,
		r.DebugLevel,
	)
	apiv1.Endpoint(
		a,
		"stop",
		`
		Stop and shutdown the daemon

		StopDaemon will send a shutdown request to the interrupt handler, triggering
		a graceful shutdown of the daemon.
		`,
		r.StopDaemon,
	)
	apiv1.Endpoint(
		a,
		"version",
		`
		Display pld version info

		GetVersion returns the current version and build information of the running
		daemon.
		`,
		r.Version,
	)
}
