package address

import (
	"fmt"

	"github.com/pkt-cash/pktd/btcjson"
	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg"
)

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
