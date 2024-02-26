package cjdns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/lnd/lnrpc/apiv1"
	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/zeebo/bencode"
)

type Cjdns struct {
	SocketPath          string
	Socket              net.Conn
	Device              string
	IPv6                string
	ListeningPort       int
	ListenConn          *net.UDPConn
	api                 apiv1.Apiv1
	lndPeers            []CjdnsNode
	socketLock          sync.Mutex
	invoiceResponseChan chan InvoiceResponse
}

type LndPubkeyRequest struct {
	CjdnsAddr   string
	CjdnsPubKey string
	Txid        string
}

type LndPubkeyResponse struct {
	CjdnsAddr string
	lndPubkey string
	Txid      string
}
type InvoiceRequest struct {
	CjdnsAddr   string
	CjdnsPubKey string
	Txid        string
	Amount      uint64
}

type CjdnsNode struct {
	CjdnsAddr   string
	CjdnsPubKey string
	lndPubkey   string
}

type InvoiceResponse struct {
	RHash          []byte `json:"rHash"`
	PaymentRequest string `json:"paymentRequest"`
	AddIndex       string `json:"addIndex"`
	Txid           string `json:"txid"`
	Error          string `json:"error"`
}

type CjdnsPeer struct {
	addr   string
	lladdr string
	state  string
}

type CjdnsMessage struct {
	invoiceReq InvoiceRequest
	invoiceRes InvoiceResponse
	lndPubReq  LndPubkeyRequest
	lndPubRes  LndPubkeyResponse
}

// var CjdnsInvoiceResponse InvoiceResponse

// var lndPubkeyResponse LndPubkeyResponse

type lndRpcServer interface {
	LndListPeers(ctx context.Context, in *rpc_pb.ListPeersRequest) (*rpc_pb.ListPeersResponse, er.R)
	LndConnectPeer(ctx context.Context, in *rpc_pb.ConnectPeerRequest) (*rpc_pb.Null, er.R)
	LndAddInvoice(ctx context.Context, in *rpc_pb.Invoice) (*rpc_pb.AddInvoiceResponse, er.R)
	LndPeerPort() int
	LndIdentityPubkey() string
}

func NewCjdnsHandler(socket string, api *apiv1.Apiv1) (*Cjdns, er.R) {
	log.Infof("Starting CJDNS message handler...")
	//TODO: check if cjdns is running and if tun0 is up
	device := "tun0"
	ipv6, err := getDeviceAddr(device)
	if err != nil {
		return nil, er.New("Error getting CJDNS address at device tun0")
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, er.New("Error connecting to CJDNS socket")
	}

	invoiceResponseChan := make(chan InvoiceResponse)
	cjdns := Cjdns{
		Device:              device,
		IPv6:                ipv6,
		SocketPath:          socket,
		Socket:              conn,
		api:                 *api,
		invoiceResponseChan: invoiceResponseChan,
	}
	// Unregister all handlers
	udpPorts, _ := cjdns.ListHandlers()
	for _, port := range udpPorts {
		cjdns.UnregisterHandler(port)
	}
	log.Infof("CJDNS message handler initialized successfully")
	return &cjdns, nil
}

