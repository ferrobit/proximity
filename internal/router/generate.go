package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/rules"
)

type proxyGroup struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Proxies  []string `json:"proxies"`
	URL      string   `json:"url,omitempty"`
	Interval int      `json:"interval,omitempty"`
	Timeout  int      `json:"timeout,omitempty"`
}

type tunSection struct {
	Enable     bool     `json:"enable"`
	Device     string   `json:"device"`
	Stack      string   `json:"stack"`
	AutoRoute  bool     `json:"auto-route"`
	AutoDetect bool     `json:"auto-detect-interface"`
	DNSHijack  []string `json:"dns-hijack,omitempty"`
}

type sniffPorts struct {
	Ports []string `json:"ports"`
}

type snifferSection struct {
	Enable       bool                  `json:"enable"`
	OverrideDest bool                  `json:"override-destination"`
	Sniff        map[string]sniffPorts `json:"sniff"`
}

type listenerUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type listener struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Port        int            `json:"port"`
	Listen      string         `json:"listen"`
	UDP         bool           `json:"udp"`
	Users       []listenerUser `json:"users"`
	Certificate string         `json:"certificate,omitempty"`
	PrivateKey  string         `json:"private-key,omitempty"`
}

type Options struct {
	Socket    string
	Capture   bool
	ProxyBind string
}

type document struct {
	MixedPort   int              `json:"mixed-port"`
	AllowLan    bool             `json:"allow-lan"`
	BindAddress string           `json:"bind-address"`
	Mode        string           `json:"mode"`
	LogLevel    string           `json:"log-level"`
	IPv6        bool             `json:"ipv6"`
	Controller  string           `json:"external-controller-unix"`
	FindProcess string           `json:"find-process-mode"`
	GeoUpdate   bool             `json:"geo-auto-update"`
	Profile     map[string]bool  `json:"profile"`
	RoutingMark int              `json:"routing-mark,omitempty"`
	Tun         *tunSection      `json:"tun,omitempty"`
	Sniffer     *snifferSection  `json:"sniffer,omitempty"`
	DNS         *dnsSection      `json:"dns,omitempty"`
	Listeners   []listener       `json:"listeners,omitempty"`
	Proxies     []map[string]any `json:"proxies"`
	Groups      []proxyGroup     `json:"proxy-groups"`
	Rules       []string         `json:"rules"`
}

type buildState struct {
	config   config.Config
	links    map[string]link.Status
	document document
	variants map[string][]rules.Variant
	active   map[string]bool
	warnings []string
	entries  []dnsEntry
	cache    map[string][]dnsUpstream
}

const secureSuffix = "~https"
const fileSuffix = "~ftp"
const securePort = 443
const filePort = 21
const healthTimeout = 5000
const listenAll = "0.0.0.0"
const listenDual = "::"

func Generate(snapshot config.Config, links map[string]link.Status, options Options) ([]byte,
	[]string, error) {
	state := &buildState{config: snapshot, links: links, variants: map[string][]rules.Variant{},
		active:   map[string]bool{config.TargetDirect: true, config.TargetReject: true},
		warnings: []string{}, cache: map[string][]dnsUpstream{}}
	settings := snapshot.Settings
	state.document = document{MixedPort: settings.MixedPort, AllowLan: true,
		BindAddress: settings.BindAddress, Mode: "rule", LogLevel: settings.LogLevel,
		IPv6: settings.IPv6, Controller: options.Socket, FindProcess: "off",
		Profile: map[string]bool{"store-selected": false}, Proxies: []map[string]any{},
		Groups: []proxyGroup{}, Rules: []string{}}
	if options.ProxyBind != "" {
		state.document.BindAddress = options.ProxyBind
	}
	if options.Capture {
		state.document.RoutingMark = link.CaptureMark
		state.attachTun()
	}
	state.buildProxies()
	state.buildGroups()
	state.buildInbounds()
	state.buildRules()
	buffer := bytes.Buffer{}
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state.document); err != nil {
		return nil, nil, fmt.Errorf("failed to encode router configuration: %w", err)
	}
	return buffer.Bytes(), state.warnings, nil
}

