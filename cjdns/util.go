package cjdns

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/pkt-cash/pktd/pktlog/log"
)

const (
	ContentType_IP6_IP       = 0
	ContentType_IP6_ICMP     = 1
	ContentType_IP6_IGMP     = 2
	ContentType_IP6_IPIP     = 4
	ContentType_IP6_TCP      = 6
	ContentType_IP6_EGP      = 8
	ContentType_IP6_PUP      = 12
	ContentType_IP6_UDP      = 17
	ContentType_IP6_IDP      = 22
	ContentType_IP6_TP       = 29
	ContentType_IP6_DCCP     = 33
	ContentType_IP6_IPV6     = 41
	ContentType_IP6_RSVP     = 46
	ContentType_IP6_GRE      = 47
	ContentType_IP6_ESP      = 50
	ContentType_IP6_AH       = 51
	ContentType_IP6_MTP      = 92
	ContentType_IP6_BEETPH   = 94
	ContentType_IP6_ENCAP    = 98
	ContentType_IP6_PIM      = 103
	ContentType_IP6_COMP     = 108
	ContentType_IP6_SCTP     = 132
	ContentType_IP6_UDPLITE  = 136
	ContentType_IP6_RAW      = 255
	ContentType_CJDHT        = 256
	ContentType_IPTUN        = 257
	ContentType_RESERVED     = 258
	ContentType_RESERVED_MAX = 0x7fff
	ContentType_AVAILABLE    = 0x8000
	ContentType_CTRL         = 0xffff + 1
	ContentType_MAX          = 0xffff + 2
)

func generateRandomNumber() int64 {
	rand.Seed(time.Now().UnixNano())
	return rand.Int63n(9000000000) + 1000000000
}

func (c *Cjdns) getSendConn() (*net.UDPConn, error) {
	// use this to send a packet to cjdns throught tun0
	rAddr, err := net.ResolveUDPAddr("udp", "[fc00::1]:1")
	if err != nil {
		log.Errorf("Error resolving UDP address: %v", err)
		return nil, err
	}
	if err != nil {
		log.Errorf("Error getting device address: %v", err)
		return nil, err
	}
	//bind to local address (tun0) and a port, then register that port to cjdns
	sAddr := &net.UDPAddr{IP: net.ParseIP(c.IPv6), Port: 1}
	conn, err := net.DialUDP("udp", sAddr, rAddr)

	c.RegisterHandler(ContentType_RESERVED, int64(sAddr.Port))
	if err != nil {
		log.Errorf("Error dialing UDP address: %v", err)
		return nil, err
	}

	return conn, nil
}

func getDeviceAddr(device string) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Errorf("Error getting interfaces for CJDNS connection: %v", err)
		return "", err
	}
	deviceAddr := ""
	for _, iface := range ifaces {
		if iface.Name == device {
			addrs, err := iface.Addrs()
			if err != nil {
				log.Errorf("Error getting CJDNS addresses at tun0: %v", err)
				return "", err
			}

			for _, addr := range addrs {
				ip, _, err := net.ParseCIDR(addr.String())
				if err != nil {
					log.Errorf("Error parsing CIDR: %v", err)
					return "", err
				}
				deviceAddr = ip.String()
				break
			}
		}
		if deviceAddr != "" {
			return deviceAddr, nil
		}
	}
	return "", errors.New("device not found")
}

func publicToIp6(pubKey string) (string, error) {
	if pubKey[len(pubKey)-2:] != ".k" {
		return "", errors.New("key does not end with .k")
	}

	keyBytes, err := Base32_decode(pubKey[:len(pubKey)-2])
	if err != nil {
		return "", err
	}

	hashOne := sha512.Sum512(keyBytes)
	hashTwo := sha512.Sum512(hashOne[:])

	var parts []string
	for i := 0; i < 16; i += 2 {
		parts = append(parts, hex.EncodeToString(hashTwo[i:i+2]))
	}

	return strings.Join(parts, ":"), nil
}
