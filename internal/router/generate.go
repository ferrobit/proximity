package router

import (
	"bytes"
	"encoding/json"
	"fmt"
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

type Options struct {
	Socket    string
	Capture   bool
	ProxyBind string
}

type dnsSection struct {
	Enable       bool                `json:"enable"`
	IPv6         bool                `json:"ipv6"`
	EnhancedMode string              `json:"enhanced-mode,omitempty"`
	FakeIPRange  string              `json:"fake-ip-range,omitempty"`
	Nameserver   []string            `json:"nameserver"`
	Policy       map[string][]string `json:"nameserver-policy"`
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
}

const secureSuffix = "~https"
const fileSuffix = "~ftp"
const securePort = 443
const filePort = 21
const healthTimeout = 5000

func Generate(snapshot config.Config, links map[string]link.Status, options Options) ([]byte,
	[]string, error) {
	state := &buildState{config: snapshot, links: links, variants: map[string][]rules.Variant{},
		active:   map[string]bool{config.TargetDirect: true, config.TargetReject: true},
		warnings: []string{}}
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
		state.document.Tun = &tunSection{Enable: true, Device: link.CaptureDevice, Stack: "gvisor",
			DNSHijack: []string{"any:53"}}
		state.document.Sniffer = &snifferSection{Enable: true, Sniff: map[string]sniffPorts{
			"HTTP": {Ports: []string{"80", "8080-8880"}}, "TLS": {Ports: []string{"443", "8443"}},
			"QUIC": {Ports: []string{"443", "8443"}}}}
	}
	state.buildProxies()
	state.buildGroups()
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
	for index, filter := range snapshot.Filters {
		if parsed, err := rules.Parse(filter.Directive, filter.Params); err == nil &&
			filter.Enabled {
			result = append(result, rules.Item{Key: filter.Key, Label: filterLabel(filter, index),
				Directive: filter.Directive, Target: filter.Target, Rules: parsed})
		}
	}
	return result
}

func Used(snapshot config.Config) map[string]bool {
	result := map[string]bool{snapshot.Settings.DefaultTarget: true}
	for _, filter := range snapshot.Filters {
		if filter.Enabled {
			result[filter.Target] = true
		}
	}
	for _, fallback := range snapshot.Fallbacks {
		if !fallback.Enabled || !result[fallback.ID] {
			continue
		}
		for _, id := range fallback.Endpoints {
			result[id] = true
		}
	}
	return result
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

func filterLabel(filter config.Filter, index int) string {
	if filter.ID != "" {
		return "'" + filter.ID + "'"
	}
	return fmt.Sprintf("#%v", index+1)
}

func (state *buildState) warnf(format string, arguments ...any) {
	state.warnings = append(state.warnings, fmt.Sprintf(format, arguments...))
}

func (state *buildState) buildProxies() {
	for _, endpoint := range state.config.Endpoints {
		if !endpoint.Enabled {
			continue
		}
		state.active[endpoint.ID] = true
		proxy := map[string]any{"name": endpoint.ID}
		switch endpoint.Type {
		case config.TypeHost:
			proxy["type"], proxy["udp"] = "direct", true
			if host := endpoint.Host; host.Mode == config.HostInterface && host.Gateway != "" {
				proxy["routing-mark"] = link.Table(endpoint.Slot)
			} else if host.Mode == config.HostInterface {
				proxy["interface-name"] = host.Interface
			}
		case config.TypeSocks5:
			options := endpoint.Socks5
			proxy["type"], proxy["server"], proxy["port"] = "socks5", options.Server, options.Port
			proxy["udp"] = options.UDP
			setCredentials(proxy, options.Username, options.Password, options.TLS,
				options.SkipCertVerify)
		case config.TypeHTTP:
			options := endpoint.HTTP
			proxy["type"], proxy["server"], proxy["port"] = "http", options.Server, options.Port
			setCredentials(proxy, options.Username, options.Password, options.TLS,
				options.SkipCertVerify)
			state.buildSplit(endpoint, proxy)
		default:
			proxy["type"], proxy["udp"] = "direct", true
			proxy["routing-mark"] = link.Table(endpoint.Slot)
		}
		state.document.Proxies = append(state.document.Proxies, proxy)
	}
}

func (state *buildState) buildSplit(endpoint config.Endpoint, proxy map[string]any) {
	options := endpoint.HTTP
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
		variant["name"], variant["server"] = endpoint.ID+item.suffix, item.server
		variant["port"] = item.port
		state.document.Proxies = append(state.document.Proxies, variant)
		state.variants[endpoint.ID] = append(state.variants[endpoint.ID],
			rules.Variant{Port: item.match, Target: endpoint.ID + item.suffix})
	}
}

