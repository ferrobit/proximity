package link

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

type PingRule struct {
	Prefix netip.Prefix
	Allow  bool
	Table  int
}

type captureState struct {
	device string
	active bool
	ipv6   bool
	index  int
	ping   string
}

type pingFamily struct {
	six      bool
	protocol string
	match    string
	number   string
	ip       string
	source   string
	fake     string
}

const CaptureDevice = devicePrefix + "tun"
const CaptureMark = tableBase
const CaptureFakeIPRange = "198.18.0.1/16"
const captureSource = "198.18.0.0/30"
const captureSource6 = "fdfe:dcba:9876::/126"
const bypassPriority = "5150"
const dnsPriority = "5155"
const localPriority = "5160"
const pingPriority = "5165"
const pingDefaultPriority = "5166"
const capturePriority = "5170"
const maxPingRules = 4096

// iproute2 6.1 (the container image) accepts only numeric protocols in `ip rule`.
const dnsUDP = "17"
const dnsTCP = "6"
const pingChain = "PROXIMITY-PING"

var pingFamilies = []pingFamily{{false, "icmp", "--icmp-type", "1", "-4", captureSource,
	"198.18.0.0/16"}, {true, "ipv6-icmp", "--icmpv6-type", "58", "-6", captureSource6, ""}}
var tunnelDevices = []string{devicePrefix + "o+", devicePrefix + "a+"}

// NOTE: mihomo answers an echo request that came into the TUN with its own request sent
// directly, past the rules; ping lists the addresses where such a request may go out.
func (manager *Manager) Capture(enabled bool, ipv6 bool, ping []PingRule) bool {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if enabled = enabled && manager.options.HostNetwork; !enabled {
		ping = nil
	}
	manager.capture.filter(manager.options.Tools, ping, ipv6)
	return manager.capture.ensure(manager.options.Tools, enabled, ipv6)
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
			// NOTE: Without "from" the strict reverse path filter (rp_filter=1) finds this rule
			// for an answer of a DNS server to the router itself and drops the answer.
			for _, protocol := range []string{dnsUDP, dnsTCP} {
				_, _ = tools.ip(family, "rule", "add", "priority", dnsPriority, "from", source, "iif",
					"lo", "ipproto", protocol, "dport", "53", "lookup", table)
			}
			_, _ = tools.ip(family, "rule", "add", "priority", capturePriority, "from", source, "iif",
				"lo", "lookup", table)
		}
	}
	capture.active, capture.ipv6, capture.index = true, ipv6, 0
}

func (capture *captureState) remove(tools Tools) {
	for _, family := range []string{"-4", "-6"} {
		for _, priority := range []string{bypassPriority, dnsPriority, localPriority, capturePriority} {
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

// NOTE: Ping of a tunnel route does not enter the TUN: an `ip rule` sends it into the table
// of the outbound and its source, the address of the TUN that ping chose, is masqueraded.
// The rules keep the order of the routes, so an earlier route to the TUN still wins. The
// rule of the default target has its own priority: the kernel takes a rule without "to"
// for a copy of a rule with the same table and "to".
func (capture *captureState) filter(tools Tools, ping []PingRule, ipv6 bool) {
	signature := fmt.Sprint(ping != nil, ipv6, ping)
	if signature == capture.ping {
		return
	}
	capture.removePing(tools)
	if capture.ping = signature; ping == nil {
		return
	}
	last := -1
	for index, rule := range ping {
		if rule.Table != 0 {
			last = index
		}
	}
	for _, family := range pingFamilies {
		binary := family.binary(tools)
		if family.six && !ipv6 {
			continue
		} else if _, err := tools.firewallOf(binary, "-N", pingChain); err != nil {
			continue
		}
		for _, rule := range ping {
			arguments, verdict := []string{"-A", pingChain}, "DROP"
			if !family.owns(rule) {
				continue
			} else if rule.Prefix.IsValid() {
				arguments = append(arguments, "-d", rule.Prefix.String())
			}
			if rule.Allow {
				verdict = "RETURN"
			}
			_, _ = tools.firewallOf(binary, append(arguments, "-j", verdict)...)
		}
		_, _ = tools.firewallOf(binary, append([]string{"-I", "OUTPUT", "1"}, family.jump()...)...)
		if last < 0 {
			continue
		}
		for _, rule := range ping[:last+1] {
			family.route(tools, rule)
		}
		for _, device := range tunnelDevices {
			_, _ = tools.firewallOf(binary, append([]string{"-A", "POSTROUTING"},
				family.masquerade(device)...)...)
		}
	}
}

func (capture *captureState) removePing(tools Tools) {
	for _, family := range pingFamilies {
		binary := family.binary(tools)
		for range 4 {
			if _, err := tools.firewallOf(binary, append([]string{"-D", "OUTPUT"},
				family.jump()...)...); err != nil {
				break
			}
		}
		_, _ = tools.firewallOf(binary, "-F", pingChain)
		_, _ = tools.firewallOf(binary, "-X", pingChain)
		for _, device := range tunnelDevices {
			for range 4 {
				if _, err := tools.firewallOf(binary, append([]string{"-D", "POSTROUTING"},
					family.masquerade(device)...)...); err != nil {
					break
				}
			}
		}
		for _, priority := range []string{pingPriority, pingDefaultPriority} {
			for range maxPingRules {
				if _, err := tools.ip(family.ip, "rule", "del", "priority", priority); err != nil {
					break
				}
			}
		}
	}
}

func (family pingFamily) binary(tools Tools) string {
	if family.six {
		return tools.Ip6tables
	}
	return tools.Iptables
}

func (family pingFamily) jump() []string {
	return []string{"-m", "mark", "--mark", strconv.Itoa(CaptureMark), "-p", family.protocol,
		family.match, "echo-request", "-j", pingChain}
}

func (family pingFamily) masquerade(device string) []string {
	return []string{"-t", "nat", "-s", family.source, "-o", device, "-p", family.protocol, "-j",
		"MASQUERADE"}
}

func (family pingFamily) owns(rule PingRule) bool {
	return !rule.Prefix.IsValid() || rule.Prefix.Addr().Is6() == family.six
}

func (family pingFamily) route(tools Tools, rule PingRule) {
	table, priority, rest := strconv.Itoa(CaptureMark), pingPriority, []string{}
	if !family.owns(rule) {
		return
	} else if rule.Table != 0 {
		table = strconv.Itoa(rule.Table)
	}
	if rule.Prefix.IsValid() {
		rest = []string{"to", rule.Prefix.String()}
	} else if priority = pingDefaultPriority; family.fake != "" {
		_, _ = tools.ip(family.ip, "rule", "add", "priority", pingPriority, "ipproto",
			family.number, "to", family.fake, "lookup", strconv.Itoa(CaptureMark))
	}
	_, _ = tools.ip(append(append([]string{family.ip, "rule", "add", "priority", priority,
		"ipproto", family.number}, rest...), "lookup", table)...)
}