func Owner(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, secureSuffix), fileSuffix)
}

func Items(snapshot config.Config) []rules.Item {
	result := []rules.Item{}
	for index, route := range snapshot.Routes {
		if parsed, err := rules.Parse(route.Directive, route.Params); err == nil &&
			route.Enabled {
			result = append(result, rules.Item{Key: route.Key, Label: routeLabel(route, index),
				Directive: route.Directive, Target: route.Target, Rules: parsed})
		}
	}
	return result
}

func Used(snapshot config.Config) map[string]bool {
	result := map[string]bool{snapshot.Settings.DefaultTarget: true}
	for _, route := range snapshot.Routes {
		if route.Enabled {
			result[route.Target] = true
		}
	}
	for _, fallback := range snapshot.Fallbacks {
		if !fallback.Enabled || !result[fallback.ID] {
			continue
		}
		for _, id := range fallback.Outbounds {
			result[id] = true
		}
	}
	return result
}

// NOTE: An echo request has no domain, port or transport, so only IP rules decide by its
// address; a rule that may match an address but cannot be checked here (GEOIP, IP-ASN, a
// source or a process) with a target that is not direct closes the list with a drop. A
// tunnel target (OpenVPN, AmneziaWG) carries ping in the routing table of its outbound.
func Ping(snapshot config.Config, links map[string]link.Status) []link.PingRule {
	state := &buildState{config: snapshot, links: links, variants: map[string][]rules.Variant{},
		active: map[string]bool{config.TargetDirect: true, config.TargetReject: true}}
	state.buildProxies()
	state.buildGroups()
	result := []link.PingRule{}
	for _, route := range snapshot.Routes {
		parsed, err := rules.Parse(route.Directive, route.Params)
		if !route.Enabled || !state.active[route.Target] || err != nil {
			continue
		}
		allow, table := state.direct(route.Target), state.table(route.Target)
		for _, rule := range parsed {
			switch rule.Directive {
			case rules.IPCIDR, rules.IPCIDR6:
				if prefix, err := netip.ParsePrefix(rule.Value); err == nil && rule.Port == 0 {
					result = append(result, link.PingRule{Prefix: prefix.Masked(), Allow: allow,
						Table: table})
				}
			case rules.GeoIP, rules.IPASN, rules.SourceIPCIDR, rules.ProcessName, rules.ProcessPath:
				if !allow && rule.Port == 0 {
					return append(result, link.PingRule{})
				}
			}
		}
	}
	target := snapshot.Settings.DefaultTarget
	if !state.active[target] {
		target = config.TargetDirect
	}
	return append(result, link.PingRule{Allow: state.direct(target), Table: state.table(target)})
}

func setCredentials(proxy map[string]any, username string, password string, secure bool,
	skip bool) {
	if username != "" {
		proxy["username"], proxy["password"] = username, password
	}
	if secure {
		proxy["tls"], proxy["skip-cert-verify"] = true, skip
	}
}

func routeLabel(route config.Route, index int) string {
	if route.ID != "" {
		return "'" + route.ID + "'"
	}
	return fmt.Sprintf("#%v", index+1)
}

func (state *buildState) warnf(format string, arguments ...any) {
	state.warnings = append(state.warnings, fmt.Sprintf(format, arguments...))
}

func (state *buildState) attachTun() {
	state.document.Tun = &tunSection{Enable: true, Device: link.CaptureDevice, Stack: "gvisor",
		DNSHijack: []string{"any:53"}}
	state.document.Sniffer = &snifferSection{Enable: true, Sniff: map[string]sniffPorts{
		"HTTP": {Ports: []string{"80", "8080-8880"}}, "TLS": {Ports: []string{"443", "8443"}},
		"QUIC": {Ports: []string{"443", "8443"}}}}
}

