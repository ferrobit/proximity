package link

import (
	"net"
	"strconv"
)

type captureState struct {
	device string
	active bool
	ipv6   bool
	index  int
}

const CaptureDevice = devicePrefix + "tun"
const CaptureMark = tableBase
const captureSource = "198.18.0.0/30"
const captureSource6 = "fdfe:dcba:9876::/126"
const bypassPriority = "5150"
const localPriority = "5160"
const capturePriority = "5170"

func (manager *Manager) Capture(enabled bool, ipv6 bool) bool {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	return manager.capture.ensure(manager.options.Tools, enabled && manager.options.HostNetwork,
		ipv6)
}

func (capture *captureState) ensure(tools Tools, enabled bool, ipv6 bool) bool {
	if !enabled {
		if capture.active {
			capture.remove(tools)
		}
		return false
	} else if !capture.active || capture.ipv6 != ipv6 {
		capture.remove(tools)
		capture.install(tools, ipv6)
	}
	device, err := net.InterfaceByName(capture.device)
	if err != nil {
		capture.index = 0
		return false
	} else if device.Index == capture.index {
		return true
	}
	table := strconv.Itoa(CaptureMark)
	if _, err = tools.ip("-4", "route", "replace", "default", "dev", capture.device, "table",
		table); err != nil {
		return false
	}
	if capture.index = device.Index; ipv6 {
		_, _ = tools.ip("-6", "route", "replace", "default", "dev", capture.device, "table", table)
	}
	return true
}

func (capture *captureState) install(tools Tools, ipv6 bool) {
	table := strconv.Itoa(CaptureMark)
	families := map[string][]string{"-4": {"0.0.0.0/32", captureSource}}
	if ipv6 {
		families["-6"] = []string{"::/128", captureSource6}
	}
	for family, sources := range families {
		_, _ = tools.ip(family, "rule", "add", "priority", bypassPriority, "fwmark", table, "lookup",
			"main")
		_, _ = tools.ip(family, "rule", "add", "priority", localPriority, "lookup", "main",
			"suppress_prefixlength", "0")
		for _, source := range sources {
			_, _ = tools.ip(family, "rule", "add", "priority", capturePriority, "from", source, "iif",
				"lo", "lookup", table)
		}
	}
	capture.active, capture.ipv6, capture.index = true, ipv6, 0
}

func (capture *captureState) remove(tools Tools) {
	for _, family := range []string{"-4", "-6"} {
		for _, priority := range []string{bypassPriority, localPriority, capturePriority} {
			for range 8 {
				if _, err := tools.ip(family, "rule", "del", "priority", priority); err != nil {
					break
				}
			}
		}
		_, _ = tools.ip(family, "route", "flush", "table", strconv.Itoa(CaptureMark))
	}
	capture.active, capture.index = false, 0
}
