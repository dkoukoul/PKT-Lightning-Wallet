package cjdns

import (
	"encoding/hex"
	"errors"
	"strings"

	"regexp"
)

const (
	SwitchHeaderSize = 12
	currentVer       = 1
)

type SwitchHeader struct {
	Label         string
	Congestion    int
	SuppressError bool
	Version       int
	LabelShift    int
	Penalty       int
}

func (sh *SwitchHeader) parse(hdrBytes []byte) (SwitchHeader, error) {
	// fmt.Println("Parse switch header", hdrBytes)
	if len(hdrBytes) < SwitchHeaderSize {
		return SwitchHeader{}, errors.New("runt")
	}
	x := 0
	labelBytes := hdrBytes[x : x+8]
	x += 8
	congestAndSuppressErrors := hdrBytes[x]
	x += 2
	versionAndLabelShift := hdrBytes[x]
	x += 2

	version := versionAndLabelShift >> 6

	if version == 0 {
		// Versions < 18 did not always set the version on the switch header so we'll be quiet.
	} else if version != currentVer {
		// fmt.Println("WARNING: Parsing label with unrecognized version number [", version, "]")
	}
	labelStr := hex.EncodeToString(labelBytes)
	//labelRegex       = "^([a-f0-9]{4}\\.){3}[a-f0-9]{4}$"
	re := regexp.MustCompile(`[0-9a-f]{4}`)
	labelStr = re.ReplaceAllString(labelStr, "$0.")
	labelStr = labelStr[:len(labelStr)-1]
	return SwitchHeader{
		Label:         labelStr,
		Congestion:    int(congestAndSuppressErrors >> 1),
		SuppressError: congestAndSuppressErrors&1 != 0,
		Version:       int(version),
		LabelShift:    int(versionAndLabelShift & ((1 << 6) - 1)),
		Penalty:       int(hdrBytes[x-2])<<8 | int(hdrBytes[x-1]),
	}, nil
}

func (sh *SwitchHeader) serialize() []byte {
    hdrBytes := make([]byte, SwitchHeaderSize)
    labelBytes, _ := hex.DecodeString(strings.ReplaceAll(sh.Label, ".", "") + "00000000")
    copy(hdrBytes[0:8], labelBytes)
    hdrBytes[8] = byte(sh.Congestion<<1) | byte(0)
    hdrBytes[9] = byte(sh.Version<<6) | byte(sh.LabelShift)
    hdrBytes[10] = byte(sh.Penalty >> 8)
    hdrBytes[11] = byte(sh.Penalty)
    return hdrBytes
}