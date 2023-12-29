package sendtxstatus

import (
	"time"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/event"
	"github.com/pkt-cash/pktd/btcutil/lock"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
)

type status struct {
	st          rpc_pb.SendTxStatus
	lastUpdated time.Time
}

type SendTxStatus struct {
	statuses lock.AtomicMap[string, status]
	ee       event.Emitter[*rpc_pb.SendTxUpdate]
}

func movePeer(sts *rpc_pb.SendTxStatus, list2 *[]string, item string) {
	f := func(p string) bool { return p != item }
	sts.PeersAcknoledged = util.Filter(sts.PeersAcknoledged, f)
	sts.PeersNotified = util.Filter(sts.PeersNotified, f)
	sts.PeersRemaining = util.Filter(sts.PeersRemaining, f)
	sts.PeersRequested = util.Filter(sts.PeersRequested, f)
	sts.PeersSent = util.Filter(sts.PeersSent, f)
	sts.Rejections = util.Filter(sts.Rejections, func(rej *rpc_pb.Rejection) bool {
		return rej.Peer != item
	})
	if list2 != nil {
		*list2 = append(*list2, item)
	}
}

func (s *SendTxStatus) update(txid string, f func(sts *rpc_pb.SendTxStatus)) {
	s.statuses.Update(txid, func(sts *status, exists bool) bool {
		if !exists {
			return false
		}
		sts.lastUpdated = time.Now()
		f(&sts.st)
		return true
	})
	s.statuses.Retain(func(txid string, sts *status) bool {
		return sts.lastUpdated.Add(time.Minute * 10).After(time.Now())
	})
}

func (s *SendTxStatus) Init(txid string, peers []string) er.R {
	s.statuses.Put(txid, status{
		st: rpc_pb.SendTxStatus{
			Txid:           txid,
			PeersRemaining: append([]string{}, peers...),
		},
		lastUpdated: time.Now(),
	})
	return s.ee.TryEmit(&rpc_pb.SendTxUpdate{
		Txid:  txid,
		Event: rpc_pb.SendTxEvent_INIT,
		Peers: append([]string{}, peers...),
	})
}
func (s *SendTxStatus) Notified(txid string, peer string) er.R {
	s.update(txid, func(sts *rpc_pb.SendTxStatus) { movePeer(sts, &sts.PeersNotified, peer) })
	return s.ee.TryEmit(&rpc_pb.SendTxUpdate{
		Txid:  txid,
		Event: rpc_pb.SendTxEvent_NOTIFIED,
		Peers: []string{peer},
	})
}
func (s *SendTxStatus) Requested(txid string, peer string) er.R {
	s.update(txid, func(sts *rpc_pb.SendTxStatus) { movePeer(sts, &sts.PeersRequested, peer) })
	return s.ee.TryEmit(&rpc_pb.SendTxUpdate{
		Txid:  txid,
		Event: rpc_pb.SendTxEvent_REQUESTED,
		Peers: []string{peer},
	})
}
func (s *SendTxStatus) Sent(txid string, peer string) er.R {
	s.update(txid, func(sts *rpc_pb.SendTxStatus) { movePeer(sts, &sts.PeersSent, peer) })
	return s.ee.TryEmit(&rpc_pb.SendTxUpdate{
		Txid:  txid,
		Event: rpc_pb.SendTxEvent_SENT,
		Peers: []string{peer},
	})
}
func (s *SendTxStatus) Acknoledged(txid string, peer string) er.R {
	s.update(txid, func(sts *rpc_pb.SendTxStatus) { movePeer(sts, &sts.PeersAcknoledged, peer) })
	return s.ee.TryEmit(&rpc_pb.SendTxUpdate{
		Txid:  txid,
		Event: rpc_pb.SendTxEvent_ACKNOLEDGED,
		Peers: []string{peer},
	})
}
func (s *SendTxStatus) Rejected(txid, peer, reason string) er.R {
	s.update(txid, func(sts *rpc_pb.SendTxStatus) {
		movePeer(sts, nil, peer)
		sts.Rejections = append(sts.Rejections, &rpc_pb.Rejection{
			Peer:   peer,
			Reason: reason,
		})
	})
	return s.ee.TryEmit(&rpc_pb.SendTxUpdate{
		Txid:   txid,
		Event:  rpc_pb.SendTxEvent_REJECTED,
		Peers:  []string{peer},
		Detail: reason,
	})
}
func RegisterNew(a *apiv1.Apiv1) *SendTxStatus {
	sts := SendTxStatus{
		ee: event.NewEmitter[*rpc_pb.SendTxUpdate]("SendTxStatus emitter"),
	}
	apiv1.Endpoint(
		a,
		"sending",
		`
		Status update events of transactions which are being sent on chain
		`,
		func(_ *rpc_pb.Null) (*rpc_pb.TransactionsInFlight, er.R) {
			items := sts.statuses.Items()
			out := rpc_pb.TransactionsInFlight{
				Transactions: make([]*rpc_pb.SendTxStatus, 0, len(items)),
			}
			for _, it := range items {
				out.Transactions = append(out.Transactions, &it.V.st)
			}
			return &out, nil
		},
	)
	apiv1.Stream(
		a,
		"sending",
		`
		Status update events of transactions which are being sent on chain
		`,
		&sts.ee,
		func(_ *rpc_pb.Null) (func(sts *rpc_pb.SendTxUpdate) bool, er.R) {
			return func(sts *rpc_pb.SendTxUpdate) bool {
				return true
			}, nil
		},
	)
	return &sts
}
