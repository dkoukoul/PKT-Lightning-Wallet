package cjdns

import (
	"bytes"
	"encoding/hex"

	"github.com/zeebo/bencode"
	"github.com/pkt-cash/pktd/pktlog/log"
)

type Message struct {
	RouteHeader  RouteHeader
	DataHeader   DataHeader
	ContentBytes []byte
	RawBytes     []byte
	ContentBenc  interface{}
	Content      interface{}
}

func (msg *Message) encode() ([]byte, error) {
	var buf bytes.Buffer

	// Write route header
	routeHeaderBytes, err := msg.RouteHeader.serialize()
	// fmt.Println("Route Header Bytes:", routeHeaderBytes)
	if err != nil {
		return nil, err
	}
	buf.Write(routeHeaderBytes)

	// Write data header if not a control message
	if !msg.RouteHeader.IsCtrl {
		dataHeaderBytes, err := msg.DataHeader.encode()
		if err != nil {
			return nil, err
		}
		buf.Write(dataHeaderBytes)
	}

	// Write content bytes
	buf.Write(msg.ContentBytes)
	buf.Write(msg.RawBytes)
	bencBytes, ok := msg.ContentBenc.([]byte)
	if !ok {
		log.Tracef("Error converting bencode to bytes")
	} else {
		buf.Write(bencBytes)
	}
	contentBytes, ok := msg.Content.([]byte)
	if !ok {
		log.Tracef("Error converting content to bytes")
	} else {
		buf.Write(contentBytes)
	}

	return buf.Bytes(), nil
}

func decode(bytes []byte) (Message, error) {
	x := 0
	routeHeaderBytes := bytes[x:RouteHeaderSize]
	x += RouteHeaderSize
	routeHeader := RouteHeader{}
	routeHeader, err := routeHeader.parse(routeHeaderBytes)
	if err != nil {
		log.Errorf("Error parsing CJDNS message route header: ", err)
	}

	var dataHeaderBytes []byte = nil
	var dataHeader DataHeader = DataHeader{}
	if !routeHeader.IsCtrl {
		dataHeaderBytes = bytes[x : x+DataHeaderSize]
		x += DataHeaderSize
		dataHeader, err = dataHeader.parse(dataHeaderBytes)
		if err != nil {
			log.Errorf("Error parsing CJDNS message data header: ", err)
		}
	}
	if x > len(bytes) {
		log.Errorf("Error parsing CJDNS message, message not long enough to be decoded: ", err)
		return Message{}, err
	}
	dataBytes := bytes[x:]

	var decodedBytes interface{} = nil
	var content interface{} = nil
	if dataHeader.ContentType == ContentType_RESERVED {
		coinType := dataBytes[:4]
		if hex.EncodeToString(coinType) == "80000186" {
			bencode.DecodeBytes(dataBytes[4:], &decodedBytes)
		} else {
			bencode.DecodeBytes(dataBytes, &decodedBytes)
		}
	} else if dataHeader.ContentType == ContentType_CJDHT {
		bencode.DecodeBytes(dataBytes, &decodedBytes)
	} else if routeHeader.IsCtrl {
		content, _ = parseCtrl(dataBytes)
	}

	return Message{
		RouteHeader:  routeHeader,
		DataHeader:   dataHeader,
		ContentBytes: dataBytes,
		RawBytes:     bytes,
		ContentBenc:  decodedBytes,
		Content:      content,
	}, nil
}
