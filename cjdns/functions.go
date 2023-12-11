package cjdns

import (
	"errors"
	"strings"
	"time"

	"github.com/pkt-cash/pktd/pktlog/log"
	"github.com/zeebo/bencode"
)


func (c *Cjdns) ListHandlers() ([]int, error) {
	message := map[string]interface{}{
		"q":    "UpperDistributor_listHandlers",
		"page": 0,
	}

	bytes, err := bencode.EncodeBytes(message)
	if err != nil {
		return nil, err
	}

    c.socketLock.Lock()
    
	_, err = c.Socket.Write(bytes)
	if err != nil {
		return nil, err
	}
	c.Socket.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1024)
	resultChan := make(chan []int, 1)
	var response map[string]interface{}
	go func(resultChan chan<- []int) {
		n, err := c.Socket.Read(buf)
		if err != nil {
			return
		}
		if n > 0 {
			err := bencode.DecodeBytes(buf, &response)
			if err != nil {
				log.Errorf("Error decoding response: %s", err)
				return
			}
			if handlers, ok := response["handlers"].(interface{}); ok {
				udpPorts := []int{}
				for _, handler := range handlers.([]interface{}) {
					udpPort := handler.(map[string]interface{})["udpPort"].(int64)
					udpPorts = append(udpPorts, int(udpPort))
				}
				resultChan <- udpPorts
			}
		}
	}(resultChan)
	defer c.socketLock.Unlock()
	return <-resultChan, nil
}

func (c *Cjdns) PeerStats() ([]CjdnsPeer, error) {
	c.socketLock.Lock()
	// log.Infof("Getting Cjdns peer stats...")
	cjdnsPeers := []CjdnsPeer{}
	message := map[string]interface{}{
		"q":    "InterfaceController_peerStats",
		"page": 0,
	}
	bytes, err := bencode.EncodeBytes(message)
	if err != nil {
		return nil, err
	}

	_, err = c.Socket.Write(bytes)
	if err != nil {
		return nil, err
	}

	c.Socket.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	resultChan := make(chan []CjdnsPeer)
	var response map[string]interface{}
	go func() {
		data := []byte{}
		for {
			n, err := c.Socket.Read(buf)
			// log.Infof("Read %v bytes from Cjdns socket", buf)
			if n > 0 {
				// log.Debugf("Read %v bytes from Cjdns socket", n)
				data = append(data, buf[:n]...)
				if err != nil {
					log.Errorf("Error reading from CJDNS socket: %v", err)
					resultChan <- nil
				}
			} else if n == 0 {
				// log.Debugf("Read 0 bytes from Cjdns socket, breaking")
				break
			}
		}
		err = bencode.DecodeBytes(data, &response)
		// log.Debugf("Cjdns peer stats response: %v", response)
		if err != nil {
			log.Errorf("Error decoding response: %v", err)
		}
		// log.Debugf("PeerStats response: %v", response)
		if peers, ok := response["peers"].(interface{}); ok {
			for _, peer := range peers.([]interface{}) {
				p := peer.(map[string]interface{})
				cjdnsPeer := CjdnsPeer{
					addr:   p["addr"].(string),
					lladdr: p["lladdr"].(string),
					state:  p["state"].(string),
				}
				cjdnsPeers = append(cjdnsPeers, cjdnsPeer)
				//return here if we want to connect to only one node
			}
			resultChan <- cjdnsPeers
			return // here if we want to try to connect to all nodes
		}
	}()

	peers := <-resultChan
	if peers == nil {
		return nil, errors.New("timeout reading cjdns socket")
	}
	defer c.socketLock.Unlock()
	return cjdnsPeers, nil
}

func (c *Cjdns) RegisterHandler(contentType int64, udpPort int64) error {
	c.socketLock.Lock()
	message := map[string]interface{}{
		"q":    "UpperDistributor_registerHandler",
		"args": map[string]int64{"contentType": contentType, "udpPort": udpPort},
	}
	bytes, err := bencode.EncodeBytes(message)
	if err != nil {
		log.Errorf("Error encoding CJDNS message:", err)
		return err
	}

    
    
	_, err = c.Socket.Write(bytes)
	if err != nil {
		log.Errorf("Error writing to CJDNS socket:", err)
		return err
	}
	c.Socket.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1024)
	var response map[string]interface{}
	n, err := c.Socket.Read(buf)
	if err != nil {
		return err
	}
	if n > 0 {
	 	bencode.DecodeBytes(buf[:n], &response)
	}
	defer c.socketLock.Unlock()
	return nil
}

func (c *Cjdns) UnregisterHandler(udpPort int) error {
	message := map[string]interface{}{
		"q":    "UpperDistributor_unregisterHandler",
		"args": map[string]int{"udpPort": udpPort},
	}
	bytes, err := bencode.EncodeBytes(message)
	if err != nil {
		log.Errorf("Error encoding CJDNS message:", err)
		return err
	}

    c.socketLock.Lock()
    
	_, err = c.Socket.Write(bytes)
	if err != nil {
		log.Errorf("Error writing to CJDNS socket:", err)
		return err
	}
	c.Socket.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 1024)

	var response map[string]interface{}
	n, err := c.Socket.Read(buf)
	if err != nil {
		return err
	}
	if n > 0 {
		err := bencode.DecodeBytes(buf[:n], &response)
		// log.Debugf("UnregisterHandler response: %v", response)
		if err != nil {
			log.Error("Error decoding response: %s", err)
			return err
		}
	}
	defer c.socketLock.Unlock()
	return nil
}

func (c *Cjdns) GetNodes() []CjdnsNode {
	//Get cjdns nodes
	peers, err := c.PeerStats()
	if err != nil {
		log.Errorf("Error getting CJDNS peers: %v", err)
	}
	nodes := []CjdnsNode{}

	//Query them one by one for lnd pubkey
	for _, peer := range peers {
		parts := strings.Split(peer.addr, ".")
		pubkey := strings.Join(parts[len(parts)-2:], ".")

		cjdnsIpv6, err := publicToIp6(pubkey)
		if err != nil {
			log.Errorf("Error deriving CJDNS IPv6 from pubkey: %v", err)
			return nil
		}
		node := CjdnsNode{
			CjdnsAddr:   cjdnsIpv6,
			CjdnsPubKey: pubkey,
			lndPubkey:   "",
		}
		nodes = append(nodes, node)
	}

	return nodes
}