func (c *Cjdns) Start(lnd lndRpcServer) er.R {
	c.registerRpc()

	go func() {
		for {
			cjdnsMsgChan, err := c.ListenCjdnsMessages()
			cjdnsMessage := <-cjdnsMsgChan
			close(cjdnsMsgChan)
			log.Tracef("Handling Cjdns message: %v", cjdnsMessage)
			if err != nil {
				log.Warnf("Error listening for CJDNS invoice request: %v", err)
			}
			// Check type of message
			if cjdnsMessage.invoiceReq.CjdnsAddr != "" {
				log.Tracef("CJDNS invoice request: %v", cjdnsMessage.invoiceReq)
				errorInvoiceResponse := &rpc_pb.RestError{
					Message: "Error creating invoice",
				}
				// log.Tracef("Listing lnd peers...")
				listPeersResponse, err := lnd.LndListPeers(context.TODO(), &rpc_pb.ListPeersRequest{})
				if err != nil {
					log.Errorf("Error listing lnd peers: %v", err)
				}
				if len(listPeersResponse.Peers) == 0 {
					errorInvoiceResponse = &rpc_pb.RestError{
						Message: "No lnd peer connection",
					}
					c.SendCjdnsInvoiceResponse(cjdnsMessage.invoiceReq, &rpc_pb.AddInvoiceResponse{}, errorInvoiceResponse)
				} else {
					invoice := &rpc_pb.Invoice{
						Value: int64(cjdnsMessage.invoiceReq.Amount),
					}
					log.Tracef("Creating lnd invoice with: %v", invoice)
					invoiceResponse, errr := lnd.LndAddInvoice(context.TODO(), invoice)
					if errr != nil {
						log.Errorf("Error adding invoice: %v", errr)
					}
					log.Tracef("Lnd Invoice response: %v", invoiceResponse)
					errrr := c.SendCjdnsInvoiceResponse(cjdnsMessage.invoiceReq, invoiceResponse, nil)
					if errrr != nil {
						log.Errorf("Error sending CJDNS invoice response: %v", err)
					}
				}
			} else if cjdnsMessage.invoiceRes.Txid != "" {
				log.Tracef("CJDNS invoice response: %v", cjdnsMessage.invoiceRes)
				c.invoiceResponseChan <- cjdnsMessage.invoiceRes
			} else if cjdnsMessage.lndPubReq.CjdnsAddr != "" {
				log.Tracef("CJDNS lnd pubkey request: %s", cjdnsMessage.lndPubReq.CjdnsAddr)
				idPubHex := lnd.LndIdentityPubkey()
				port := lnd.LndPeerPort()
				c.SendLndPubkeyResponse(idPubHex, cjdnsMessage.lndPubReq, port)
			} else if cjdnsMessage.lndPubRes.lndPubkey != "" {
				log.Tracef("CJDNS lnd pubkey response with pubkey: %s for: %s", cjdnsMessage.lndPubRes.lndPubkey, cjdnsMessage.lndPubRes.CjdnsAddr)
				lndAddress := rpc_pb.LightningAddress{
					Pubkey: cjdnsMessage.lndPubRes.lndPubkey,
					Host:   cjdnsMessage.lndPubRes.CjdnsAddr,
				}
				connectRequest := &rpc_pb.ConnectPeerRequest{
					Addr: &lndAddress,
				}
				log.Infof("Connecting to LND peer...: %s", connectRequest.Addr)
				_, err := lnd.LndConnectPeer(context.TODO(), connectRequest)
				if err != nil {
					if !strings.Contains(err.Message(), "already connected") {
						log.Warnf("Error connecting to LND peer: %s", err.Message())
					} else {
						log.Infof("Already connected to LND peer: %s", cjdnsMessage.lndPubRes.CjdnsAddr)
					}
				} else {
					log.Infof("Connected to LND peer: %s", cjdnsMessage.lndPubRes.CjdnsAddr)
				}
			}
		}
	}()
	// MAX_LND_PEER_CONNECTIONS := 5
	// go func() {
	// 	// for {
	// 		log.Debugf("Checking lnd peer connections...")
	// 		listPeersResponse, err := lnd.LndListPeers(context.TODO(), &rpc_pb.ListPeersRequest{})
	// 		if err != nil {
	// 			log.Errorf("Error listing lnd peers: %v", err)
	// 		}
	// 		if len(listPeersResponse.Peers) < MAX_LND_PEER_CONNECTIONS {
	// 			log.Infof("Will try to connect to request Lnd connection from CJDNS nodes.")
	// 			nodes := c.GetNodes()
	// 			for _, node := range nodes {
	// 				if node.lndPubkey == "" {
	// 					log.Debugf("Sending Cjdns lnd pubkey query to: %v %v", node.CjdnsAddr, node.CjdnsPubKey)
	// 					err := c.sendLndPubkeyQuery(node.CjdnsAddr, node.CjdnsPubKey)
	// 					if err != nil {
	// 						log.Errorf("Error sending CJDNS lnd pubkey query: %v", err)
	// 					}
	// 					time.Sleep(time.Second * 5)
	// 				}
	// 			}
	// 		} else {
	// 			log.Infof("LND peer connection established.")
	// 			//return to establish multiple connections
	// 		}
	// 	// }
	// }()

	return nil
}

