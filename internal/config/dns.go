package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"proximity/internal/wgconf"
)

type DNSOptions struct {
	Servers []string `json:"servers"`
	Exit    string   `json:"exit"`
}

type DNSServer struct {
	Scheme string
	Host   string
	Port   int
	Path   string
	Plain  bool
}

const (
	DNSUDP   = "udp"
	DNSTCP   = "tcp"
	DNSTLS   = "tls"
	DNSHTTPS = "https"
	DNSQUIC  = "quic"
)

const (
	CarrierUDP = "udp"
	CarrierTCP = "tcp"
)

const DNSSystem = "system"
const maxDNSServers = 8
const dnsDefaultPath = "/dns-query"

var dnsPorts = map[string]int{DNSUDP: 53, DNSTCP: 53, DNSTLS: 853, DNSHTTPS: 443, DNSQUIC: 853}
var openvpnHostDirectives = []string{"remote", "http-proxy", "socks-proxy"}

func ParseDNSServer(text string) (DNSServer, error) {
	text = strings.TrimSpace(text)
	if !strings.Contains(text, "://") {
		return parsePlainServer(text)
	}
	address, err := url.Parse(text)
	if err != nil {
		return DNSServer{}, fmt.Errorf("cannot parse %q as DNS server: %w", text, err)
	}
	result := DNSServer{Scheme: strings.ToLower(address.Scheme), Host: address.Hostname(),
		Path: address.Path}
	if result.Port = dnsPorts[result.Scheme]; result.Port == 0 {
		return DNSServer{}, fmt.Errorf("unexpected scheme of DNS server '%v'", text)
	} else if address.User != nil || address.RawQuery != "" || address.Fragment != "" ||
		strings.Contains(text, "#") {
		return DNSServer{}, fmt.Errorf("unexpected part in DNS server '%v'", text)
	} else if result.Path != "" && result.Scheme != DNSHTTPS {
		return DNSServer{}, fmt.Errorf("unexpected path in DNS server '%v'", text)
	}
	if port := address.Port(); port != "" {
		if result.Port, err = strconv.Atoi(port); err != nil || result.Port < 1 ||
			result.Port > 65535 {
			return DNSServer{}, fmt.Errorf("invalid port of DNS server '%v'", text)
		}
	}
	if result.Scheme == DNSHTTPS && result.Path == "" {
		result.Path = dnsDefaultPath
	}
	if _, err = netip.ParseAddr(result.Host); err == nil {
		return result, nil
	} else if result.Scheme == DNSUDP || result.Scheme == DNSTCP {
		return DNSServer{}, fmt.Errorf("invalid DNS server '%v', expected IP address", text)
	} else if !hostPattern.MatchString(result.Host) {
		return DNSServer{}, fmt.Errorf("invalid host of DNS server '%v'", text)
	}
	return result, nil
}

func parsePlainServer(text string) (DNSServer, error) {
	result := DNSServer{Scheme: DNSUDP, Port: dnsPorts[DNSUDP], Plain: true}
	if address, err := netip.ParseAddr(text); err == nil {
		result.Host = address.String()
		return result, nil
	}
	address, err := netip.ParseAddrPort(text)
	if err != nil || address.Port() == 0 {
		return DNSServer{}, fmt.Errorf("invalid DNS server '%v', expected IP address or URL", text)
	}
	result.Host, result.Port = address.Addr().String(), int(address.Port())
	return result, nil
}

func normalizeServers(servers []string) []string {
	result := []string{}
	for _, line := range servers {
		for _, item := range strings.FieldsFunc(line, func(symbol rune) bool {
			return symbol == ',' || symbol == ';' || symbol == ' ' || symbol == '\t'
		}) {
			if parsed, err := ParseDNSServer(item); err == nil {
				item = parsed.String()
			}
			if !slices.Contains(result, item) {
				result = append(result, item)
			}
		}
	}
	return result
}

func validateServers(servers []string) error {
	if len(servers) > maxDNSServers {
		return fmt.Errorf("too many DNS servers (%v), expected up to %v", len(servers),
			maxDNSServers)
	}
	for _, server := range servers {
		if _, err := ParseDNSServer(server); err != nil {
			return err
		}
	}
	return nil
}

