package cjdns

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	DataHeaderSize   = 4
)

type DataHeader struct {
	ContentType uint16
	Version     int
}

func (dh * DataHeader) parse(bytes []byte) (DataHeader, error) {
	if len(bytes) < 4 {
		return DataHeader{}, fmt.Errorf("DataHeader too short")
	}

	versionAndFlags := bytes[0]
	//unused := bytes[1]
	contentType := binary.BigEndian.Uint16(bytes[2:])

	version := versionAndFlags >> 4

	return DataHeader{
		ContentType: contentType,
		Version:     int(version),
	}, nil
}

func (dh *DataHeader) encode() ([]byte, error) {
    if dh == nil {
        return nil, fmt.Errorf("DataHeader is nil")
    }

    buf := new(bytes.Buffer)

    versionAndFlags := byte(dh.Version << 4)
    binary.Write(buf, binary.BigEndian, versionAndFlags)
    binary.Write(buf, binary.BigEndian, uint8(0))
    binary.Write(buf, binary.BigEndian, dh.ContentType)

    return buf.Bytes(), nil
}