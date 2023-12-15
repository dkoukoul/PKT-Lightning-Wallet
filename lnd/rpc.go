// NOTICE: This entire file is DEPRECATED
// Please avoid adding new endpoints to this file, instead add them where
// the business logic is. See pktwallet/wallet.go for an example of how to
// do this well.
// Endpoints which are relevant to different business logic should be moved
// where appropriate.
package lnd

import (
	"context"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/generated/proto/restrpc_pb/help_pb"
	"github.com/pkt-cash/pktd/generated/proto/routerrpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/verrpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/wtclientrpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/lnd/lnrpc/routerrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/wtclientrpc"
	"github.com/pkt-cash/pktd/pktconfig/version"
	"google.golang.org/protobuf/proto"
)

func (c *RpcContext) RegisterFunctions(a *apiv1.Apiv1) {
	lightning := apiv1.DefineCategory(
		a,
		"lightning",
		`
		The Lightning Network component of the wallet
		`,
	)
	lightningChannel := apiv1.DefineCategory(
		lightning,
		"channel",
		`
		Management of lightning channels to direct peers of this pld node
		`,
	)
	apiv1.Endpoint(
		lightningChannel,
		"",
		`
		List all open channels

		ListChannels returns a description of all the open channels that this node
		is a participant in.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ListChannelsRequest) (*rpc_pb.ListChannelsResponse, er.R) {
			return rs.ListChannels(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningChannel,
		"open",
		`
		Open a channel to a node or an existing peer
		
		OpenChannel attempts to open a singly funded channel specified in the
		request to a remote peer. Users are able to specify a target number of
		blocks that the funding transaction should be confirmed in, or a manual fee
		rate to us for the funding transaction. If neither are specified, then a
		lax block confirmation target is used. Each OpenStatusUpdate will return
		the pending channel ID of the in-progress channel. Depending on the
		arguments specified in the OpenChannelRequest, this pending channel ID can
		then be used to manually progress the channel funding flow.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.OpenChannelRequest) (*rpc_pb.ChannelPoint, er.R) {
			return rs.OpenChannelSync(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningChannel,
		"close",
		`
		Close an existing channel

		CloseChannel attempts to close an active channel identified by its channel
		outpoint (ChannelPoint). The actions of this method can additionally be
		augmented to attempt a force close after a timeout period in the case of an
		inactive peer. If a non-force close (cooperative closure) is requested,
		then the user can specify either a target number of blocks until the
		closure transaction is confirmed, or a manual fee rate. If neither are
		specified, then a default lax, block confirmation target is used.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.CloseChannelRequest) (*rpc_pb.Null, er.R) {
			// TODO(cjd): streaming
			return nil, rs.CloseChannel(req, nil)
		}),
	)
	apiv1.Endpoint(
		lightningChannel,
		"abandon",
		`
		Abandons an existing channel

		AbandonChannel removes all channel state from the database except for a
		close summary. This method can be used to get rid of permanently unusable
		channels due to bugs fixed in newer versions of lnd. This method can also be
		used to remove externally funded channels where the funding transaction was
		never broadcast. Only available for non-externally funded channels in dev
		build.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.AbandonChannelRequest) (*rpc_pb.AbandonChannelResponse, er.R) {
			return rs.AbandonChannel(req)
		}),
	)
	apiv1.Endpoint(
		lightningChannel,
		"balance",
		`
		Returns the sum of the total available channel balance across all open channels

		ChannelBalance returns a report on the total funds across all open channels,
		categorized in local/remote, pending local/remote and unsettled local/remote
		balances.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.ChannelBalanceResponse, er.R) {
			return rs.ChannelBalance(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningChannel,
		"pending",
		`
		Display information pertaining to pending channels

		PendingChannels returns a list of all the channels that are currently
		considered "pending". A channel is pending if it has finished the funding
		workflow and is waiting for confirmations for the funding txn, or is in the
		process of closure, either initiated cooperatively or non-cooperatively.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.PendingChannelsResponse, er.R) {
			return rs.PendingChannels(context.TODO(), req)
		}),
		help_pb.F_ALLOW_GET,
	)
	apiv1.Endpoint(
		lightningChannel,
		"closed",
		`
		List all closed channels

		ClosedChannels returns a description of all the closed channels that
		this node was a participant in.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ClosedChannelsRequest) (*rpc_pb.ClosedChannelsResponse, er.R) {
			return rs.ClosedChannels(context.TODO(), req)
		}),
		help_pb.F_ALLOW_GET,
	)
	apiv1.Endpoint(
		lightningChannel,
		"networkinfo",
		`
		Get statistical information about the current state of the network

		GetNetworkInfo returns some basic stats about the known channel graph from
		the point of view of the node.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.NetworkInfo, er.R) {
			return rs.GetNetworkInfo(req)
		}),
		help_pb.F_ALLOW_GET,
	)
	apiv1.Endpoint(
		lightningChannel,
		"feereport",
		`
		Display the current fee policies of all active channels

		FeeReport allows the caller to obtain a report detailing the current fee
		schedule enforced by the node globally for each channel.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.FeeReportResponse, er.R) {
			return rs.FeeReport(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningChannel,
		"policy",
		`
		Display the current fee policies of all active channels

		FeeReport allows the caller to obtain a report detailing the current fee
		schedule enforced by the node globally for each channel.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.PolicyUpdateRequest) (*rpc_pb.PolicyUpdateResponse, er.R) {
			return rs.UpdateChannelPolicy(context.TODO(), req)
		}),
	)

	//	>>> lightning/channel/backup subCategory commands
	lightningChannelBackup := apiv1.DefineCategory(
		lightningChannel,
		"backup",
		`
		Backup and recovery of the state of active Lightning Channels
		`,
	)
	apiv1.Endpoint(
		lightningChannelBackup,
		"export",
		`
		Obtain a static channel back up for a selected channels, or all known channels

		ExportChannelBackup attempts to return an encrypted static channel backup
		for the target channel identified by it channel point. The backup is
		encrypted with a key generated from the aezeed seed of the user. The
		returned backup can either be restored using the RestoreChannelBackup
		method once lnd is running, or via the InitWallet and UnlockWallet methods
		from the WalletUnlocker service.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ExportChannelBackupRequest) (*rpc_pb.ChannelBackup, er.R) {
			return rs.ExportChannelBackup(context.TODO(), req)
		}),
	)

	apiv1.Endpoint(
		lightningChannelBackup,
		"verify",
		`
		Verify an existing channel backup

		VerifyChanBackup allows a caller to verify the integrity of a channel backup
		snapshot. This method will accept either a packed Single or a packed Multi.
		Specifying both will result in an error.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ChanBackupSnapshot) (*rpc_pb.VerifyChanBackupResponse, er.R) {
			return rs.VerifyChanBackup(context.TODO(), req)
		}),
	)

	apiv1.Endpoint(
		lightningChannelBackup,
		"restore",
		`
		Restore an existing single or multi-channel static channel backup

		RestoreChannelBackups accepts a set of singular channel backups, or a
		single encrypted multi-chan backup and attempts to recover any funds
		remaining within the channel. If we are able to unpack the backup, then the
		new channel will be shown under listchannels, as well as pending channels.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.RestoreChanBackupRequest) (*rpc_pb.RestoreBackupResponse, er.R) {
			return rs.RestoreChannelBackups(context.TODO(), req)
		}),
	)

	//	>>> lightning/graph subCategory commands
	lightningGraph := apiv1.DefineCategory(
		lightning, "graph", "Information about the global known Lightning Network")

	apiv1.Endpoint(
		lightningGraph,
		"",
		`
		Describe the network graph

		DescribeGraph returns a description of the latest graph state from the
		point of view of the node. The graph information is partitioned into two
		components: all the nodes/vertexes, and all the edges that connect the
		vertexes themselves. As this is a directed graph, the edges also contain
		the node directional specific routing policy which includes: the time lock
		delta, fee information, etc.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ChannelGraphRequest) (*rpc_pb.ChannelGraph, er.R) {
			return rs.DescribeGraph(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningGraph,
		"nodemetrics",
		`
		Get node metrics

		Returns node metrics calculated from the graph. Currently
		the only supported metric is betweenness centrality of individual nodes.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.NodeMetricsRequest) (*rpc_pb.NodeMetricsResponse, er.R) {
			return rs.GetNodeMetrics(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningGraph,
		"channel",
		`
		Get the state of a channel

		GetChanInfo returns the latest authenticated network announcement for the
		given channel identified by its channel ID: an 8-byte integer which
		uniquely identifies the location of transaction's funding output within the
		blockchain.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ChanInfoRequest) (*rpc_pb.ChannelEdge, er.R) {
			return rs.GetChanInfo(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningGraph,
		"nodeinfo",
		`
		Get information on a specific node

		Returns the latest advertised, aggregated, and authenticated
		channel information for the specified node identified by its public key.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.NodeInfoRequest) (*rpc_pb.NodeInfo, er.R) {
			return rs.GetNodeInfo(context.TODO(), req)
		}),
	)

	//	>>> lightning/invoice subCategory commands
	lightningInvoice := apiv1.DefineCategory(
		lightning, "invoice", "Management of invoices which are used to request payment over Lightning")
	apiv1.Endpoint(
		lightningInvoice,
		"create",
		`
		Add a new invoice

		AddInvoice attempts to add a new invoice to the invoice database. Any
		duplicated invoices are rejected, therefore all invoices *must* have a
		unique payment preimage.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Invoice) (*rpc_pb.AddInvoiceResponse, er.R) {
			return rs.AddInvoice(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningInvoice,
		"lookup",
		`
		Lookup an existing invoice by its payment hash

		LookupInvoice attempts to look up an invoice according to its payment hash.
		The passed payment hash *must* be exactly 32 bytes, if not, an error is
		returned.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.PaymentHash) (*rpc_pb.Invoice, er.R) {
			return rs.LookupInvoice(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningInvoice,
		"",
		`
		List all invoices currently stored within the database. Any active debug invoices are ignored

		ListInvoices returns a list of all the invoices currently stored within the
		database. Any active debug invoices are ignored. It has full support for
		paginated responses, allowing users to query for specific invoices through
		their add_index. This can be done by using either the first_index_offset or
		last_index_offset fields included in the response as the index_offset of the
		next request. By default, the first 100 invoices created will be returned.
		Backwards pagination is also supported through the Reversed flag.
		`,
		withRpc(c, func(cc *LightningRPCServer, req *rpc_pb.ListInvoiceRequest) (*rpc_pb.ListInvoiceResponse, er.R) {
			return cc.ListInvoices(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningInvoice,
		"decodepayreq",
		`
		Decode a payment request

		DecodePayReq takes an encoded payment request string and attempts to decode
		it, returning a full description of the conditions encoded within the
		payment request.
		`,
		withRpc(c, func(cc *LightningRPCServer, req *rpc_pb.PayReqString) (*rpc_pb.PayReq, er.R) {
			return cc.DecodePayReq(context.TODO(), req)
		}),
	)

	//	>>> lightning/payment subCategory command
	lightningPayment := apiv1.DefineCategory(lightning, "payment",
		"Lightning network payments which have been made, or have been forwarded, through this node")
	apiv1.Endpoint(
		lightningPayment,
		"send",
		`
		SendPayment sends payments through the Lightning Network
		`,
		withRpc(c, func(cc *LightningRPCServer, req *rpc_pb.SendRequest) (*rpc_pb.SendResponse, er.R) {
			return cc.SendPaymentSync(context.TODO(), req)
		}),
	)
	// TODO(cjd): Streaming
	// apiv1.Register(
	// 	a,
	// 	"/lightning/payment/payinvoice",
	// 	`
	// 	Send a payment over lightning

	// 	SendPaymentV2 attempts to route a payment described by the passed
	// 	PaymentRequest to the final destination. The call returns a stream of
	// 	payment updates.
	// 	`,
	// 	false,
	// 	withRouter(c, func(rs *routerrpc.Server, req *routerrpc_pb.SendPaymentRequest) (*rpc_pb.Null, er.R) {
	// 		return rs.SendPaymentV2(context.TODO(), req)
	// 	}),
	// )
	apiv1.Endpoint(
		lightningPayment,
		"sendtoroute",
		`
		Send a payment over a predefined route

		SendToRouteV2 attempts to make a payment via the specified route. This
		method differs from SendPayment in that it allows users to specify a full
		route manually. This can be used for things like rebalancing, and atomic
		swaps.
		`,
		withRouter(c, func(rs *routerrpc.Server, req *routerrpc_pb.SendToRouteRequest) (*rpc_pb.HTLCAttempt, er.R) {
			return rs.SendToRouteV2(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningPayment,
		"",
		`
		List all outgoing payments

    	ListPayments returns a list of all outgoing payments.
		`,
		withRpc(c, func(cc *LightningRPCServer, req *rpc_pb.ListPaymentsRequest) (*rpc_pb.ListPaymentsResponse, er.R) {
			return cc.ListPayments(context.TODO(), req)
		}),
	)
	// TODO(cjd): Streaming only
	// apiv1.Register(
	// 	a,
	// 	"/lightning/payment/track",
	// 	`
	// 	Track payment

	// 	TrackPaymentV2 returns an update stream for the payment identified by the
	// 	payment hash.
	// 	`,
	// 	false,
	// 	withRouter(c, func(rs *routerrpc.Server, req *routerrpc_pb.TrackPaymentRequest) (*rpc_pb.HTLCAttempt, er.R) {
	// 		return rs.TrackPaymentV2(context.TODO(), req)
	// 	}),
	// )
	apiv1.Endpoint(
		lightningPayment,
		"queryroutes",
		`
		Query a route to a destination

		QueryRoutes attempts to query the daemon's Channel Router for a possible
		route to a target destination capable of carrying a specific amount of
		satoshis. The returned route contains the full details required to craft and
		send an HTLC, also including the necessary information that should be
		present within the Sphinx packet encapsulated within the HTLC.
		`,
		withRpc(c, func(cc *LightningRPCServer, req *rpc_pb.QueryRoutesRequest) (*rpc_pb.QueryRoutesResponse, er.R) {
			return cc.QueryRoutes(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningPayment,
		"fwdinghistory",
		`
		Query the history of all forwarded HTLCs

		ForwardingHistory allows the caller to query the htlcswitch for a record of
		all HTLCs forwarded within the target time range, and integer offset
		within that time range. If no time-range is specified, then the first chunk
		of the past 24 hrs of forwarding history are returned.
	
		A list of forwarding events are returned. Each response has the index offset
		of the last entry. The index offset can be provided to the request to allow
		the caller to skip a series of records.
		`,
		withRpc(c, func(cc *LightningRPCServer, req *rpc_pb.ForwardingHistoryRequest) (*rpc_pb.ForwardingHistoryResponse, er.R) {
			return cc.ForwardingHistory(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningPayment,
		"querymc",
		`
		Query the internal mission control state

		QueryMissionControl exposes the internal mission control state to callers.
		It is a development feature.
		`,
		withRouter(c, func(rs *routerrpc.Server, req *rpc_pb.Null) (*routerrpc_pb.QueryMissionControlResponse, er.R) {
			return rs.QueryMissionControl(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningPayment,
		"queryprob",
		`
		Estimate a success probability

		QueryProbability returns the current success probability estimate for a
		given node pair and amount.
		`,
		withRouter(c, func(rs *routerrpc.Server, req *routerrpc_pb.QueryProbabilityRequest) (*routerrpc_pb.QueryProbabilityResponse, er.R) {
			return rs.QueryProbability(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningPayment,
		"resetmc",
		`
		Reset internal mission control state

		ResetMissionControl clears all mission control state and starts with a clean slate.
		`,
		withRouter(c, func(rs *routerrpc.Server, req *rpc_pb.Null) (*rpc_pb.Null, er.R) {
			return rs.ResetMissionControl(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningPayment,
		"buildroute",
		`
		Build a route from a list of hop pubkeys

		BuildRoute builds a fully specified route based on a list of hop public
		keys. It retrieves the relevant channel policies from the graph in order to
		calculate the correct fees and time locks.
		`,
		withRouter(c, func(rs *routerrpc.Server, req *routerrpc_pb.BuildRouteRequest) (*routerrpc_pb.BuildRouteResponse, er.R) {
			return rs.BuildRoute(context.TODO(), req)
		}),
	)

	//	>>> lightning/peer subCategory command

	lightningPeer := apiv1.DefineCategory(lightning, "peer", "Lightning nodes to which we are directly connected")
	apiv1.Endpoint(
		lightningPeer,
		"connect",
		`
		Connect to a remote pld peer

		ConnectPeer attempts to establish a connection to a remote peer. This is at
		the networking level, and is used for communication between nodes. This is
		distinct from establishing a channel with a peer.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ConnectPeerRequest) (*rpc_pb.Null, er.R) {
			return rs.ConnectPeer(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningPeer,
		"disconnect",
		`
		Disconnect a remote pld peer identified by public key

		DisconnectPeer attempts to disconnect one peer from another identified by a
		given pubKey. In the case that we currently have a pending or active channel
		with the target peer, then this action will be not be allowed.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.DisconnectPeerRequest) (*rpc_pb.Null, er.R) {
			return rs.DisconnectPeer(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningPeer,
		"",
		`
		List all active, currently connected peers

		ListPeers returns a verbose listing of all currently active peers.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ListPeersRequest) (*rpc_pb.ListPeersResponse, er.R) {
			return rs.ListPeers(context.TODO(), req)
		}),
	)

	lightningWatchtower := apiv1.DefineCategory(lightning, "watchtower",
		"Watchtowers identify and react to malicious activity on the Lightning Network")
	apiv1.Endpoint(
		lightningWatchtower,
		"",
		`
		Display information about all registered watchtowers
	
		ListTowers returns the list of watchtowers registered with the client.
		`,
		withWtclient(c, func(rs *wtclientrpc.WatchtowerClient, req *wtclientrpc_pb.ListTowersRequest) (*wtclientrpc_pb.ListTowersResponse, er.R) {
			return rs.ListTowers(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningWatchtower,
		"stats",
		`
		Display the session stats of the watchtower client

		Stats returns the in-memory statistics of the client since startup.
		`,
		withWtclient(c, func(rs *wtclientrpc.WatchtowerClient, req *rpc_pb.Null) (*wtclientrpc_pb.StatsResponse, er.R) {
			return rs.Stats(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningWatchtower,
		"create",
		`
		Register a watchtower to use for future sessions/backups
	
		AddTower adds a new watchtower reachable at the given address and
		considers it for new sessions. If the watchtower already exists, then
		any new addresses included will be considered when dialing it for
		session negotiations and backups.
		`,
		withWtclient(c, func(rs *wtclientrpc.WatchtowerClient, req *wtclientrpc_pb.AddTowerRequest) (*rpc_pb.Null, er.R) {
			return rs.AddTower(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningWatchtower,
		"delete",
		`
		Remove a watchtower to prevent its use for future sessions/backups
	
		RemoveTower removes a watchtower from being considered for future session
		negotiations and from being used for any subsequent backups until it's added
		again. If an address is provided, then this RPC only serves as a way of
		removing the address from the watchtower instead.
		`,
		withWtclient(c, func(rs *wtclientrpc.WatchtowerClient, req *wtclientrpc_pb.RemoveTowerRequest) (*rpc_pb.Null, er.R) {
			return rs.RemoveTower(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningWatchtower,
		"towerinfo",
		`
		Display information about a specific registered watchtower
		`,
		withWtclient(c, func(rs *wtclientrpc.WatchtowerClient, req *wtclientrpc_pb.GetTowerInfoRequest) (*wtclientrpc_pb.Tower, er.R) {
			return rs.GetTowerInfo(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		lightningWatchtower,
		"towerpolicy",
		`
		Display the active watchtower client policy configuration
		`,
		withWtclient(c, func(rs *wtclientrpc.WatchtowerClient, req *wtclientrpc_pb.PolicyRequest) (*wtclientrpc_pb.PolicyResponse, er.R) {
			return rs.Policy(context.TODO(), req)
		}),
	)

	//	>>> meta category command
	meta := apiv1.DefineCategory(a, "meta",
		"API endpoints which are relevant to the entire pld node, not any specific module")
	apiv1.Endpoint(
		meta,
		"debuglevel",
		`
		Set the debug level

		DebugLevel allows a caller to programmatically set the logging verbosity of
		lnd. The logging can be targeted according to a coarse daemon-wide logging
		level, or in a granular fashion to specify the logging for a target
		sub-system.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.DebugLevelRequest) (*rpc_pb.Null, er.R) {
			return rs.DebugLevel(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		meta,
		"stop",
		`
		Stop and shutdown the daemon

		StopDaemon will send a shutdown request to the interrupt handler, triggering
		a graceful shutdown of the daemon.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.Null, er.R) {
			return rs.StopDaemon(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		meta,
		"version",
		`
		Display pld version info

		GetVersion returns the current version and build information of the running
		daemon.
		`,
		func(req *rpc_pb.Null) (*verrpc_pb.Version, er.R) {
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
		},
	)

	neutrino := a.Category("neutrino")
	apiv1.Endpoint(
		neutrino,
		"bcasttransaction",
		`
		Broadcast a transaction to the network

		Broadcast a transaction to the network so it can be logged in the chain.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.BcastTransactionRequest) (*rpc_pb.BcastTransactionResponse, er.R) {
			return rs.BcastTransaction(context.TODO(), req)
		}),
	)

	// We're not doing estimatefee because it is unreliable and a bad API
	// apiv1.Endpoint(
	// 	neutrino,
	// 	"estimatefee",
	// 	`
	// 	Get fee estimates for sending coins on-chain to one or more addresses

	// 	`,
	// 	withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.EstimateFeeRequest) (*rpc_pb.EstimateFeeResponse, er.R) {
	// 		return rs.EstimateFee(context.TODO(), req)
	// 	}),
	// )

	apiv1.DefineCategory(a, "cjdns", "Cjdns RPCs")
}

type RpcContext struct {
	MaybeRpcServer        *LightningRPCServer
	MaybeMetaService      *lnrpc.MetaService
	MaybeRouterServer     *routerrpc.Server
	MaybeWatchTowerClient *wtclientrpc.WatchtowerClient
}

func withRpc[Q, R proto.Message](
	c *RpcContext,
	f func(*LightningRPCServer, Q) (R, er.R),
) func(Q) (R, er.R) {
	return func(q Q) (R, er.R) {
		if c.MaybeRpcServer == nil {
			var none R
			return none, er.Errorf("Could not call function because LightningRPCServer is not yet ready")
		}
		return f(c.MaybeRpcServer, q)
	}
}
func withMeta[Q, R proto.Message](
	c *RpcContext,
	f func(*lnrpc.MetaService, Q) (R, er.R),
) func(Q) (R, er.R) {
	return func(q Q) (R, er.R) {
		if c.MaybeMetaService == nil {
			var none R
			return none, er.Errorf("Could not call function because LightningRPCServer is not yet ready")
		}
		return f(c.MaybeMetaService, q)
	}
}
func withRouter[Q, R proto.Message](
	c *RpcContext,
	f func(*routerrpc.Server, Q) (R, er.R),
) func(Q) (R, er.R) {
	return func(q Q) (R, er.R) {
		if c.MaybeRouterServer == nil {
			var none R
			return none, er.Errorf("Could not call function because RouterServer is not yet ready")
		}
		return f(c.MaybeRouterServer, q)
	}
}
func withWtclient[Q, R proto.Message](
	c *RpcContext,
	f func(*wtclientrpc.WatchtowerClient, Q) (R, er.R),
) func(Q) (R, er.R) {
	return func(q Q) (R, er.R) {
		if c.MaybeWatchTowerClient == nil {
			var none R
			return none, er.Errorf("Could not call function because MaybeWatchTowerClient is not yet ready")
		}
		return f(c.MaybeWatchTowerClient, q)
	}
}
