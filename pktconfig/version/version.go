// Copyright (c) 2013-2014 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package version

import (
	"fmt"
	"regexp"
	"strings"

	genversion "github.com/pkt-cash/pktd/generated/version"
)

var userAgentName = "unknown" // pktd, pktwallet, pktctl...
var appMajor uint = 0
var appMinor uint = 0
var appPatch uint = 0
var version = "0.0.0-custom"
var custom = true
var prerelease = false
var dirty = false

func init() {
	ver := genversion.Version()
	if len(ver) == 0 {
		// custom build
		return
	}
	tag := "-custom"
	// pktd-v1.1.0-beta-19-gfa3ba767
	if _, err := fmt.Sscanf(ver, "pkt-lightning-wallet-v%d.%d.%d", &appMajor, &appMinor, &appPatch); err == nil {
		tag = ""
		custom = false
		if x := regexp.MustCompile(`-[0-9]+-g[0-9a-f]{8}`).FindString(ver); len(x) > 0 {
			tag += "-" + x[strings.LastIndex(x, "-")+2:]
			prerelease = true
		}
		if strings.Contains(ver, "-dirty") {
			tag += "-dirty"
			dirty = true
		}
	}
	version = fmt.Sprintf("%d.%d.%d%s", appMajor, appMinor, appPatch, tag)
}

func IsCustom() bool {
	return custom
}

func IsDirty() bool {
	return dirty
}

func IsPrerelease() bool {
	return prerelease
}

func AppMajorVersion() uint {
	return appMajor
}
func AppMinorVersion() uint {
	return appMinor
}
func AppPatchVersion() uint {
	return appPatch
}

func SetUserAgentName(ua string) {
	if userAgentName != "unknown" {
		panic("setting useragent to [" + ua +
			"] failed, useragent was already set to [" + userAgentName + "]")
	}
	userAgentName = ua
}

func Version() string {
	return version
}

func UserAgentName() string {
	return userAgentName
}

func UserAgentVersion() string {
	return version
}