func (state *buildState) buildInbounds() {
	for _, inbound := range state.config.Inbounds {
		target, match := inbound.Target(), "IN-NAME,"+inbound.ID
		if !inbound.Enabled {
			continue
		} else if problem := state.config.InboundProblem(inbound); problem != "" {
			state.warnf("inbound '%v' is skipped: %v", inbound.ID, problem)
			continue
		}
		if server := inbound.OpenVPN; server != nil {
			match = "SRC-IP-CIDR," + server.Network
			if state.document.Tun == nil {
				state.attachTun()
			}
		} else {
			state.document.Listeners = append(state.document.Listeners, state.makeListener(inbound))
		}
		if target == "" {
			continue
		}
		for _, variant := range state.variants[target] {
			state.document.Rules = append(state.document.Rules, fmt.Sprintf(
				"AND,((%v),(DST-PORT,%v)),%v", match, variant.Port, variant.Target))
		}
		state.document.Rules = append(state.document.Rules, match+","+target)
	}
}

func (state *buildState) makeListener(inbound config.Inbound) listener {
	socks := inbound.Socks5
	result := listener{Name: inbound.ID, Type: "socks", Port: inbound.Port, Listen: socks.Listen,
		UDP: socks.UDP, Users: []listenerUser{}}
	if socks.Listen == config.ListenAny && state.config.Settings.IPv6 {
		result.Listen = listenDual
	} else if socks.Listen == config.ListenAny {
		result.Listen = listenAll
	}
	for _, user := range inbound.ActiveUsers() {
		if !inbound.Anonymous {
			result.Users = append(result.Users, listenerUser{user.Name, user.Password})
		}
	}
	if socks.TLS {
		result.Certificate, result.PrivateKey = socks.Certificate, socks.Key
	}
	return result
}

func (state *buildState) buildProxies() {
	for _, outbound := range state.config.Outbounds {
		if !outbound.Enabled {
			continue
		}
		state.active[outbound.ID] = true
		proxy := map[string]any{"name": outbound.ID}
		switch outbound.Type {
		case config.TypeHost:
			proxy["type"], proxy["udp"] = "direct", true
			if host := outbound.Host; host.Mode == config.HostInterface && host.Gateway != "" {
				proxy["routing-mark"] = link.Table(outbound.Slot)
			} else if host.Mode == config.HostInterface {
				proxy["interface-name"] = host.Interface
			}
		case config.TypeSocks5:
			options := outbound.Socks5
			proxy["type"], proxy["server"], proxy["port"] = "socks5", options.Server, options.Port
			proxy["udp"] = options.UDP
			setCredentials(proxy, options.Username, options.Password, options.TLS,
				options.SkipCertVerify)
		case config.TypeHTTP:
			options := outbound.HTTP
			proxy["type"], proxy["server"], proxy["port"] = "http", options.Server, options.Port
			setCredentials(proxy, options.Username, options.Password, options.TLS,
				options.SkipCertVerify)
			state.buildSplit(outbound, proxy)
		default:
			proxy["type"], proxy["udp"] = "direct", true
			proxy["routing-mark"] = link.Table(outbound.Slot)
		}
		state.document.Proxies = append(state.document.Proxies, proxy)
	}
}

func (state *buildState) buildSplit(outbound config.Outbound, proxy map[string]any) {
	options := outbound.HTTP
	if options.Single {
		return
	}
	for _, item := range []struct {
		server string
		port   int
		suffix string
		match  int
	}{{options.HTTPSServer, options.HTTPSPort, secureSuffix, securePort},
		{options.FTPServer, options.FTPPort, fileSuffix, filePort}} {
		if item.server == "" {
			continue
		}
		variant := map[string]any{}
		for key, value := range proxy {
			variant[key] = value
		}
		variant["name"], variant["server"] = outbound.ID+item.suffix, item.server
		variant["port"] = item.port
		state.document.Proxies = append(state.document.Proxies, variant)
		state.variants[outbound.ID] = append(state.variants[outbound.ID],
			rules.Variant{Port: item.match, Target: outbound.ID + item.suffix})
	}
}