func (c *Cjdns) sendLndPubkeyQuery(cjdnsNodeIp string, cjdnsNodePubkey string) error {
	conn, err := c.getSendConn()
	if err != nil {
		log.Errorf("Error getting CJDNS UDP connection:", err)
		return err
	}
	data, _ := createLndQueryRequest(cjdnsNodeIp, cjdnsNodePubkey)
	// Send data
	_, err = conn.Write(data)
	if err != nil {
		log.Errorf("Error sending CJDNS UDP packet:", err)
		return err
	}
	defer conn.Close()
	return nil
}

func (c *Cjdns) SendLndPubkeyResponse(lndPubkey string, req LndPubkeyRequest, port int) error {
	conn, err := c.getSendConn()
	if err != nil {
		log.Errorf("Error getting CJDNS UDP connection: %v", err)
		return err
	}
	data := createLndQueryResponse(req.CjdnsAddr, req.CjdnsPubKey, lndPubkey, req.Txid, port)
	log.Tracef("CJDNS lnd pubkey response: %v", data)
	// Send data
	_, err = conn.Write(data)
	if err != nil {
		log.Errorf("Error sending CJDNS UDP packet: %v", err)
		return err
	}

	log.Tracef("CJDNS LND Pubkey response sent successfully to: %s", req.CjdnsAddr)
	defer conn.Close()
	return nil
}

func (c *Cjdns) SendCjdnsInvoiceResponse(request InvoiceRequest, response *rpc_pb.AddInvoiceResponse, restError *rpc_pb.RestError) error {
	var data []byte
	if restError != nil {
		data = createErrorResponse(request.CjdnsAddr, request.CjdnsPubKey, restError.Message, request.Txid)
		//TODO: handle error messages in ListeningsCjdnsMessages
	} else {
		invoice := InvoiceResponse{
			RHash:          response.RHash,
			PaymentRequest: response.PaymentRequest,
			AddIndex:       strconv.Itoa(int(response.AddIndex)),
		}
		data = createInvoiceResponse(request.CjdnsAddr, request.CjdnsPubKey, invoice, request.Txid)
	}
	conn, err := c.getSendConn()
	if err != nil {
		log.Errorf("Error getting CJDNS UDP connection: %v", err)
		return err
	}

	// Send data
	_, err = conn.Write(data)
	if err != nil {
		log.Errorf("Error sending CJDNS UDP packet: %v", err)
		return err
	}

	log.Infof("CJDNS Invoice Response sent successfully to: %s", request.CjdnsAddr)
	defer conn.Close()
	return nil
}

func (c *Cjdns) SendCjdnsInvoiceRequest(cjdns_addr string, cjdns_pubkey string, amount uint64) (string, error) {
	log.Tracef("SendCjdnsInvoiceRequest: %s, %d", cjdns_addr, amount)
	conn, err := c.getSendConn()
	if err != nil {
		log.Errorf("Error getting UDP connection: %v", err)
		return "", err
	}
	// Data to send
	if cjdns_addr == "" {
		//TODO: retrieve pubkey from ping
	}
	data, txid := createInvoiceRequest(cjdns_addr, cjdns_pubkey, amount)

	// Send data
	_, err = conn.Write(data)
	if err != nil {
		log.Errorf("Error sending UDP packet: %v", err)
		return "", err
	}

	log.Infof("CJDNS Invoice Request sent successfully to: %s", cjdns_addr)
	defer conn.Close()
	return txid, nil
}

