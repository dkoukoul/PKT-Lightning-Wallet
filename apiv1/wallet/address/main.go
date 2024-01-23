package address

import (
	"bytes"
	"encoding/base64"

	"github.com/pkt-cash/pktd/btcec"
	"github.com/pkt-cash/pktd/btcutil"
	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/chaincfg/chainhash"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/pktwallet/waddrmgr"
	"github.com/pkt-cash/pktd/pktwallet/wallet"
	"github.com/pkt-cash/pktd/wire"
)

type rpc struct {
	w *wallet.Wallet
}

func (r *rpc) resync(req *rpc_pb.ReSyncChainRequest) (*rpc_pb.Null, er.R) {
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
	err := r.w.ResyncChain(fh, th, a, drop)
	return nil, err
}

func (r *rpc) stopresync(req *rpc_pb.Null) (*rpc_pb.Null, er.R) {
	_, err := r.w.StopResync()
	return nil, err
}

func (r *rpc) _import(req *rpc_pb.ImportPrivKeyRequest) (*rpc_pb.ImportPrivKeyResponse, er.R) {
	wif, err := btcutil.DecodeWIF(req.PrivateKey)
	if err != nil {
		return nil, err
	}
	if !wif.IsForNet(r.w.ChainParams()) {
		// If the wif is for the wrong chain, lets attempt to import it anyway
		var err er.R
		wif, err = btcutil.NewWIF(wif.PrivKey, r.w.ChainParams(), wif.CompressPubKey)
		if err != nil {
			return nil, err
		}
	}

	scope := waddrmgr.KeyScopeBIP0084
	if req.Legacy {
		scope = waddrmgr.KeyScopeBIP0044
	}

	// Import the private key, handling any errors.
	addr, err := r.w.ImportPrivateKey(scope, wif, nil, req.Rescan)
	switch {
	case waddrmgr.ErrLocked.Is(err):
		return nil, er.New("ErrRPCWalletUnlockNeeded: -13 Enter the wallet passphrase with walletpassphrase first")
	}

	return &rpc_pb.ImportPrivKeyResponse{
		Address: addr,
	}, err
}

func (r *rpc) balances(
	in *rpc_pb.GetAddressBalancesRequest,
) (*rpc_pb.GetAddressBalancesResponse, er.R) {
	if adb, err := r.w.CalculateAddressBalances(in.Minconf, in.Showzerobalance); err != nil {
		return nil, err
	} else {
		resp := make([]*rpc_pb.GetAddressBalancesResponseAddr, 0, len(adb))
		for k, v := range adb {
			vote, err := r.w.GetVote(k)
			if err != nil {
				return nil, err
			}
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
				Vote:            vote,
			})
		}
		return &rpc_pb.GetAddressBalancesResponse{Addrs: resp}, nil
	}
}

func (r *rpc) create(req *rpc_pb.GetNewAddressRequest) (*rpc_pb.GetNewAddressResponse, er.R) {
	scope := waddrmgr.KeyScopeBIP0084
	if req.Legacy {
		scope = waddrmgr.KeyScopeBIP0044
	}
	if addr, err := r.w.NewAddress(waddrmgr.DefaultAccountNum, scope); err != nil {
		return nil, err
	} else {
		return &rpc_pb.GetNewAddressResponse{
			Address: addr.EncodeAddress(),
		}, nil
	}
}

func (r *rpc) dumpprivkey(req *rpc_pb.DumpPrivKeyRequest) (*rpc_pb.DumpPrivKeyResponse, er.R) {
	addr, err := decodeAddress(req.Address, r.w.ChainParams())
	if err != nil {
		return nil, err
	}
	key, err := r.w.DumpWIFPrivateKey(addr)
	if waddrmgr.ErrLocked.Is(err) {
		// Address was found, but the private key isn't
		// accessible.
		return nil, er.New("ErrRPCWalletUnlockNeeded -13 Enter the wallet passphrase with walletpassphrase first")
	}
	return &rpc_pb.DumpPrivKeyResponse{
		PrivateKey: key,
	}, nil
}

func (r *rpc) signmessage(in *rpc_pb.SignMessageRequest) (*rpc_pb.SignMessageResponse, er.R) {

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

	addr, err := decodeAddress(in.Address, r.w.ChainParams())
	if err != nil {
		return nil, err
	}

	privKey, err := r.w.PrivKeyForAddress(addr)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	wire.WriteVarString(&buf, 0, "Bitcoin Signed Message:\n")
	buf.Write(msg)
	messageHash := chainhash.DoubleHashB(buf.Bytes())
	sigbytes, err := btcec.SignCompact(btcec.S256(), privKey,
		messageHash, true)
	if err != nil {
		return nil, err
	}

	return &rpc_pb.SignMessageResponse{
		Signature: base64.StdEncoding.EncodeToString(sigbytes),
	}, nil
}

func Register(
	a *apiv1.Apiv1,
	w *wallet.Wallet,
) {
	r := rpc{w: w}
	apiv1.Endpoint(
		a,
		"resync",
		`
		Re-scan the chain for transactions

		Scan the chain for transactions which may not have been recorded in the wallet's
		database. This endpoint returns instantly and completes in the background.
		Use meta/getinfo to follow up on the status.
		`,
		r.resync,
	)
	apiv1.Endpoint(
		a,
		"stopresync",
		`
		Stop the currently active resync job

		Only one resync job can take place at a time, this will stop the active one if any.
		This endpoint errors if there is no currently active resync job.
		Check meta/getinfo to see if there is a resync job ongoing.
		`,
		r.stopresync,
	)
	apiv1.Endpoint(
		a,
		"balances",
		`
		Compute and display balances for each address in the wallet

		This computes and returns the current balances of every address, as well as the
		number of unspent outputs, unconfirmed coins and other information.
		In a wallet with many outputs, this endpoint can take a long time.
		`,
		r.balances,
	)
	apiv1.Endpoint(
		a,
		"create",
		`
		Generates a new address

		Generates a new payment address
		`,
		r.create,
	)
	apiv1.Endpoint(
		a,
		"dumpprivkey",
		`
		Returns the private key that controls a wallet address

		Returns the private key in WIF encoding that controls some wallet address.
		Note that if the private key of an address falls into the wrong hands, all
		funds on THAT ADDRESS can be stolen. However no other addresses in the wallet
		are affected.
		`,
		r.dumpprivkey,
	)
	apiv1.Endpoint(
		a,
		"import",
		`
		Imports a WIF-encoded private key

		Imports a WIF-encoded private key to the wallet.
		Funds from this key/address will be spendable once it is imported.
		NOTE: Imported addresses will NOT be recovered if you recover your
		wallet from seed as they are not mathmatically derived from the seed.
		`,
		r._import,
	)
	apiv1.Endpoint(
		a,
		"signmessage",
		`
		Signs a message using the private key of a payment address

		SignMessage signs a message with an address's private key. The returned
		signature string can be verified using a utility such as:
		https://github.com/cjdelisle/pkt-checksig

		NOTE: Only legacy style addresses (mixed capital and lower case letters,
		beginning with a 'p') can currently be used to sign messages.
		`,
		r.signmessage,
	)
}