func (state *buildState) buildGroups() {
	for _, fallback := range state.config.Fallbacks {
		members := []string{}
		for _, id := range fallback.Endpoints {
			if state.active[id] {
				members = append(members, id)
			}
		}
		if !fallback.Enabled {
			continue
		} else if len(members) == 0 {
			state.warnf("fallback '%v' is skipped: no enabled endpoints", fallback.ID)
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

func (state *buildState) buildRules() {
	policy := map[string][]string{}
	for index, filter := range state.config.Filters {
		label := filterLabel(filter, index)
		if !filter.Enabled {
			continue
		} else if !state.active[filter.Target] {
			state.warnf("filter %v is skipped: target '%v' is disabled or does not exist", label,
				filter.Target)
			continue
		}
		parsed, err := rules.Parse(filter.Directive, filter.Params)
		if err != nil {
			state.warnf("filter %v is skipped: %v", label, err)
			continue
		}
		servers := state.nameservers(filter.Target)
		for _, rule := range parsed {
			state.document.Rules = append(state.document.Rules,
				rule.Lines(filter.Target, state.variants[filter.Target])...)
			if strings.HasPrefix(rule.Directive, "PROCESS-") {
				state.document.FindProcess = "strict"
			}
			if rule.Directive == rules.DomainSuffix && len(servers) != 0 {
				policy["+."+rule.Value] = servers
			} else if rule.Directive == rules.Domain && len(servers) != 0 {
				policy[rule.Value] = servers
			}
		}
	}
	for _, endpoint := range state.config.Endpoints {
		servers := state.nameservers(endpoint.ID)
		for _, domain := range state.links[endpoint.ID].Domains {
			if len(servers) != 0 && domain != "" {
				policy["+."+domain] = servers
			}
		}
	}
	target := state.config.Settings.DefaultTarget
	if !state.active[target] {
		state.warnf("default target '%v' is disabled, using '%v'", target, config.TargetDirect)
		target = config.TargetDirect
	}
	state.document.Rules = append(state.document.Rules, "MATCH,"+target)
	if state.document.Tun != nil || len(policy) != 0 {
		dns := &dnsSection{Enable: true, IPv6: state.config.Settings.IPv6,
			Nameserver: []string{"system"}, Policy: policy}
		if state.document.Tun != nil {
			dns.EnhancedMode = "fake-ip"
			dns.FakeIPRange = link.CaptureFakeIPRange
		}
		state.document.DNS = dns
	}
}

func (state *buildState) nameservers(id string) []string {
	endpoint, found := state.config.FindEndpoint(id)
	if !found || !endpoint.Enabled || !endpoint.IsTunnel() {
		return nil
	} else if endpoint.OpenVPN != nil && !endpoint.OpenVPN.UseDNS {
		return nil
	} else if endpoint.AmneziaWG != nil && !endpoint.AmneziaWG.UseDNS {
		return nil
	}
	result := []string{}
	for _, server := range state.links[id].DNS {
		if strings.Contains(server, ":") {
			server = "[" + server + "]"
		}
		result = append(result, server+"#"+id)
	}
	return result
}