// func (c *Cjdns) ListenCjdnsMessages() (chan InvoiceRequest, chan LndPubkeyRequest, error) {
func (c *Cjdns) ListenCjdnsMessages() (chan CjdnsMessage, error) {
	// use this to read a packet to cjdns throught tun0
	cjdnsMsgChan := make(chan CjdnsMessage)
	// lndPubkeyRequestChan := make(chan LndPubkeyRequest)
	rAddr, err := net.ResolveUDPAddr("udp", "["+c.IPv6+"]:0")
	if err != nil {
		log.Errorf("Error resolving UDP address: %v", err)
		// return invoiceRequestChan, lndPubkeyRequestChan, err
		return cjdnsMsgChan, err
	}

	//bind to local address (tun0) and a port, then register that port to cjdns
	listenPort := 0

	if c.ListeningPort != 0 {
		listenPort = c.ListeningPort
	}
	if c.ListenConn == nil {
		c.ListenConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(c.IPv6), Port: listenPort})
		if err != nil {
			log.Errorf("Error listening UDP address: %s", err)
			// return invoiceRequestChan, lndPubkeyRequestChan, err
			return cjdnsMsgChan, err
		}
		localAddr := c.ListenConn.LocalAddr().(*net.UDPAddr)
		log.Infof("Listening for CJDNS messages at address: %s:%d", c.IPv6, localAddr.Port)
		c.ListeningPort = localAddr.Port
		c.RegisterHandler(ContentType_RESERVED, int64(localAddr.Port))
	}
	buf := make([]byte, 4096)
	go func() {
		for {
			n, _, err := c.ListenConn.ReadFromUDP(buf)
			if err != nil {
				log.Errorf("Error reading from UDP port %d: %v", rAddr.Port, err)
				continue
			}

			message, err := decode(buf[:n])
			if err != nil {
				log.Errorf("Error decoding message: %v", err)
			}
			if message.DataHeader.ContentType == uint16(ContentType_RESERVED) {
				cjdnsMessage := CjdnsMessage{}
				// log.Infof("Received CJDNS RESERVED message: %v", message.ContentBenc)
				// if message.ContentBenc.(map[string]interface{})["q"] != nil {
				// 	log.Infof("Message q: ", message.ContentBenc.(map[string]interface{})["q"].(string))
				// }
				if message.ContentBenc != nil {
					// Invoice Request
					if message.ContentBenc.(map[string]interface{})["q"] != nil && message.ContentBenc.(map[string]interface{})["q"].(string) == "invoice_req" {
						log.Debugf("Received CJDNS invoice request")
						invoiceRequest := InvoiceRequest{
							CjdnsAddr:   message.RouteHeader.IP.String(),
							CjdnsPubKey: message.RouteHeader.PublicKey,
							Txid:        message.ContentBenc.(map[string]interface{})["txid"].(string),
							Amount:      uint64(message.ContentBenc.(map[string]interface{})["amt"].(int64)),
						}
						cjdnsMessage.invoiceReq = invoiceRequest
						cjdnsMessage.invoiceRes = InvoiceResponse{}
						cjdnsMessage.lndPubReq = LndPubkeyRequest{}
						cjdnsMessage.lndPubRes = LndPubkeyResponse{}
						cjdnsMsgChan <- cjdnsMessage
						return
						// Invoice Response Error
					} else if message.ContentBenc.(map[string]interface{})["error"] != nil {
						log.Debugf("Received CJDNS invoice response error")
						// CjdnsInvoiceResponse.Error =
						// CjdnsInvoiceResponse.Txid = message.ContentBenc.(map[string]interface{})["txid"].(string)
						invoiceResponse := InvoiceResponse{
							Error: message.ContentBenc.(map[string]interface{})["error"].(map[string]interface{})["message"].(string),
							Txid:  message.ContentBenc.(map[string]interface{})["txid"].(string),
						}
						cjdnsMessage.invoiceReq = InvoiceRequest{}
						cjdnsMessage.invoiceRes = invoiceResponse
						cjdnsMessage.lndPubReq = LndPubkeyRequest{}
						cjdnsMessage.lndPubRes = LndPubkeyResponse{}
						cjdnsMsgChan <- cjdnsMessage
						return
						// Invoice Response
					} else if message.ContentBenc.(map[string]interface{})["invoice"] != nil {
						log.Debugf("Received CJDNS invoice response")
						printError := false
						txid, ok := message.ContentBenc.(map[string]interface{})["txid"].(string)
						if !ok {
							log.Errorf("Error getting txid from CJDNS invoice response.")
							printError = true
						}
						paymentRequest, ok := message.ContentBenc.(map[string]interface{})["invoice"].(map[string]interface{})["paymentRequest"].(string)
						if !ok {
							log.Errorf("Error getting paymentRequest from CJDNS invoice response.")
							printError = true
						}
						index, ok := message.ContentBenc.(map[string]interface{})["invoice"].(map[string]interface{})["addIndex"].(string)
						if !ok {
							log.Errorf("Error getting addIndex from CJDNS invoice response.")
							printError = true
						}
						rHash, ok := message.ContentBenc.(map[string]interface{})["invoice"].(map[string]interface{})["rHash"]
						if !ok {
							log.Errorf("Error getting rHash from CJDNS invoice response.")
							printError = true
						}
						if printError {
							log.Errorf("CJDNS invoice response: %v", message.ContentBenc)
						}
						invoiceResponse := InvoiceResponse{
							RHash:          []byte(rHash.(string)),
							PaymentRequest: paymentRequest,
							AddIndex:       index,
							Txid:           txid,
						}
						cjdnsMessage.invoiceReq = InvoiceRequest{}
						cjdnsMessage.invoiceRes = invoiceResponse
						cjdnsMessage.lndPubReq = LndPubkeyRequest{}
						cjdnsMessage.lndPubRes = LndPubkeyResponse{}
						cjdnsMsgChan <- cjdnsMessage
						return
					} else if message.ContentBenc.(map[string]interface{})["q"] != nil && message.ContentBenc.(map[string]interface{})["q"].(string) == "lnd_pubkey" {
						log.Debugf("Received CJDNS lnd pubkey request")
						lndPubReq := LndPubkeyRequest{
							CjdnsAddr:   message.RouteHeader.IP.String(),
							CjdnsPubKey: message.RouteHeader.PublicKey,
							Txid:        message.ContentBenc.(map[string]interface{})["txid"].(string),
						}
						cjdnsMessage.invoiceReq = InvoiceRequest{}
						cjdnsMessage.invoiceRes = InvoiceResponse{}
						cjdnsMessage.lndPubReq = lndPubReq
						cjdnsMessage.lndPubRes = LndPubkeyResponse{}
						cjdnsMsgChan <- cjdnsMessage
						return
					} else if message.ContentBenc.(map[string]interface{})["lnd_pubkey"] != nil {
						log.Debugf("Received lnd pubkey response: %v", message.ContentBenc)
						// lndPubkeyResponse.Txid = message.ContentBenc.(map[string]interface{})["txid"].(string)
						// lndPubkeyResponse.lndPubkey = message.ContentBenc.(map[string]interface{})["lnd_pubkey"].(string)
						// log.Infof("Lnd pubkey: %s", lndPubkeyResponse.lndPubkey)
						port := 9735 //default
						if message.ContentBenc.(map[string]interface{})["port"] != nil {
							port = int(message.ContentBenc.(map[string]interface{})["port"].(int64))
						}
						lndPubkeyResponse := LndPubkeyResponse{	
							CjdnsAddr: "["+message.RouteHeader.IP.String()+"]:"+strconv.Itoa(port),
							lndPubkey: message.ContentBenc.(map[string]interface{})["lnd_pubkey"].(string),
							Txid:      message.ContentBenc.(map[string]interface{})["txid"].(string),
						}
						cjdnsMessage.invoiceReq = InvoiceRequest{}
						cjdnsMessage.invoiceRes = InvoiceResponse{}
						cjdnsMessage.lndPubReq = LndPubkeyRequest{}
						cjdnsMessage.lndPubRes = lndPubkeyResponse
						cjdnsMsgChan <- cjdnsMessage
						return
					} else {
						log.Warn("Received CJDNS invoice response with unknown format. Try upgrading pld...")
						log.Warnf("Message.ContentBenc: %v", message.ContentBenc)
						cjdnsMessage.invoiceReq = InvoiceRequest{}
						cjdnsMessage.invoiceRes = InvoiceResponse{}
						cjdnsMessage.lndPubReq = LndPubkeyRequest{}
						cjdnsMessage.lndPubRes = LndPubkeyResponse{}
						cjdnsMsgChan <- cjdnsMessage
						return
					}
				}
			}
		}
	}()
	// defer close(cjdnsMsgChan)
	return cjdnsMsgChan, nil
}

