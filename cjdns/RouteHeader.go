package cjdns

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"github.com/pkt-cash/pktd/pktlog/log"
)

const (
	RouteHeaderSize = 68
	F_CTRL          = 0x01
	F_INCOMING      = 0x02
)

type RouteHeader struct {
	PublicKey    string
	Version      int32
	IP           net.IP
	SwitchHeader SwitchHeader
	IsIncoming   bool
	IsCtrl       bool
}

func (rh *RouteHeader) serialize() ([]byte, error) {
	var ZEROKEY = make([]byte, 32)
	var ZEROIP = make([]byte, 16)
	if string(rh.IP) == "" && !rh.IsCtrl {
		return nil, errors.New("IP6 required")
	}
	keyBytes := ZEROKEY
	if rh.PublicKey != "" {
		keyBytes = stringToKeyBytes(rh.PublicKey)
	}
	shBytes := rh.SwitchHeader.serialize()

	versionBytes := make([]byte, 4)
	version := uint32(rh.Version)
	binary.BigEndian.PutUint32(versionBytes, version)
	flags := byte(0)
	if rh.IsIncoming {
		flags |= F_INCOMING
	}
	if rh.IsCtrl {
		flags |= F_CTRL
	}
	padBytes := []byte{flags, 0, 0, 0}
	ipBytes := ZEROIP
	// fmt.Println("serialize isCTRL:", rh.IsCtrl)
	if string(rh.IP) != "" {
		ipBytes = rh.IP.To16()
	}
	out := bytes.Join([][]byte{keyBytes, shBytes, versionBytes, padBytes, ipBytes}, []byte{})
	// fmt.Println("Seria hdrBytes:", out)
	return out, nil
}

func (rh *RouteHeader) parse(hdrBytes []byte) (RouteHeader, error) {
	// fmt.Println("Parse hdrBytes:", hdrBytes)
	if len(hdrBytes) < RouteHeaderSize {
		return RouteHeader{}, errors.New("runt")
	}
	x := 0
	keyBytes := hdrBytes[x : x+32]
	x += 32
	shBytes := hdrBytes[x : x+12]
	x += 12
	versionBytes := hdrBytes[x : x+4]
	x += 4
	flags := hdrBytes[x+1]

	x += 2
	// unusedBytes := hdrBytes[x : x+2]
	// fmt.Println("unusedBytes", unusedBytes)
	x += 2
	ipBytes := hdrBytes[x : x+16]
	isCtrl := false
	if flags != 0 {
		isCtrl = true
	}
	// fmt.Println("parse isCtrl:", isCtrl)
	if RouteHeaderSize != len(hdrBytes) {
		return RouteHeader{}, errors.New("invalid header size")
	}
	if !isCtrl && isAllZero(ipBytes) {
		return RouteHeader{}, errors.New("IP6 is not defined")
	}
	// } else if isCtrl && !isAllZero(ipBytes) {
	// 	// return RouteHeader_t{}, errors.New("IP6 is defined for CTRL frame")
	// 	// fmt.Println("IP6 is defined for CTRL frame")
	// }
	switchHeader := SwitchHeader{}
	switchHeader, err := switchHeader.parse(shBytes)

	if err != nil {
		switchHeader = SwitchHeader{}
		log.Errorf("Error parsing CJDNS message switch header: ", err)
	}
	var ip net.IP = nil
	if !isCtrl {
		ip = ip6_bytes_to_net_ip(ipBytes)
	}
	out := RouteHeader{
		PublicKey:    keyBytesToString(keyBytes),
		Version:      int32(versionBytes[0])<<24 | int32(versionBytes[1])<<16 | int32(versionBytes[2])<<8 | int32(versionBytes[3]),
		IP:           ip,
		SwitchHeader: switchHeader,
		IsIncoming:   flags&F_INCOMING != 0,
		IsCtrl:       isCtrl,
	}

	return out, nil
}

func isAllZero(bytes []byte) bool {
	for _, b := range bytes {
		if b != 0 {
			return false
		}
	}
	return true
}

func stringToKeyBytes(key string) []byte {
	bytes, err := Base32_decode(strings.TrimSuffix(key, ".k"))
	if err != nil {
		log.Errorf("Error decoding key: ", err)
		return nil
	} else {
		return bytes
	}
}

func keyBytesToString(bytes []byte) string {
	if len(bytes) != 32 {
		log.Warn("CJDNS public key unexpected length", len(bytes))
	}
	return Base32_encode(bytes) + ".k"
}

func ip6_bytes_to_net_ip(ip6 []byte) net.IP {
	if len(ip6) != 16 {
		log.Errorf("CJDNS ip bad length")
	}
	if ip6[0] != 0xfc {
		log.Errorf("CJDNS ip does not begin with fc")
	}
	return net.IP(ip6)
}

func Base32_encode(input []byte) string {
	B32_CHARS := []byte("0123456789bcdfghjklmnpqrstuvwxyz")
	outIndex := 0
	inIndex := 0
	work := 0
	bits := 0
	var output []byte
	for inIndex < len(input) {
		work |= int(input[inIndex]) << bits
		bits += 8
		for bits >= 5 {
			b32Index := work & 31
			output = append(output, B32_CHARS[b32Index])
			outIndex++
			bits -= 5
			work >>= 5
		}
		inIndex++
	}

	if bits > 0 {
		b32Index := work & 31
		output = append(output, B32_CHARS[b32Index])
		bits -= 5
		work >>= 5
	}
	return string(output)
}

func Base32_decode(input string) ([]byte, error) {
	var NUM_FOR_ASCII = []int{
		99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99,
		99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99, 99,
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 99, 99, 99, 99, 99, 99,
		99, 99, 10, 11, 12, 99, 13, 14, 15, 99, 16, 17, 18, 19, 20, 99,
		21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 99, 99, 99, 99, 99,
		99, 99, 10, 11, 12, 99, 13, 14, 15, 99, 16, 17, 18, 19, 20, 99,
		21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 99, 99, 99, 99, 99,
	}
	output := make([]byte, 0)
	outputIndex := 0
	inputIndex := 0
	nextByte := 0
	bits := 0

	for inputIndex < len(input) {
		o := int(input[inputIndex])
		if o&0x80 != 0 {
			return nil, errors.New("invalid input")
		}
		b := NUM_FOR_ASCII[o]
		inputIndex++
		if b > 31 {
			return nil, errors.New("bad character " + string(input[inputIndex]) + " in " + input)
		}

		nextByte |= (b << bits)
		bits += 5

		if bits >= 8 {
			output = append(output, byte(nextByte&0xff))
			outputIndex++
			bits -= 8
			nextByte >>= 8
		}
	}

	if bits >= 5 || nextByte != 0 {
		return nil, errors.New("invalid input")
	}

	return output, nil
}
