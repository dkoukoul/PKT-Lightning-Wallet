package cjdns

import (
	"encoding/binary"
	"errors"
	"fmt"
)

func netChecksumRaw(buf []byte) uint16 {
	// Checksum pairs.
	var state uint32
	var hbit uint32
	add := func(num uint16) {
		state += uint32(num)
		if state > 0x7fffffff {
			hbit ^= 1
			state &= 0x7fffffff
		}
	}

	for i := 0; i < len(buf)-1; i += 2 {
		add(binary.LittleEndian.Uint16(buf[i:]))
	}

	// Do the odd byte if there is one.
	if len(buf)%2 != 0 {
		add(uint16(buf[len(buf)-1]))
	}

	state |= hbit << 31

	for state < 0 || state > 0xFFFF {
		state = (state >> 16) + (state & 0xFFFF)
	}

	// Unconditional flip because Go only runs on little endian machines
	state = ((state << 8) & 0xffff) | (state >> 8)

	return uint16(state ^ 0xffff)
}

func parseCtrl(bytes []byte) (out interface{}, err error) {
	checksum := uint16(bytes[0])<<8 | uint16(bytes[1])
	bytes[0], bytes[1] = 0, 0
	realChecksum := netChecksumRaw(bytes)
	bytes[0], bytes[1] = byte(checksum>>8), byte(checksum&0xff)
	endian := "little"
	if realChecksum != checksum {
		if realChecksum == ((checksum<<8)&0xff00 | (checksum>>8)&0xff) {
			endian = "big"
		} else {
			err = errors.New(fmt.Sprintf("invalid checksum, expected [%d] got [%d]", realChecksum, checksum))
			return
		}
	}
	//TODO: parse the rest of the packet
	// typ := typeString(uint16(bytes[2])<<8 | uint16(bytes[3]))
	// content := bytes[4:]
	// switch typ {
	// case "ERROR":
	//     out, err = errMsgParse(content)
	// case "PING", "PONG", "KEYPING", "KEYPONG":
	//     out, err = pingParse(content, typ)
	// default:
	//     err = errors.New(fmt.Sprintf("could not parse, unknown type CTRL packet %s", typ))
	// }
	out.(map[string]interface{})["endian"] = endian
	return
}