func createErrorResponse(receiverIP string, receiverPubkey string, errorMsg string, txid string) []byte {
	// coinType := []byte{0x80, 0x00, 0x01, 0x86}
	msg := map[string]interface{}{
		"error": map[string]string{
			"message": errorMsg,
		},
		"txid": txid,
	}
	payload := encodeMsg(msg, receiverIP, receiverPubkey)
	return payload
}

func createLndQueryRequest(receiverIP string, receiverPubkey string) ([]byte, string) {
	if receiverIP == "" {
		log.Errorf("Can not create CJDNS lnd pubkey request, IP is empty")
		return nil, ""
	}
	random := int(generateRandomNumber()) // Convert int64 to int
	requests := 1
	txid := strconv.Itoa(random) + "/" + strconv.Itoa(requests)
	msg := map[string]interface{}{
		"q":    "lnd_pubkey",
		"txid": txid,
	}
	payload := encodeMsg(msg, receiverIP, receiverPubkey)
	return payload, txid
}

func createLndQueryResponse(receiverIP string, receiverPubkey string, lndPubkey string, txid string, port int) []byte {
	msg := map[string]interface{}{
		"lnd_pubkey": lndPubkey,
		"port": 	  port,
		"txid":       txid,
	}
	payload := encodeMsg(msg, receiverIP, receiverPubkey)
	return payload
}