func (state *buildState) buildGroups() {
	for _, fallback := range state.config.Fallbacks {
		members := []string{}
		for _, id := range fallback.Outbounds {
			if state.active[id] {
				members = append(members, id)
			}
		}
		if !fallback.Enabled {
			continue
		} else if len(members) == 0 {
			state.warnf("fallback '%v' is skipped: no enabled outbounds", fallback.ID)
			continue
		}
		state.active[fallback.ID] = true
		state.appendGroup(fallback, fallback.ID, members)
		for _, item := range []struct {
			suffix string
			port   int
		}{{secureSuffix, securePort}, {fileSuffix, filePort}} {
			mapped, found := []string{}, false
			for _, id := range members {
				name := id
				for _, variant := range state.variants[id] {
					if variant.Port == item.port {
						name, found = variant.Target, true
					}
				}
				mapped = append(mapped, name)
			}
			if found {
				state.appendGroup(fallback, fallback.ID+item.suffix, mapped)
				state.variants[fallback.ID] = append(state.variants[fallback.ID],
					rules.Variant{Port: item.port, Target: fallback.ID + item.suffix})
			}
		}
	}
}

func (state *buildState) appendGroup(fallback config.Fallback, name string, members []string) {
	group := proxyGroup{Name: name, Type: "fallback", Proxies: members, URL: fallback.URL,
		Interval: fallback.Interval, Timeout: healthTimeout}
	if !fallback.HealthCheck {
		group = proxyGroup{Name: name, Type: "select", Proxies: state.alive(members)}
	}
	state.document.Groups = append(state.document.Groups, group)
}

func (state *buildState) alive(members []string) []string {
	result := []string{}
	for _, name := range members {
		if status, found := state.links[Owner(name)]; !found || status.State == link.StateUp {
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return members
	}
	return result
}

func (state *buildState) direct(target string) bool {
	if target == config.TargetDirect {
		return true
	}
	for _, outbound := range state.config.Outbounds {
		if outbound.ID == target {
			return outbound.Type == config.TypeHost
		}
	}
	for _, group := range state.document.Groups {
		if group.Name == target {
			return !slices.ContainsFunc(group.Proxies, func(member string) bool {
				return !state.direct(member)
			})
		}
	}
	return false
}

func (state *buildState) table(target string) int {
	for _, outbound := range state.config.Outbounds {
		if outbound.ID == target && outbound.IsTunnel() {
			return link.Table(outbound.Slot)
		}
	}
	return 0
}

func (state *buildState) buildRules() {
	for index, route := range state.config.Routes {
		label := routeLabel(route, index)
		if !route.Enabled {
			continue
		} else if !state.active[route.Target] {
			state.warnf("route %v is skipped: target '%v' is disabled or does not exist", label,
				route.Target)
			continue
		}
		parsed, err := rules.Parse(route.Directive, route.Params)
		if err != nil {
			state.warnf("route %v is skipped: %v", label, err)
			continue
		}
		state.noteDomains(route.Target, parsed)
		for _, rule := range parsed {
			state.document.Rules = append(state.document.Rules,
				rule.Lines(route.Target, state.variants[route.Target])...)
			if strings.HasPrefix(rule.Directive, "PROCESS-") {
				state.document.FindProcess = "strict"
			}
		}
	}
	target := state.config.Settings.DefaultTarget
	if !state.active[target] {
		state.warnf("default target '%v' is disabled, using '%v'", target, config.TargetDirect)
		target = config.TargetDirect
	}
	state.document.Rules = append(state.document.Rules, "MATCH,"+target)
	state.buildDNS(target)
}
