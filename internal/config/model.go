package config

import (
	"encoding/base32"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"proximity/internal/rules"
	"proximity/internal/wgconf"
)

type Settings struct {
	MixedPort           int    `json:"mixed_port"`
	BindAddress         string `json:"bind_address"`
	DefaultTarget       string `json:"default_target"`
	LogLevel            string `json:"log_level"`
	IPv6                bool   `json:"ipv6"`
	AnonymousDashboard  bool   `json:"anonymous_dashboard"`
	ConnectivityChecks  bool   `json:"connectivity_checks"`
	ProbeTarget         string `json:"probe_target"`
	CheckInterval       int    `json:"check_interval"`
	Capture             bool   `json:"capture"`
	UnreachableLimit    int    `json:"unreachable_limit"`
	UnreachableLifetime int    `json:"unreachable_lifetime"`
	Autostart           bool   `json:"autostart"`
}

type User struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	PasswordHash string `json:"password_hash"`
	Epoch        int    `json:"epoch"`
}

type HostOptions struct {
	Mode             string `json:"mode"`
	Interface        string `json:"interface"`
	Gateway          string `json:"gateway"`
	Attempts         int    `json:"attempts"`
	ReconnectTimeout int    `json:"reconnect_timeout"`
}

type Socks5Options struct {
	Server         string `json:"server"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	TLS            bool   `json:"tls"`
	SkipCertVerify bool   `json:"skip_cert_verify"`
	UDP            bool   `json:"udp"`
}

type HTTPOptions struct {
	Server         string `json:"server"`
	Port           int    `json:"port"`
	Single         bool   `json:"single"`
	HTTPSServer    string `json:"https_server"`
	HTTPSPort      int    `json:"https_port"`
	FTPServer      string `json:"ftp_server"`
	FTPPort        int    `json:"ftp_port"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	TLS            bool   `json:"tls"`
	SkipCertVerify bool   `json:"skip_cert_verify"`
}