func createInvoiceResponse(receiverIP string, receiverPubkey string, invoice InvoiceResponse, txid string) []byte {
	msg := map[string]interface{}{
		"invoice": map[string]interface{}{
			"rHash":          invoice.RHash,
			"paymentRequest": invoice.PaymentRequest,
			"addIndex":       invoice.AddIndex,
		},
		"txid": txid,
	}
	payload := encodeMsg(msg, receiverIP, receiverPubkey)
	return payload
}

func encodeMsg(msg map[string]interface{}, ip string, pubkey string) []byte {
	coinType := []byte{0x80, 0x00, 0x01, 0x86}
	var bytesMessage []byte = nil
	bytesMessage = append(bytesMessage, coinType...)
	encodedMsg, err := bencode.EncodeBytes(msg)
	if err != nil {
		log.Errorf("Error bencoding message: %s", err)
	}
	bytesMessage = append(bytesMessage, encodedMsg...)
	var cjdnsip net.IP = net.ParseIP(ip)
	var message Message = Message{
		RouteHeader: RouteHeader{
			PublicKey: pubkey,
			Version:   22,
			IP:        cjdnsip,
			SwitchHeader: SwitchHeader{
				Label:   "0000",
				Version: 1,
			},
			IsIncoming: false,
			IsCtrl:     false,
		},
		DataHeader: DataHeader{
			ContentType: ContentType_RESERVED,
			Version:     1,
		},
		ContentBytes: bytesMessage,
		RawBytes:     nil,
		ContentBenc:  msg,
		Content:      nil,
	}

	payload, err := message.encode()
	if err != nil {
		log.Errorf("Error encoding CJDNS message: %s", err)
	}
	return payload
}

func createInvoiceRequest(receiverIP string, receiverPubkey string, amount uint64) ([]byte, string) {
	if receiverIP == "" {
		log.Errorf("Can not create CJDNS invoice request, IP is empty")
		return nil, ""
	}
	random := int(generateRandomNumber())
	requests := 1
	txid := strconv.Itoa(random) + "/" + strconv.Itoa(requests)
	msg := map[string]interface{}{
		"q":    "invoice_req",
		"amt":  amount,
		"txid": txid,
	}
	payload := encodeMsg(msg, receiverIP, receiverPubkey)
	return payload, txid
}

