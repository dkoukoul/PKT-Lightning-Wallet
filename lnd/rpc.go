// NOTICE: This entire file is DEPRECATED
// Please avoid adding new endpoints to this file, instead add them where
// the business logic is. See pktwallet/wallet.go for an example of how to
// do this well.
// Endpoints which are relevant to different business logic should be moved
// where appropriate.
package lnd

import (
	"context"
	"strconv"

	"github.com/pkt-cash/pktd/btcjson"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/connmgr/banmgr"
	"github.com/pkt-cash/pktd/generated/proto/meta_pb"
	"github.com/pkt-cash/pktd/generated/proto/restrpc_pb/help_pb"
	"github.com/pkt-cash/pktd/generated/proto/routerrpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/verrpc_pb"
	"github.com/pkt-cash/pktd/generated/proto/walletunlocker_pb"
	"github.com/pkt-cash/pktd/generated/proto/wtclientrpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/lnd/lnrpc/routerrpc"
	"github.com/pkt-cash/pktd/lnd/lnrpc/wtclientrpc"
	"github.com/pkt-cash/pktd/lnd/walletunlocker"
	"github.com/pkt-cash/pktd/neutrino"
	"github.com/pkt-cash/pktd/pktconfig/version"
	"github.com/pkt-cash/pktd/pktwallet/waddrmgr"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
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
		"getinfo",
		`
		Returns basic information related to the active daemon

		GetInfo returns general information concerning the lightning node including
		it's identity pubkey, alias, the chains it is connected to, and information
		concerning the number of open+pending channels.
		`,
		func(m *rpc_pb.Null) (*meta_pb.GetInfo2Response, er.R) {
			var ni rpc_pb.NeutrinoInfo
			if n := c.MaybeNeutrino; n != nil {
				neutrinoPeers := n.Peers()
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
				n.BanMgr().ForEachIp(func(bi banmgr.BanInfo) er.R {
					ban := rpc_pb.NeutrinoBan{}
					ban.Addr = bi.Addr
					ban.Reason = bi.Reason
					ban.EndTime = bi.BanExpiresTime.String()
					ban.BanScore = bi.BanScore

					ni.Bans = append(ni.Bans, &ban)
					return nil
				})

				neutrionoQueries := n.GetActiveQueries()
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

				bb, err := n.BestBlock()
				if err != nil {
					return nil, err
				}
				ni.BlockHash = bb.Hash.String()
				ni.Height = bb.Height
				ni.BlockTimestamp = bb.Timestamp.String()
				ni.IsSyncing = !n.IsCurrent()
			}

			var walletInfo *rpc_pb.WalletInfo
			if w := c.MaybeWallet; w != nil {
				mgrStamp := w.Manager.SyncedTo()
				walletStats := &rpc_pb.WalletStats{}
				w.ReadStats(func(ws *btcjson.WalletStats) {
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
			}

			// Get Lightning info
			var lightning *rpc_pb.GetInfoResponse
			if cc := c.MaybeRpcServer; cc != nil {
				if l, err := cc.GetInfo(context.TODO(), nil); err != nil {
					return nil, er.E(err)
				} else {
					lightning = l
				}
			}

			return &meta_pb.GetInfo2Response{
				Neutrino:  &ni,
				Wallet:    walletInfo,
				Lightning: lightning,
			}, nil
		},
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

	// wallet category commands
	wallet := apiv1.DefineCategory(a, "wallet", "APIs for management of on-chain (non-Lightning) payments")
	apiv1.Endpoint(
		wallet,
		"balance",
		`
		Compute and display the wallet's current balance

		WalletBalance returns total unspent outputs(confirmed and unconfirmed), all
		confirmed unspent outputs and all unconfirmed unspent outputs under control
		of the wallet.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.WalletBalanceResponse, er.R) {
			return rs.WalletBalance(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		wallet,
		"changepassphrase",
		`
		Change an encrypted wallet's password at startup

		ChangePassword changes the password of the encrypted wallet. This will
		automatically unlock the wallet database if successful.
		`,
		withMeta(c, func(rs *lnrpc.MetaService, req *meta_pb.ChangePasswordRequest) (*rpc_pb.Null, er.R) {
			return rs.ChangePassword(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		wallet,
		"checkpassphrase",
		`
		Check the wallet's password

    	CheckPassword verify that the password in the request is valid for the wallet.
		`,
		withMeta(c, func(rs *lnrpc.MetaService, req *meta_pb.CheckPasswordRequest) (*meta_pb.CheckPasswordResponse, er.R) {
			return rs.CheckPassword(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		wallet,
		"create",
		`
		Initialize a wallet when starting lnd for the first time

		This is used when lnd is starting up for the first time to fully
		initialize the daemon and its internal wallet. At the very least a wallet
		password must be provided. This will be used to encrypt sensitive material
		on disk.
	
		In the case of a recovery scenario, the user can also specify their aezeed
		mnemonic and passphrase. If set, then the daemon will use this prior state
		to initialize its internal wallet.
	
		Alternatively, this can be used along with the /util/seed/create RPC to
		obtain a seed, then present it to the user. Once it has been verified by
		the user, the seed can be fed into this RPC in order to commit the new
		wallet.
		`,
		withUnlocker(c, func(rs *walletunlocker.UnlockerService, req *walletunlocker_pb.InitWalletRequest) (*rpc_pb.Null, er.R) {
			return rs.InitWallet(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		wallet,
		"unlock",
		`
		Unlock an encrypted wallet at startup

		Required at startup of pld to provide a password to unlock the wallet database.
		`,
		withUnlocker(c, func(rs *walletunlocker.UnlockerService, req *walletunlocker_pb.UnlockWalletRequest) (*rpc_pb.Null, er.R) {
			return rs.UnlockWallet(context.TODO(), req)
		}),
	)

	//	>>> wallet/networkstewardvote subCategory command
	walletNetworkStewardVote := apiv1.DefineCategory(wallet, "networkstewardvote",
		"Control how this wallet votes on PKT Network Steward")
	apiv1.Endpoint(
		walletNetworkStewardVote,
		"",
		`
		Find out how the wallet's currently configured to vote

		Find out how the wallet is currently configured to vote in a network steward election.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.GetNetworkStewardVoteResponse, er.R) {
			return rs.GetNetworkStewardVote(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletNetworkStewardVote,
		"set",
		`
		Configure the wallet to vote for a network steward

		Configure the wallet to vote for a network steward when making payments (note: payments to segwit addresses cannot vote)
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.SetNetworkStewardVoteRequest) (*rpc_pb.Null, er.R) {
			return rs.SetNetworkStewardVote(context.TODO(), req)
		}),
	)

	//	>>> wallet/transaction subCategory command
	walletTransaction := apiv1.DefineCategory(wallet, "transaction",
		"Create and manage on-chain transactions with the wallet")
	apiv1.Endpoint(
		walletTransaction,
		"",
		`
		Get details regarding a transaction

    	Returns a JSON object with details regarding a transaction relevant to this wallet.
		If the transaction is not known to be relevant to at least one address in this wallet
		it will appear as "not found" even if the transaction is real.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.GetTransactionRequest) (*rpc_pb.GetTransactionResponse, er.R) {
			return rs.GetTransaction(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletTransaction,
		"create",
		`
		Create a transaction but do not send it to the chain

		This does not store the transaction as existing in the wallet so
		/wallet/transaction/query will not return a transaction created by this
		endpoint. In order to make multiple transactions concurrently, prior to
		the first transaction being submitted to the chain, you must specify the
		autolock field.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.CreateTransactionRequest) (*rpc_pb.CreateTransactionResponse, er.R) {
			return rs.CreateTransaction(context.TODO(), req)
		}),
	)
	// TODO(cjd): I don't like this endpoint, use sendfrom
	// apiv1.Endpoint(
	// 	walletTransaction,
	// 	"sendcoins",
	// 	`
	// 	Send bitcoin on-chain to an address

	// 	SendCoins executes a request to send coins to a particular address. Unlike
	// 	SendMany, this RPC call only allows creating a single output at a time. If
	// 	neither target_conf, or sat_per_byte are set, then the internal wallet will
	// 	consult its fee model to determine a fee for the default confirmation
	// 	target.
	// 	`,
	// 	withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.SendCoinsRequest) (*rpc_pb.SendCoinsResponse, er.R) {
	// 		return rs.SendCoins(context.TODO(), req)
	// 	}),
	// )
	apiv1.Endpoint(
		walletTransaction,
		"sendfrom",
		`
		Authors, signs, and sends a transaction which sources funds from specific addresses

		SendFrom authors, signs, and sends a transaction which sources it's funds
		from specific addresses.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.SendFromRequest) (*rpc_pb.SendFromResponse, er.R) {
			return rs.SendFrom(context.TODO(), req)
		}),
	)
	// TODO(cjd): This is not written right, needs to be addressed
	// apiv1.Endpoint(
	// 	walletTransaction,
	// 	"sendmany",
	// 	`
	// 	Send PKT on-chain to multiple addresses

	// 	SendMany handles a request for a transaction that creates multiple specified
	// 	outputs in parallel. If neither target_conf, or sat_per_byte are set, then
	// 	the internal wallet will consult its fee model to determine a fee for the
	// 	default confirmation target.
	// 	`,
	// 	withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.SendManyRequest) (*rpc_pb.SendManyResponse, er.R) {
	// 		return rs.SendMany(context.TODO(), req)
	// 	}),
	// )
	apiv1.Endpoint(
		walletTransaction,
		"decode",
		`
		Parse a binary representation of a transaction into it's relevant data

		Parse a binary or hex encoded transaction and returns a structured description of it.
		This endpoint also uses information from the wallet, if possible, to fill in additional
		data such as the amounts of the transaction inputs - data which is not present inside of the
		transaction itself. If the relevant data is not in the wallet, some info about the transaction
		will be missing such as input amounts and fees.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.DecodeRawTransactionRequest) (*rpc_pb.TransactionInfo, er.R) {
			return rs.DecodeRawTransaction(context.TODO(), req)
		}))

	walletUnspent := apiv1.DefineCategory(wallet, "unspent",
		"Detected unspent transactions associated with one of our wallet addresses")
	apiv1.Endpoint(
		walletUnspent,
		"",
		`
		List utxos available for spending

		ListUnspent returns a list of all utxos spendable by the wallet with a
		number of confirmations between the specified minimum and maximum.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ListUnspentRequest) (*rpc_pb.ListUnspentResponse, er.R) {
			return rs.ListUnspent(context.TODO(), req)
		}),
	)

	walletUnspentLock := apiv1.DefineCategory(walletUnspent, "lock",
		`
		Unspent outputs which are locked

		Locking of unspent outputs prevent them from being used as funding for transaction/create
		or transaction/sendcoins, etc. This is useful when creating multiple transactions which are
		not sent to the chain (yet). Locking the outputs will prevent each subsequent transaction
		from trying to source the same funds, making mutually invalid transactions.

		Locked outputs can be grouped with "named" locks, so that they can be unlocked as a group.
		This is useful when one transaction may source many unspent outputs, they can be locked
		with the name/purpose of that transaction.
		`)
	apiv1.Endpoint(
		walletUnspentLock,
		"",
		`
		List utxos which are locked

		Returns an set of outpoints marked as locked by using /wallet/unspent/lock/create
		These are batched by group name.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.ListLockUnspentResponse, er.R) {
			return rs.ListLockUnspent(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletUnspentLock,
		"create",
		`
		Lock one or more unspent outputs

		You may optionally specify a group name. You may call this endpoint
		multiple times with the same group name to add more unspents to the group.
		NOTE: The lock group name "none" is reserved.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.LockUnspentRequest) (*rpc_pb.Null, er.R) {
			return rs.LockUnspent(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletUnspentLock,
		"delete",
		`
		Remove one or a group of locks

		If a lock name is specified, all locks with that name will be unlocked
		in addition to all unspents that are specifically identified. If the literal
		word "none" is specified as the lock name, all uncategorized locks will be removed.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.LockUnspentRequest) (*rpc_pb.Null, er.R) {
			return rs.UnlockUnspent(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletUnspentLock,
		"deleteall",
		`
		Remove every lock, including all categories.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.Null, er.R) {
			return rs.UnlockAllUnspent(context.TODO(), req)
		}),
	)

	walletAddress := apiv1.DefineCategory(wallet, "address",
		`
		Management of PKT addresses in the wallet

		The root keys of this wallet can be used to derive as many addresses as you need.
		If you recover your wallet from seed, all of the same addresses will derive again
		in the same order. The public does not know that these addresses are linked to the
		same wallet unless you spend from multiple of them in the same transaction.

		Each address can be pay, be paid, hold a balance, and generally be used as it's own
		wallet.
		`)
	apiv1.Endpoint(
		walletAddress,
		"resync",
		`
		Re-scan the chain for transactions

		Scan the chain for transactions which may not have been recorded in the wallet's
		database. This endpoint returns instantly and completes in the background.
		Use meta/getinfo to follow up on the status.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ReSyncChainRequest) (*rpc_pb.Null, er.R) {
			return rs.ReSync(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletAddress,
		"stopresync",
		`
		Stop the currently active resync job

		Only one resync job can take place at a time, this will stop the active one if any.
		This endpoint errors if there is no currently active resync job.
		Check meta/getinfo to see if there is a resync job ongoing.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.Null) (*rpc_pb.Null, er.R) {
			return rs.StopReSync(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletAddress,
		"balances",
		`
		Compute and display balances for each address in the wallet

		This computes and returns the current balances of every address, as well as the
		number of unspent outputs, unconfirmed coins and other information.
		In a wallet with many outputs, this endpoint can take a long time.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.GetAddressBalancesRequest) (*rpc_pb.GetAddressBalancesResponse, er.R) {
			return rs.GetAddressBalances(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletAddress,
		"create",
		`
		Generates a new address

		Generates a new payment address
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.GetNewAddressRequest) (*rpc_pb.GetNewAddressResponse, er.R) {
			return rs.GetNewAddress(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletAddress,
		"dumpprivkey",
		`
		Returns the private key that controls a wallet address

		Returns the private key in WIF encoding that controls some wallet address.
		Note that if the private key of an address falls into the wrong hands, all
		funds on THAT ADDRESS can be stolen. However no other addresses in the wallet
		are affected.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.DumpPrivKeyRequest) (*rpc_pb.DumpPrivKeyResponse, er.R) {
			return rs.DumpPrivKey(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletAddress,
		"import",
		`
		Imports a WIF-encoded private key

		Imports a WIF-encoded private key to the wallet.
		Funds from this key/address will be spendable once it is imported.
		NOTE: Imported addresses will NOT be recovered if you recover your
		wallet from seed as they are not mathmatically derived from the seed.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ImportPrivKeyRequest) (*rpc_pb.ImportPrivKeyResponse, er.R) {
			return rs.ImportPrivKey(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		walletAddress,
		"signmessage",
		`
		Signs a message using the private key of a payment address

		SignMessage signs a message with an address's private key. The returned
		signature string can be verified using a utility such as:
		https://github.com/cjdelisle/pkt-checksig

		NOTE: Only legacy style addresses (mixed capital and lower case letters,
		beginning with a 'p') can currently be used to sign messages.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.SignMessageRequest) (*rpc_pb.SignMessageResponse, er.R) {
			return rs.SignMessage(context.TODO(), req)
		}),
	)

	neutrino := apiv1.DefineCategory(a, "neutrino",
		"The Neutrino interface which is used to communicate with the p2p nodes in the network")
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

	util := apiv1.DefineCategory(a, "util",
		"Stateless utility functions which do not affect, not query, the node in any way")
	utilSeed := apiv1.DefineCategory(util, "seed",
		"Manipulation of mnemonic seed phrases which represent wallet keys")
	apiv1.Endpoint(
		utilSeed,
		"changepassphrase",
		`
		Alter the passphrase which is used to encrypt a wallet seed

		The old seed words are transformed into a new seed words,
		representing the same seed but encrypted with a different passphrase.
		`,
		withRpc(c, func(rs *LightningRPCServer, req *rpc_pb.ChangeSeedPassphraseRequest) (*rpc_pb.ChangeSeedPassphraseResponse, er.R) {
			return rs.ChangeSeedPassphrase(context.TODO(), req)
		}),
	)
	apiv1.Endpoint(
		utilSeed,
		"create",
		`
		Create a secret seed

		This allows you to statelessly create a new wallet seed.
		This seed can then be used to initialize a wallet.
		`,
		withUnlocker(c, func(rs *walletunlocker.UnlockerService, req *walletunlocker_pb.GenSeedRequest) (*walletunlocker_pb.GenSeedResponse, er.R) {
			return rs.GenSeed0(context.TODO(), req)
		}),
	)
	apiv1.DefineCategory(a, "cjdns", "Cjdns RPCs")
}

type RpcContext struct {
	MaybeNeutrino         *neutrino.ChainService
	MaybeWallet           *wallet.Wallet
	MaybeRpcServer        *LightningRPCServer
	MaybeWalletUnlocker   *walletunlocker.UnlockerService
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
func withUnlocker[Q, R proto.Message](
	c *RpcContext,
	f func(*walletunlocker.UnlockerService, Q) (R, er.R),
) func(Q) (R, er.R) {
	return func(q Q) (R, er.R) {
		if c.MaybeWalletUnlocker == nil {
			var none R
			return none, er.Errorf("Could not call function because LightningRPCServer is not yet ready")
		}
		return f(c.MaybeWalletUnlocker, q)
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
