package config

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

type InboundUser struct {
	Enabled  bool   `json:"enabled"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type Socks5Server struct {
	Listen      string `json:"listen"`
	UDP         bool   `json:"udp"`
	TLS         bool   `json:"tls"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

type OpenVPNServer struct {
	Server            string   `json:"server"`
	PublicPort        int      `json:"public_port"`
	Listen            string   `json:"listen"`
	Protocol          string   `json:"protocol"`
	Network           string   `json:"network"`
	RedirectGateway   bool     `json:"redirect_gateway"`
	Routes            []string `json:"routes"`
	DNS               []string `json:"dns"`
	ClientToClient    bool     `json:"client_to_client"`
	MaxClients        int      `json:"max_clients"`
	KeepaliveInterval int      `json:"keepalive_interval"`
	KeepaliveTimeout  int      `json:"keepalive_timeout"`
	Renegotiation     int      `json:"renegotiation"`
	MTU               int      `json:"mtu"`
	Ciphers           string   `json:"ciphers"`
	Digest            string   `json:"digest"`
	TLSVersion        string   `json:"tls_version"`
	Verbosity         int      `json:"verbosity"`
	OTP               bool     `json:"otp"`
	OTPKey            string   `json:"otp_key"`
	CACertificate     string   `json:"ca_certificate"`
	CAKey             string   `json:"ca_key"`
	Certificate       string   `json:"certificate"`
	Key               string   `json:"key"`
	ClientCertificate string   `json:"client_certificate"`
	ClientKey         string   `json:"client_key"`
	TLSCrypt          string   `json:"tls_crypt"`
}

type Inbound struct {
	ID               string         `json:"id"`
	Type             string         `json:"type"`
	Enabled          bool           `json:"enabled"`
	Autostart        bool           `json:"autostart"`
	Attempts         int            `json:"attempts"`
	ReconnectTimeout int            `json:"reconnect_timeout"`
	Recovery         *Recovery      `json:"recovery,omitempty"`
	Statistics       bool           `json:"statistics"`
	Verbose          bool           `json:"verbose"`
	Port             int            `json:"port"`
	Outbound         string         `json:"outbound"`
	Anonymous        bool           `json:"anonymous"`
	Users            []InboundUser  `json:"users"`
	Socks5           *Socks5Server  `json:"socks5,omitempty"`
	OpenVPN          *OpenVPNServer `json:"openvpn,omitempty"`
}

const (
	ViaHost   = "@host"
	ViaRouter = "@router"
)

const (
	ProtocolUDP = "udp"
	ProtocolTCP = "tcp"
)

const InboundPortFirst = 7800
const InboundPortLast = 7898
const ListenAny = "*"
const maxInboundUsers = 256
const maxNameServers = 4
const maxPushedRoutes = 64
const defaultMaxClients = 32
const defaultKeepaliveInterval = 10
const defaultKeepaliveTimeout = 60
const defaultRenegotiation = 3600
const minRenegotiation = 120 // NOTE: OpenVPN requires token renewal time >= 2 * hand-window.
const defaultMTU = 1420      // NOTE: With the transport headers a packet still fits 1500 bytes.
const defaultVerbosity = 3
const defaultCiphers = "AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305"
const defaultDigest = "SHA256"
const defaultTLSVersion = "1.2"
const networkTemplate = "10.%v.%v.0/24"
const networkBase = 77
const networkLimit = 255
const reservedNetwork = "198.18.0.0/15"

var inboundLabels = map[string]string{TypeSocks5: "SOCKS5", TypeOpenVPN: "OpenVPN"}
var digests = []string{defaultDigest, "SHA384", "SHA512"}
var tlsVersions = []string{defaultTLSVersion, "1.3"}
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`)
var secretPattern = regexp.MustCompile(`^[!-~]{1,64}$`)
var cipherPattern = regexp.MustCompile(`^[A-Za-z0-9-]+(:[A-Za-z0-9-]+)*$`)

func validateListen(listen string) error {
	if address, err := netip.ParseAddr(listen); listen != ListenAny && (err != nil ||
		!address.Is4()) {
		return fmt.Errorf("invalid listen address '%v'", listen)
	}
	return nil
}

func cleanList(items []string) []string {
	result := []string{}
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func (config *Config) NextInboundID(label string) string {
	for index := 1; ; index++ {
		if id := fmt.Sprintf("%v %v", label, index); config.findInbound(id) < 0 {
			return id
		}
	}
}

func (config *Config) AddInbound(inbound Inbound, now time.Time) (Inbound, error) {
	if inboundLabels[inbound.Type] == "" {
		return Inbound{}, fmt.Errorf("unexpected inbound type '%v'", inbound.Type)
	}
	if inbound.normalize(); inbound.ID == "" {
		inbound.ID = config.NextInboundID(inboundLabels[inbound.Type])
	}
	if inbound.Port == 0 {
		inbound.Port = config.freePort()
	}
	if config.findInbound(inbound.ID) >= 0 {
		return Inbound{}, fmt.Errorf("ID '%v' already exists", inbound.ID)
	} else if err := config.prepareInbound(&inbound, -1, now); err != nil {
		return Inbound{}, err
	}
	config.Inbounds = append(config.Inbounds, inbound)
	return inbound, nil
}

func (config *Config) UpdateInbound(id string, inbound Inbound, now time.Time) (Inbound, error) {
	index := config.findInbound(id)
	if index < 0 {
		return Inbound{}, fmt.Errorf("inbound '%v' does not exist", id)
	}
	current := config.Inbounds[index]
	inbound.Type = current.Type
	if inbound.normalize(); inbound.ID == "" {
		inbound.ID = id
	}
	if inbound.Port == 0 {
		inbound.Port = current.Port
	}
	if inbound.ID != id && config.findInbound(inbound.ID) >= 0 {
		return Inbound{}, fmt.Errorf("ID '%v' already exists", inbound.ID)
	} else if err := config.prepareInbound(&inbound, index, now); err != nil {
		return Inbound{}, err
	}
	config.Inbounds[index] = inbound
	return inbound, nil
}

func (config *Config) RemoveInbound(id string) error {
	index := config.findInbound(id)
	if index < 0 {
		return fmt.Errorf("inbound '%v' does not exist", id)
	}
	config.Inbounds = slices.Delete(config.Inbounds, index, index+1)
	return nil
}

func (config *Config) MoveInbound(id string, delta int) error {
	return moveItem(config.Inbounds, config.findInbound(id), delta)
}

func (config *Config) FindInbound(id string) (Inbound, bool) {
	if index := config.findInbound(id); index >= 0 {
		return config.Inbounds[index], true
	}
	return Inbound{}, false
}

func (config *Config) InboundProblem(inbound Inbound) string {
	index := config.findOutbound(inbound.Outbound)
	if !inbound.Direct() && inbound.Outbound != ViaRouter && index < 0 {
		return fmt.Sprintf("outbound '%v' does not exist", inbound.Outbound)
	} else if index >= 0 && !config.Outbounds[index].Enabled {
		return fmt.Sprintf("outbound '%v' is disabled", inbound.Outbound)
	} else if !inbound.Anonymous && len(inbound.ActiveUsers()) == 0 {
		return "anonymous connections are not allowed and enabled users do not exist"
	}
	return ""
}

// NOTE: Default tunnel networks differ between instances, so two of them can be linked
// without editing the network; the number is taken from the secret of the instance.
func (config *Config) networkBase() int {
	if config.Secret == "" {
		return networkBase
	}
	sum := sha256.Sum256([]byte(config.Secret))
	return 64 + int(sum[0])%128
}

func (config *Config) prepareInbound(inbound *Inbound, index int, now time.Time) error {
	taken := []netip.Prefix{}
	for position, other := range config.Inbounds {
		if position == index {
			continue
		} else if other.Port == inbound.Port {
			return fmt.Errorf("port (%v) is used by inbound '%v'", inbound.Port, other.ID)
		} else if prefix, found := other.network(); found {
			taken = append(taken, prefix)
		}
	}
	if inbound.Port == 0 {
		return fmt.Errorf("free port does not exist in range %v..%v", InboundPortFirst,
			InboundPortLast)
	} else if !inbound.Direct() && inbound.Outbound != ViaRouter &&
		config.findOutbound(inbound.Outbound) < 0 {
		return fmt.Errorf("outbound '%v' does not exist", inbound.Outbound)
	} else if err := inbound.complete(taken, config.networkBase(), now); err != nil {
		return err
	} else if err = inbound.validate(); err != nil {
		return err
	}
	for _, prefix := range taken {
		if current, found := inbound.network(); found && current.Overlaps(prefix) {
			return fmt.Errorf("network '%v' overlaps network '%v' of another inbound", current,
				prefix)
		}
	}
	return nil
}

func (config *Config) freePort() int {
	for port := InboundPortFirst; port <= InboundPortLast; port++ {
		if !slices.ContainsFunc(config.Inbounds, func(inbound Inbound) bool {
			return inbound.Port == port
		}) {
			return port
		}
	}
	return 0
}

func (config *Config) findInbound(id string) int {
	return slices.IndexFunc(config.Inbounds, func(inbound Inbound) bool {
		return inbound.ID == id
	})
}

func (inbound Inbound) Clone() Inbound {
	inbound.Users = slices.Clone(inbound.Users)
	if inbound.Socks5 != nil {
		socks := *inbound.Socks5
		inbound.Socks5 = &socks
	}
	if inbound.OpenVPN != nil {
		server := *inbound.OpenVPN
		server.Routes, server.DNS = slices.Clone(server.Routes), slices.Clone(server.DNS)
		inbound.OpenVPN = &server
	}
	if inbound.Recovery != nil {
		recovery := *inbound.Recovery
		inbound.Recovery = &recovery
	}
	return inbound
}

func (inbound Inbound) Slot() int {
	return inbound.Port - InboundPortFirst + 1
}

func (inbound Inbound) Direct() bool {
	return inbound.Outbound == ViaHost
}

func (inbound Inbound) Target() string {
	if inbound.Direct() {
		return TargetDirect
	} else if inbound.Outbound == ViaRouter {
		return ""
	}
	return inbound.Outbound
}

func (inbound Inbound) ActiveUsers() []InboundUser {
	result := []InboundUser{}
	for _, user := range inbound.Users {
		if user.Enabled {
			result = append(result, user)
		}
	}
	return result
}

func (inbound Inbound) Network() (netip.Prefix, bool) {
	return inbound.network()
}

func (inbound Inbound) network() (netip.Prefix, bool) {
	if inbound.OpenVPN == nil {
		return netip.Prefix{}, false
	}
	prefix, err := netip.ParsePrefix(inbound.OpenVPN.Network)
	return prefix, err == nil
}

func (inbound *Inbound) normalize() {
	inbound.ID = strings.TrimSpace(inbound.ID)
	if inbound.Attempts == 0 {
		inbound.Attempts = defaultAttempts
	}
	if inbound.ReconnectTimeout == 0 {
		inbound.ReconnectTimeout = defaultReconnectTimeout
	}
	inbound.Recovery = normalizeRecovery(inbound.Recovery)
	if inbound.Outbound = strings.TrimSpace(inbound.Outbound); inbound.Outbound == "" {
		inbound.Outbound = ViaRouter
	}
	if inbound.Users == nil {
		inbound.Users = []InboundUser{}
	}
	for index := range inbound.Users {
		inbound.Users[index].Name = strings.TrimSpace(inbound.Users[index].Name)
	}
	if inbound.Type != TypeSocks5 {
		inbound.Socks5 = nil
	} else if inbound.Socks5 == nil {
		inbound.Socks5 = &Socks5Server{UDP: true}
	}
	if inbound.Type != TypeOpenVPN {
		inbound.OpenVPN = nil
	} else if inbound.OpenVPN == nil {
		inbound.OpenVPN = &OpenVPNServer{RedirectGateway: true}
	}
	if inbound.Socks5 != nil {
		inbound.Socks5.normalize()
	}
	if inbound.OpenVPN != nil {
		inbound.OpenVPN.normalize()
	}
}

func (inbound *Inbound) complete(taken []netip.Prefix, base int, now time.Time) error {
	if socks := inbound.Socks5; socks != nil && socks.TLS && socks.Certificate == "" &&
		socks.Key == "" {
		material, err := GenerateMaterial(MaterialTLS, Material{}, now)
		if err != nil {
			return err
		}
		socks.Certificate, socks.Key = material.Certificate, material.Key
	}
	if inbound.OpenVPN != nil {
		return inbound.OpenVPN.complete(inbound.Slot(), base, taken, now)
	}
	return nil
}

func (inbound *Inbound) validate() error {
	if err := validateIdentifier(inbound.ID); err != nil {
		return err
	} else if err = validateAttempts(inbound.Attempts, inbound.ReconnectTimeout); err != nil {
		return err
	} else if err = inbound.Recovery.validate(); err != nil {
		return err
	} else if err = validateRange("port", inbound.Port, InboundPortFirst,
		InboundPortLast); err != nil {
		return err
	} else if !inbound.Direct() && inbound.Outbound != ViaRouter &&
		!identifierPattern.MatchString(inbound.Outbound) {
		return fmt.Errorf("invalid outbound '%v'", inbound.Outbound)
	} else if len(inbound.Users) > maxInboundUsers {
		return fmt.Errorf("invalid number of users (%v)", len(inbound.Users))
	}
	for index, user := range inbound.Users {
		if !loginPattern.MatchString(user.Name) {
			return fmt.Errorf("invalid user name '%v'", user.Name)
		} else if !secretPattern.MatchString(user.Password) {
			return fmt.Errorf("invalid password of user '%v'", user.Name)
		}
		for _, other := range inbound.Users[:index] {
			if other.Name == user.Name {
				return fmt.Errorf("duplicate user '%v'", user.Name)
			}
		}
	}
	switch inbound.Type {
	case TypeSocks5:
		return inbound.Socks5.validate()
	case TypeOpenVPN:
		return inbound.OpenVPN.validate()
	default:
		return fmt.Errorf("unexpected inbound type '%v'", inbound.Type)
	}
}

func (socks *Socks5Server) normalize() {
	if socks.Listen = strings.TrimSpace(socks.Listen); socks.Listen == "" {
		socks.Listen = ListenAny
	}
	socks.Certificate, socks.Key = normalizePEM(socks.Certificate), normalizePEM(socks.Key)
}

func (socks *Socks5Server) validate() error {
	if err := validateListen(socks.Listen); err != nil {
		return err
	} else if !socks.TLS {
		return nil
	} else if err = checkPair(socks.Certificate, socks.Key); err != nil {
		return fmt.Errorf("failed to check TLS certificate: %w", err)
	}
	return nil
}

func (server *OpenVPNServer) normalize() {
	server.Server = strings.TrimSpace(server.Server)
	if server.Listen = strings.TrimSpace(server.Listen); server.Listen == "" {
		server.Listen = ListenAny
	}
	if server.Protocol == "" {
		server.Protocol = ProtocolUDP
	}
	server.Network = strings.TrimSpace(server.Network)
	if prefix, err := netip.ParsePrefix(server.Network); err == nil {
		server.Network = prefix.Masked().String()
	}
	server.Routes, server.DNS = cleanList(server.Routes), cleanList(server.DNS)
	for index, route := range server.Routes {
		if prefix, err := netip.ParsePrefix(route); err == nil {
			server.Routes[index] = prefix.Masked().String()
		}
	}
	if server.MaxClients == 0 {
		server.MaxClients = defaultMaxClients
	}
	if server.KeepaliveInterval == 0 {
		server.KeepaliveInterval = defaultKeepaliveInterval
	}
	if server.KeepaliveTimeout == 0 {
		server.KeepaliveTimeout = defaultKeepaliveTimeout
	}
	if server.Renegotiation == 0 {
		server.Renegotiation = defaultRenegotiation
	}
	if server.MTU == 0 {
		server.MTU = defaultMTU
	}
	if server.Verbosity == 0 {
		server.Verbosity = defaultVerbosity
	}
	if server.Ciphers = strings.ToUpper(strings.TrimSpace(server.Ciphers)); server.Ciphers == "" {
		server.Ciphers = defaultCiphers
	}
	if server.Digest = strings.ToUpper(strings.TrimSpace(server.Digest)); server.Digest == "" {
		server.Digest = defaultDigest
	}
	if server.TLSVersion == "" {
		server.TLSVersion = defaultTLSVersion
	}
	server.OTPKey = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").
		Replace(server.OTPKey))
	server.CACertificate, server.CAKey = normalizePEM(server.CACertificate),
		normalizePEM(server.CAKey)
	server.Certificate, server.Key = normalizePEM(server.Certificate), normalizePEM(server.Key)
	server.ClientCertificate, server.ClientKey = normalizePEM(server.ClientCertificate),
		normalizePEM(server.ClientKey)
	server.TLSCrypt = normalizePEM(server.TLSCrypt)
}

func (server *OpenVPNServer) complete(slot int, base int, taken []netip.Prefix,
	now time.Time) error {
	for offset := 0; server.Network == "" && offset < networkLimit; offset++ {
		candidate := netip.MustParsePrefix(fmt.Sprintf(networkTemplate, base,
			(slot+offset)%networkLimit))
		if !slices.ContainsFunc(taken, candidate.Overlaps) {
			server.Network = candidate.String()
		}
	}
	source := Material{CACertificate: server.CACertificate, CAKey: server.CAKey}
	if server.CACertificate == "" && server.Certificate == "" && server.ClientCertificate == "" {
		material, err := GenerateMaterial(MaterialPKI, Material{}, now)
		if err != nil {
			return err
		}
		server.CACertificate, server.CAKey = material.CACertificate, material.CAKey
		server.Certificate, server.Key = material.Certificate, material.Key
		server.ClientCertificate, server.ClientKey = material.ClientCertificate, material.ClientKey
	}
	if server.Certificate == "" && server.Key == "" {
		material, err := GenerateMaterial(MaterialServer, source, now)
		if err != nil {
			return fmt.Errorf("failed to issue server certificate: %w", err)
		}
		server.Certificate, server.Key = material.Certificate, material.Key
	}
	if server.ClientCertificate == "" && server.ClientKey == "" {
		material, err := GenerateMaterial(MaterialClient, source, now)
		if err != nil {
			return fmt.Errorf("failed to issue client certificate: %w", err)
		}
		server.ClientCertificate, server.ClientKey = material.ClientCertificate, material.ClientKey
	}
	if server.TLSCrypt == "" {
		material, err := GenerateMaterial(MaterialTLSCrypt, Material{}, now)
		if err != nil {
			return err
		}
		server.TLSCrypt = material.TLSCrypt
	}
	if server.OTP && server.OTPKey == "" {
		material, err := GenerateMaterial(MaterialOTP, Material{}, now)
		if err != nil {
			return err
		}
		server.OTPKey = material.OTPKey
	}
	return nil
}

func (server *OpenVPNServer) validate() error {
	if _, err := netip.ParseAddr(server.Server); server.Server != "" && err != nil &&
		!hostPattern.MatchString(server.Server) {
		return fmt.Errorf("invalid server address '%v'", server.Server)
	} else if err = validateRange("public port", server.PublicPort, 0, 65535); err != nil {
		return err
	} else if err = validateListen(server.Listen); err != nil {
		return err
	} else if server.Protocol != ProtocolUDP && server.Protocol != ProtocolTCP {
		return fmt.Errorf("unexpected protocol '%v'", server.Protocol)
	} else if err = server.validateNetwork(); err != nil {
		return err
	} else if err = validateRange("number of clients", server.MaxClients, 1, 1000); err != nil {
		return err
	} else if err = validateRange("keepalive interval", server.KeepaliveInterval, 1,
		600); err != nil {
		return err
	} else if err = validateRange("keepalive timeout", server.KeepaliveTimeout,
		server.KeepaliveInterval+1, 3600); err != nil {
		return err
	} else if err = validateRange("renegotiation period", server.Renegotiation,
		minRenegotiation, 86400); err != nil {
		return err
	} else if err = validateRange("MTU", server.MTU, 1280, 1500); err != nil {
		return err
	} else if err = validateRange("verbosity", server.Verbosity, 1, 6); err != nil {
		return err
	} else if !cipherPattern.MatchString(server.Ciphers) {
		return fmt.Errorf("invalid data ciphers '%v'", server.Ciphers)
	} else if !slices.Contains(digests, server.Digest) {
		return fmt.Errorf("unexpected digest '%v'", server.Digest)
	} else if !slices.Contains(tlsVersions, server.TLSVersion) {
		return fmt.Errorf("unexpected TLS version '%v'", server.TLSVersion)
	} else if err = server.validateSecrets(); err != nil {
		return err
	}
	return nil
}

func (server *OpenVPNServer) validateNetwork() error {
	prefix, err := netip.ParsePrefix(server.Network)
	if err != nil {
		return fmt.Errorf("cannot parse %q as network: %w", server.Network, err)
	} else if !prefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 29 {
		return fmt.Errorf("invalid network '%v', expected IPv4 prefix /16../29", server.Network)
	} else if prefix.Overlaps(netip.MustParsePrefix(reservedNetwork)) {
		return fmt.Errorf("network '%v' overlaps reserved network '%v'", server.Network,
			reservedNetwork)
	} else if len(server.Routes) > maxPushedRoutes || len(server.DNS) > maxNameServers {
		return errors.New("too many routes or DNS servers")
	}
	for _, route := range server.Routes {
		if prefix, err := netip.ParsePrefix(route); err != nil || !prefix.Addr().Is4() {
			return fmt.Errorf("invalid route '%v', expected IPv4 prefix", route)
		}
	}
	for _, address := range server.DNS {
		if parsed, err := netip.ParseAddr(address); err != nil || !parsed.Is4() {
			return fmt.Errorf("invalid DNS server '%v', expected IPv4 address", address)
		}
	}
	return nil
}

func (server *OpenVPNServer) validateSecrets() error {
	padding := strings.Repeat("=", (8-len(server.OTPKey)%8)%8)
	if _, err := base32.StdEncoding.DecodeString(server.OTPKey + padding); err != nil {
		return fmt.Errorf("failed to decode OTP key: %w", err)
	} else if server.OTP && server.OTPKey == "" {
		return errors.New("OTP key does not exist")
	} else if _, err = parseCertificate(server.CACertificate); err != nil {
		return fmt.Errorf("failed to check CA certificate: %w", err)
	} else if err = checkPair(server.Certificate, server.Key); err != nil {
		return fmt.Errorf("failed to check server certificate: %w", err)
	} else if err = checkIssued(server.Certificate, server.CACertificate); err != nil {
		return fmt.Errorf("failed to check server certificate: %w", err)
	} else if err = checkPair(server.ClientCertificate, server.ClientKey); err != nil {
		return fmt.Errorf("failed to check client certificate: %w", err)
	} else if err = checkIssued(server.ClientCertificate, server.CACertificate); err != nil {
		return fmt.Errorf("failed to check client certificate: %w", err)
	} else if err = checkStaticKey(server.TLSCrypt); err != nil {
		return err
	} else if server.CAKey == "" {
		return nil
	} else if err = checkPair(server.CACertificate, server.CAKey); err != nil {
		return fmt.Errorf("failed to check CA: %w", err)
	}
	return nil
}