func (c *Cjdns) Ping(node string) (string, error) {
	log.Tracef("Ping Cjdns node: %v", node)
	err := error(nil)
	if c.Socket == nil {
		return "", errors.New("CJDNS connection is nil")
	}

	ping := map[string]string{
		"q": "ping",
	}
	pingNode := map[string]interface{}{
		"q":    "RouterModule_pingNode",
		"args": map[string]string{"path": "fc93:1145:f24c:ee59:4a09:288e:ada8:0901"},
	}
	var bytes []byte
	if node != "" {
		bytes, err = bencode.EncodeBytes(pingNode)
		if err != nil {
			return "", err
		}
	} else {
		bytes, err = bencode.EncodeBytes(ping)
		if err != nil {
			return "", err
		}
	}

	_, err = c.Socket.Write(bytes)
	if err != nil {
		return "", err
	}

	// Listen for response
	c.Socket.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1024)
	n, err := c.Socket.Read(buf)

	var response map[string]interface{}
	if n > 0 {
		err := bencode.DecodeBytes(buf, &response)
		if err != nil {
			return "", err
		}
		// check if response has "addr" and "ms" fields
		if addr, ok := response["addr"].(string); ok {
			res := addr + " ms:" + fmt.Sprintf("%d", response["ms"].(int64))
			return res, nil
		} else if q, ok := response["q"].(string); ok && q == "pong" {
			return q, nil
		}
		return "", nil
	}
	if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
		return "", errors.New("CJDNS connection timeout")
	} else if err != nil {
		return "", err
	}

	return "", nil
}

func (c *Cjdns) registerRpc() {
	cjdnsCategory := c.api.Category("cjdns")
	apiv1.Endpoint(
		cjdnsCategory,
		"ping",
		`
		Ping a cjdns node.
		`,
		func(req *rpc_pb.CjdnsPingRequest) (*rpc_pb.CjdnsPingResponse, er.R) {
			return c.PingCjdns(req)
		},
	)
	apiv1.Endpoint(
		cjdnsCategory,
		"requestinvoice",
		`
		Request a payment invoice using a cjdns address.
		`,
		func(req *rpc_pb.CjdnsPaymentInvoiceRequest) (*rpc_pb.CjdnsPaymentInvoiceResponse, er.R) {
			return c.CjdnsInvoiceRequest(req)
		},
	)
}

func (c *Cjdns) PingCjdns(req *rpc_pb.CjdnsPingRequest) (*rpc_pb.CjdnsPingResponse, er.R) {
	// if !isValidIPv6(req.CjdnsAddr) {
	// 	return nil, er.New("Invalid CJDNS address")
	// }
	// if r.cfg.CjdnsSocket == "" {
	// 	return nil, er.New("Cjdns socket not found")
	// }
	res, pingerr := c.Ping(req.CjdnsAddr)
	if pingerr != nil {
		return nil, er.New("Cjdns ping error")
	}

	return &rpc_pb.CjdnsPingResponse{
		Pong: res,
	}, nil
}

func (c *Cjdns) CjdnsInvoiceRequest(req *rpc_pb.CjdnsPaymentInvoiceRequest) (*rpc_pb.CjdnsPaymentInvoiceResponse, er.R) {
	txid, err := c.SendCjdnsInvoiceRequest(req.CjdnsAddr, req.CjdnsPubkey, req.Amount)
	if err != nil {
		return nil, er.New("Cjdns invoice request error")
	}

	//TODO: move the listening for respose after c.ListenCjdnsMessages()
	// how to return here to return the response?
	log.Debugf("Waiting for Cjdns invoice response...")
	response := <-c.invoiceResponseChan
	if response.Txid != txid {
		log.Errorf("Cjdns invoice response txid %s, does not match request txid %s", response.Txid, txid)
		return nil, er.New("Cjdns invoice response txid does not match request txid")
	} else if response.Error != "" {
		log.Infof("Cjdns invoice response error: ", response.Error)
		return nil, er.New(response.Error)
	} else {
		return &rpc_pb.CjdnsPaymentInvoiceResponse{
			RHash:          response.RHash,
			PaymentRequest: response.PaymentRequest,
		}, nil
	}
}

func (c *Cjdns) Stop() er.R {
	//TODO: unregister handlers
	return nil
}

// Close CJDNS socket
func Close(ls net.Conn) error {
	err := ls.Close()
	if err != nil {
		return err
	}
	return nil
}