type OpenVPNOptions struct {
	Config      string `json:"config"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	OTPKey      string `json:"otp_key"`
	OTPMode     string `json:"otp_mode"`
	KeyPassword string `json:"key_password"`
	UseDNS      bool   `json:"use_dns"`
}

type AmneziaWGOptions struct {
	Config string `json:"config"`
	UseDNS bool   `json:"use_dns"`
}

type Monitoring struct {
	Enabled           bool   `json:"enabled"`
	Interval          int    `json:"interval"`
	Timeout           int    `json:"timeout"`
	Retries           int    `json:"retries"`
	ReconnectDelay    int    `json:"reconnect_delay"`
	ReconnectAttempts int    `json:"reconnect_attempts"`
	Target            string `json:"target"`
}

type Outbound struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Group      string            `json:"group"`
	Type       string            `json:"type"`
	Enabled    bool              `json:"enabled"`
	Statistics bool              `json:"statistics"`
	Slot       int               `json:"slot"`
	Host       *HostOptions      `json:"host,omitempty"`
	Socks5     *Socks5Options    `json:"socks5,omitempty"`
	HTTP       *HTTPOptions      `json:"http,omitempty"`
	OpenVPN    *OpenVPNOptions   `json:"openvpn,omitempty"`
	AmneziaWG  *AmneziaWGOptions `json:"amneziawg,omitempty"`
	Monitoring *Monitoring       `json:"monitoring,omitempty"`
}

type Fallback struct {
	ID          string   `json:"id"`
	Enabled     bool     `json:"enabled"`
	Outbounds   []string `json:"outbounds"`
	HealthCheck bool     `json:"health_check"`
	URL         string   `json:"url"`
	Interval    int      `json:"interval"`
	Legacy      []string `json:"endpoints,omitempty"`
}

type Route struct {
	Key       string   `json:"key"`
	ID        string   `json:"id"`
	Directive string   `json:"directive"`
	Target    string   `json:"target"`
	Params    []string `json:"params"`
	Enabled   bool     `json:"enabled"`
	Provided  string   `json:"provided,omitempty"`
}

type Config struct {
	Version   int        `json:"version"`
	Secret    string     `json:"secret"`
	Settings  Settings   `json:"settings"`
	Users     []User     `json:"users"`
	Outbounds []Outbound `json:"outbounds"`
	Fallbacks []Fallback `json:"fallbacks"`
	Routes    []Route    `json:"routes"`
	Inbounds  []Inbound  `json:"inbounds"`
	Legacy
}

type Legacy struct {
	Endpoints []Outbound `json:"endpoints,omitempty"`
	Filters   []Route    `json:"filters,omitempty"`
}

const (
	TypeHost      = "host"
	TypeSocks5    = "socks5"
	TypeHTTP      = "http"
	TypeOpenVPN   = "openvpn"
	TypeAmneziaWG = "amneziawg"
)

const (
	RoleAdministrator = "administrator"
	RoleOperator      = "operator"
	RoleUser          = "user"
	RoleAnonymous     = "anonymous"
)

const (
	TargetDirect = "DIRECT"
	TargetReject = "REJECT"
)

const (
	HostDefault   = "default"
	HostInterface = "interface"
)

const (
	OTPChallenge = "challenge"
	OTPAppend    = "append"
)

const currentVersion = 5
const inboundVersion = 4
const autostartVersion = 5
const maxSlot = 9999
const defaultAttempts = 3
const defaultReconnectTimeout = 10
const providedPrefix = "Routes of "
const defaultHealthURL = "https://www.gstatic.com/generate_204"
const defaultProbeTarget = "1.1.1.1:443"
const defaultUnreachableLimit = 100
const defaultUnreachableLifetime = 24 * 60 * 60
const maxUnreachableLimit = 10000
const minUnreachableLifetime = 60
const maxUnreachableLifetime = 365 * 24 * 60 * 60

var typeLabels = map[string]string{TypeHost: "HOST", TypeSocks5: "SOCKS5", TypeHTTP: "HTTP",
	TypeOpenVPN: "OpenVPN", TypeAmneziaWG: "AmneziaWG"}
var reservedTargets = []string{TargetDirect, TargetReject, "REJECT-DROP", "PASS", "COMPATIBLE",
	"GLOBAL"}
var logLevels = []string{"silent", "error", "warning", "info", "debug"}
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9 _.-]{0,46}[A-Za-z0-9_.-])?$`)
var userPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
var interfacePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,15}$`)

func Default() Config {
	return Config{Version: currentVersion, Settings: Settings{MixedPort: 7899, BindAddress: "*",
		DefaultTarget: TargetDirect, LogLevel: "warning", AnonymousDashboard: true,
		ConnectivityChecks: true, ProbeTarget: defaultProbeTarget, CheckInterval: 30,
		Capture: true, UnreachableLimit: defaultUnreachableLimit,
		UnreachableLifetime: defaultUnreachableLifetime, Autostart: true}, Users: []User{},
		Outbounds: []Outbound{}, Fallbacks: []Fallback{}, Routes: []Route{},
		Inbounds: []Inbound{}}
}

func TypeLabel(kind string) string {
	return typeLabels[kind]
}

func IsRole(role string) bool {
	return role == RoleAdministrator || role == RoleOperator || role == RoleUser
}

func validateIdentifier(id string) error {
	if !identifierPattern.MatchString(id) {
		return fmt.Errorf("invalid ID '%v'", id)
	}
	for _, reserved := range reservedTargets {
		if strings.EqualFold(reserved, id) {
			return fmt.Errorf("reserved ID '%v'", id)
		}
	}
	return nil
}

func validateServer(server string, port int) error {
	if _, err := netip.ParseAddr(server); err != nil && !hostPattern.MatchString(server) {
		return fmt.Errorf("invalid server address '%v'", server)
	} else if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port (%v)", port)
	}
	return nil
}

func validateRange(name string, value int, low int, high int) error {
	if value < low || value > high {
		return fmt.Errorf("invalid %v (%v), expected %v..%v", name, value, low, high)
	}
	return nil
}

func validateHostPort(target string) error {
	if address, err := url.Parse("tcp://" + target); err != nil {
		return fmt.Errorf("cannot parse %q as host:port: %w", target, err)
	} else if address.Hostname() == "" || address.Port() == "" || address.Path != "" {
		return fmt.Errorf("invalid host:port '%v'", target)
	}
	return nil
}

func (settings *Settings) normalize() {
	if settings.UnreachableLimit == 0 {
		settings.UnreachableLimit = defaultUnreachableLimit
	}
	if settings.UnreachableLifetime == 0 {
		settings.UnreachableLifetime = defaultUnreachableLifetime
	}
}

func (settings *Settings) validate(config *Config) error {
	found := false
	for _, level := range logLevels {
		found = found || level == settings.LogLevel
	}
	if !found {
		return fmt.Errorf("unexpected log level '%v'", settings.LogLevel)
	} else if err := validateRange("proxy port", settings.MixedPort, 1, 65535); err != nil {
		return err
	} else if err = validateRange("check interval", settings.CheckInterval, 5, 3600); err != nil {
		return err
	} else if err = validateHostPort(settings.ProbeTarget); err != nil {
		return fmt.Errorf("failed to check probe target: %w", err)
	} else if err = validateRange("number of unreachable routes", settings.UnreachableLimit, 1,
		maxUnreachableLimit); err != nil {
		return err
	} else if err = validateRange("lifetime of unreachable routes", settings.UnreachableLifetime,
		minUnreachableLifetime, maxUnreachableLifetime); err != nil {
		return err
	} else if !config.TargetExists(settings.DefaultTarget) {
		return fmt.Errorf("default target '%v' does not exist", settings.DefaultTarget)
	}
	if _, err := netip.ParseAddr(settings.BindAddress); err != nil && settings.BindAddress != "*" {
		return fmt.Errorf("invalid bind address '%v'", settings.BindAddress)
	}
	return nil
}

func (user *User) validate() error {
	if !userPattern.MatchString(user.Name) {
		return fmt.Errorf("invalid user name '%v'", user.Name)
	} else if !IsRole(user.Role) {
		return fmt.Errorf("unexpected role '%v'", user.Role)
	} else if user.PasswordHash == "" {
		return fmt.Errorf("password of '%v' does not exist", user.Name)
	}
	return nil
}

func (outbound *Outbound) normalize() {
	outbound.ID = strings.TrimSpace(outbound.ID)
	outbound.Name = strings.TrimSpace(outbound.Name)
	outbound.Group = strings.TrimSpace(outbound.Group)
	if outbound.Type != TypeHost {
		outbound.Host = nil
	} else if outbound.Host == nil {
		outbound.Host = &HostOptions{Mode: HostDefault}
	}
	if outbound.Host != nil && outbound.Host.Attempts == 0 {
		outbound.Host.Attempts = defaultAttempts
	}
	if outbound.Host != nil && outbound.Host.ReconnectTimeout == 0 {
		outbound.Host.ReconnectTimeout = defaultReconnectTimeout
	}
	if outbound.Type != TypeSocks5 {
		outbound.Socks5 = nil
	} else if outbound.Socks5 == nil {
		outbound.Socks5 = &Socks5Options{}
	}
	if outbound.Type != TypeHTTP {
		outbound.HTTP = nil
	} else if outbound.HTTP == nil {
		outbound.HTTP = &HTTPOptions{}
	}
	if outbound.Type != TypeOpenVPN {
		outbound.OpenVPN = nil
	} else if outbound.OpenVPN == nil {
		outbound.OpenVPN = &OpenVPNOptions{}
	}
	if outbound.Type != TypeAmneziaWG {
		outbound.AmneziaWG = nil
	} else if outbound.AmneziaWG == nil {
		outbound.AmneziaWG = &AmneziaWGOptions{}
	}
	if !outbound.IsTunnel() {
		outbound.Monitoring = nil
	} else if outbound.Monitoring == nil {
		outbound.Monitoring = &Monitoring{Interval: 30, Timeout: 5, Retries: 3, ReconnectDelay: 10,
			ReconnectAttempts: 3}
	}
	if outbound.Monitoring != nil && strings.TrimSpace(outbound.Monitoring.Target) == "" {
		outbound.Monitoring.Target = defaultProbeTarget
	}
	if outbound.OpenVPN != nil {
		outbound.OpenVPN.OTPKey = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").
			Replace(outbound.OpenVPN.OTPKey))
		if outbound.OpenVPN.OTPMode == "" {
			outbound.OpenVPN.OTPMode = OTPChallenge
		}
	}
}

func (outbound *Outbound) IsTunnel() bool {
	return outbound.Type == TypeOpenVPN || outbound.Type == TypeAmneziaWG
}

func (outbound *Outbound) IsSplit() bool {
	return outbound.HTTP != nil && !outbound.HTTP.Single &&
		(outbound.HTTP.HTTPSServer != "" || outbound.HTTP.FTPServer != "")
}

func (outbound *Outbound) validate() error {
	if err := validateIdentifier(outbound.ID); err != nil {
		return err
	} else if len(outbound.Name) > 96 || len(outbound.Group) > 48 {
		return errors.New("name or group is too long")
	} else if err = validateRange("slot", outbound.Slot, 1, maxSlot); err != nil {
		return err
	}
	switch outbound.Type {
	case TypeHost:
		return outbound.Host.validate()
	case TypeSocks5:
		return validateServer(outbound.Socks5.Server, outbound.Socks5.Port)
	case TypeHTTP:
		return outbound.HTTP.validate()
	case TypeOpenVPN:
		if err := outbound.OpenVPN.validate(); err != nil {
			return err
		}
		return outbound.Monitoring.validate()
	case TypeAmneziaWG:
		if _, err := wgconf.Parse(outbound.AmneziaWG.Config); err != nil {
			return fmt.Errorf("failed to check AmneziaWG configuration: %w", err)
		}
		return outbound.Monitoring.validate()
	default:
		return fmt.Errorf("unexpected outbound type '%v'", outbound.Type)
	}
}

func (host *HostOptions) validate() error {
	if err := validateRange("connection attempts", host.Attempts, 1, 100); err != nil {
		return err
	} else if err = validateRange("reconnection timeout", host.ReconnectTimeout, 1,
		3600); err != nil {
		return err
	} else if host.Mode == HostDefault {
		return nil
	} else if host.Mode != HostInterface {
		return fmt.Errorf("unexpected host mode '%v'", host.Mode)
	} else if !interfacePattern.MatchString(host.Interface) {
		return fmt.Errorf("invalid interface name '%v'", host.Interface)
	} else if host.Gateway == "" {
		return nil
	}
	if _, err := netip.ParseAddr(host.Gateway); err != nil {
		return fmt.Errorf("cannot parse %q as gateway: %w", host.Gateway, err)
	}
	return nil
}

func (proxy *HTTPOptions) validate() error {
	if err := validateServer(proxy.Server, proxy.Port); err != nil {
		return err
	} else if proxy.Single {
		return nil
	}
	if proxy.HTTPSServer != "" {
		if err := validateServer(proxy.HTTPSServer, proxy.HTTPSPort); err != nil {
			return fmt.Errorf("failed to check HTTPS proxy: %w", err)
		}
	}
	if proxy.FTPServer != "" {
		if err := validateServer(proxy.FTPServer, proxy.FTPPort); err != nil {
			return fmt.Errorf("failed to check FTP proxy: %w", err)
		}
	}
	return nil
}

func (options *OpenVPNOptions) validate() error {
	text := "\n" + strings.ReplaceAll(options.Config, "\r", "")
	if !strings.Contains(text, "\nremote ") && !strings.Contains(text, "\nremote\t") {
		return errors.New("OpenVPN configuration has no 'remote' directive")
	} else if options.OTPMode != OTPChallenge && options.OTPMode != OTPAppend {
		return fmt.Errorf("unexpected OTP mode '%v'", options.OTPMode)
	} else if options.OTPKey == "" {
		return nil
	}
	padding := strings.Repeat("=", (8-len(options.OTPKey)%8)%8)
	if _, err := base32.StdEncoding.DecodeString(options.OTPKey + padding); err != nil {
		return fmt.Errorf("failed to decode OTP key: %w", err)
	}
	return nil
}

func (monitoring *Monitoring) validate() error {
	if err := validateRange("monitoring interval", monitoring.Interval, 5, 3600); err != nil {
		return err
	} else if err = validateRange("monitoring timeout", monitoring.Timeout, 1, 60); err != nil {
		return err
	} else if err = validateRange("monitoring retries", monitoring.Retries, 1, 20); err != nil {
		return err
	} else if err = validateRange("reconnect delay", monitoring.ReconnectDelay, 1, 3600); err != nil {
		return err
	} else if err = validateRange("reconnect attempts", monitoring.ReconnectAttempts, 0,
		100); err != nil {
		return err
	} else if err = validateHostPort(monitoring.Target); err != nil {
		return fmt.Errorf("failed to check monitoring target: %w", err)
	}
	return nil
}

func (fallback *Fallback) normalize() {
	fallback.ID = strings.TrimSpace(fallback.ID)
	if fallback.URL = strings.TrimSpace(fallback.URL); fallback.URL == "" {
		fallback.URL = defaultHealthURL
	}
	if fallback.Interval == 0 {
		fallback.Interval = 60
	}
	if fallback.Outbounds == nil {
		fallback.Outbounds = []string{}
	}
}

func (fallback *Fallback) validate(config *Config) error {
	if err := validateIdentifier(fallback.ID); err != nil {
		return err
	} else if err = validateRange("check interval", fallback.Interval, 10, 3600); err != nil {
		return err
	} else if address, err := url.Parse(fallback.URL); err != nil {
		return fmt.Errorf("cannot parse %q as URL: %w", fallback.URL, err)
	} else if address.Host == "" || (address.Scheme != "http" && address.Scheme != "https") {
		return fmt.Errorf("invalid check URL '%v'", fallback.URL)
	}
	for index, id := range fallback.Outbounds {
		if config.findOutbound(id) < 0 {
			return fmt.Errorf("outbound '%v' does not exist", id)
		}
		for _, other := range fallback.Outbounds[:index] {
			if other == id {
				return fmt.Errorf("duplicate outbound '%v'", id)
			}
		}
	}
	return nil
}

func (route *Route) normalize() {
	route.ID = strings.TrimSpace(route.ID)
	params := []string{}
	for _, line := range route.Params {
		if line = strings.TrimSpace(line); line != "" {
			params = append(params, line)
		}
	}
	route.Params = params
}

func (route *Route) validate() error {
	if route.ID == "" || len(route.ID) > 64 {
		return fmt.Errorf("invalid route ID length (%v)", len(route.ID))
	} else if _, err := rules.Parse(route.Directive, route.Params); err != nil {
		return fmt.Errorf("failed to check rule parameters: %w", err)
	}
	return nil
}
