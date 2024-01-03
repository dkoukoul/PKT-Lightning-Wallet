package util

import (
	"github.com/pkt-cash/pktd/apiv1/util/seed"
	"github.com/pkt-cash/pktd/chaincfg"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
)

func Register(
	a *apiv1.Apiv1,
	params *chaincfg.Params,
) {
	seed.Register(
		apiv1.DefineCategory(a, "seed",
			"Manipulation of mnemonic seed phrases which represent wallet keys"),
		params,
	)
}