func HostName(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil || host == "" ||
		!hostPattern.MatchString(host) {
		return ""
	}
	return host
}

func OpenVPNHosts(text string) []string {
	result, block := []string{}, ""
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		} else if name := strings.Trim(fields[0], "<>/"); strings.HasPrefix(fields[0], "</") {
			block = ""
		} else if strings.HasPrefix(fields[0], "<") && block == "" {
			block = name
		} else if directive := strings.TrimPrefix(fields[0], "--"); len(fields) > 1 &&
			(block == "" || block == "connection") &&
			slices.Contains(openvpnHostDirectives, directive) {
			if host := HostName(fields[1]); host != "" && !slices.Contains(result, host) {
				result = append(result, host)
			}
		}
	}
	return result
}

func (server DNSServer) Address() string {
	return net.JoinHostPort(server.Host, strconv.Itoa(server.Port))
}

func (server DNSServer) Named() string {
	return HostName(server.Host)
}

func (server DNSServer) String() string {
	if server.Plain && server.Port == dnsPorts[DNSUDP] {
		return server.Host
	} else if server.Plain {
		return server.Address()
	}
	return server.URL(server.Scheme)
}

func (server DNSServer) URL(scheme string) string {
	host := server.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if server.Port != dnsPorts[scheme] {
		host += ":" + strconv.Itoa(server.Port)
	}
	if scheme == DNSHTTPS {
		return scheme + "://" + host + server.Path
	}
	return scheme + "://" + host
}

func (options *DNSOptions) normalize() {
	options.Servers, options.Exit = normalizeServers(options.Servers), strings.TrimSpace(options.Exit)
}

func (options *DNSOptions) validate() error {
	return validateServers(options.Servers)
}

func (options DNSOptions) clone() DNSOptions {
	options.Servers = slices.Clone(options.Servers)
	return options
}

func (outbound *Outbound) Carrier() string {
	if outbound.Type == TypeHTTP || (outbound.Socks5 != nil && !outbound.Socks5.UDP) {
		return CarrierTCP
	}
	return CarrierUDP
}

func (outbound *Outbound) UsesDNS() bool {
	return (outbound.OpenVPN != nil && outbound.OpenVPN.UseDNS) ||
		(outbound.AmneziaWG != nil && outbound.AmneziaWG.UseDNS)
}

// NOTE: The names an outbound must resolve before it can connect: they are never asked
// through the outbound itself.
func (outbound *Outbound) Hosts() []string {
	result := []string{}
	switch outbound.Type {
	case TypeSocks5:
		result = append(result, outbound.Socks5.Server)
	case TypeHTTP:
		if result = append(result, outbound.HTTP.Server); outbound.IsSplit() {
			result = append(result, outbound.HTTP.HTTPSServer, outbound.HTTP.FTPServer)
		}
	case TypeOpenVPN:
		result = OpenVPNHosts(outbound.OpenVPN.Config)
	case TypeAmneziaWG:
		if parsed, err := wgconf.Parse(outbound.AmneziaWG.Config); err == nil {
			result = parsed.EndpointHosts()
		}
	}
	names := []string{}
	for _, host := range result {
		if name := HostName(host); name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func (config *Config) ExitExists(exit string) bool {
	return exit == "" || exit == ViaHost || exit == ViaRouter || config.findOutbound(exit) >= 0 ||
		config.findFallback(exit) >= 0
}

func (config *Config) SetSettings(settings Settings) error {
	config.Settings = settings
	return config.checkExit(strings.TrimSpace(settings.DNS.Exit))
}

func (config *Config) checkExit(exit string) error {
	if !config.ExitExists(exit) {
		return fmt.Errorf("DNS exit '%v' does not exist", exit)
	}
	return nil
}

func (config *Config) renameExit(previous string, current string) {
	for index := range config.Outbounds {
		if config.Outbounds[index].DNS.Exit == previous {
			config.Outbounds[index].DNS.Exit = current
		}
	}
	if config.Settings.DNS.Exit == previous {
		config.Settings.DNS.Exit = current
	}
}
