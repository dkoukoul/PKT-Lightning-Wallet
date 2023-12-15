package apiv1

import (
	"github.com/pkt-cash/pktd/apiv1/lightning"
	"github.com/pkt-cash/pktd/apiv1/meta"
	api_neutrino "github.com/pkt-cash/pktd/apiv1/neutrino"
	"github.com/pkt-cash/pktd/apiv1/util"
	api_wallet "github.com/pkt-cash/pktd/apiv1/wallet"
	"github.com/pkt-cash/pktd/btcutil/util/mailbox"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/neutrino"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
)

func Register(
	a *apiv1.Apiv1,
	w *wallet.Wallet,
	neutrinoCS *neutrino.ChainService,
	startLightning *mailbox.Mailbox[*lightning.StartLightning],
) {
	api_wallet.Register(
		apiv1.DefineCategory(a, "wallet", "APIs for management of on-chain (non-Lightning) payments"),
		w,
		startLightning,
	)
	lightning.Register(
		apiv1.DefineCategory(a, "lightning", "The Lightning daemon component"),
		w,
		neutrinoCS,
		startLightning,
	)
	util.Register(
		apiv1.DefineCategory(a, "util",
			"Stateless utility functions which do not affect, not query, the node in any way"),
		w.ChainParams(),
	)
	api_neutrino.Register(
		apiv1.DefineCategory(a, "neutrino",
			"The Neutrino interface which is used to communicate with the p2p nodes in the network"),
		w,
	)
	meta.Register(
		apiv1.DefineCategory(a, "meta",
			"API endpoints which are relevant to the entire pld node, not any specific module"),
		neutrinoCS,
		w,
	)
}
