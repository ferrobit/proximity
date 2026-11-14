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
	MixedPort          int    `json:"mixed_port"`
	BindAddress        string `json:"bind_address"`
	DefaultTarget      string `json:"default_target"`
	LogLevel           string `json:"log_level"`
	IPv6               bool   `json:"ipv6"`
	AnonymousDashboard bool   `json:"anonymous_dashboard"`
	ProbeTarget        string `json:"probe_target"`
	CheckInterval      int    `json:"check_interval"`
}

type User struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	PasswordHash string `json:"password_hash"`
	Epoch        int    `json:"epoch"`
}

type HostOptions struct {
	Mode      string `json:"mode"`
	Interface string `json:"interface"`
	Gateway   string `json:"gateway"`
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

type Endpoint struct {
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
	ID        string   `json:"id"`
	Enabled   bool     `json:"enabled"`
	Endpoints []string `json:"endpoints"`
	URL       string   `json:"url"`
	Interval  int      `json:"interval"`
}

type Filter struct {
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
	Endpoints []Endpoint `json:"endpoints"`
	Fallbacks []Fallback `json:"fallbacks"`
	Filters   []Filter   `json:"filters"`
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

const currentVersion = 1
const maxSlot = 9999
const defaultHealthURL = "https://www.gstatic.com/generate_204"
const defaultProbeTarget = "1.1.1.1:443"

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
	return Config{Version: currentVersion, Settings: Settings{MixedPort: 7890, BindAddress: "*",
		DefaultTarget: TargetDirect, LogLevel: "warning", AnonymousDashboard: true,
		ProbeTarget: defaultProbeTarget, CheckInterval: 30}, Users: []User{},
		Endpoints: []Endpoint{}, Fallbacks: []Fallback{}, Filters: []Filter{}}
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

func (endpoint *Endpoint) normalize() {
	endpoint.ID = strings.TrimSpace(endpoint.ID)
	endpoint.Name = strings.TrimSpace(endpoint.Name)
	endpoint.Group = strings.TrimSpace(endpoint.Group)
	if endpoint.Type != TypeHost {
		endpoint.Host = nil
	} else if endpoint.Host == nil {
		endpoint.Host = &HostOptions{Mode: HostDefault}
	}
	if endpoint.Type != TypeSocks5 {
		endpoint.Socks5 = nil
	} else if endpoint.Socks5 == nil {
		endpoint.Socks5 = &Socks5Options{}
	}
	if endpoint.Type != TypeHTTP {
		endpoint.HTTP = nil
	} else if endpoint.HTTP == nil {
		endpoint.HTTP = &HTTPOptions{}
	}
	if endpoint.Type != TypeOpenVPN {
		endpoint.OpenVPN = nil
	} else if endpoint.OpenVPN == nil {
		endpoint.OpenVPN = &OpenVPNOptions{}
	}
	if endpoint.Type != TypeAmneziaWG {
		endpoint.AmneziaWG = nil
	} else if endpoint.AmneziaWG == nil {
		endpoint.AmneziaWG = &AmneziaWGOptions{}
	}
	if !endpoint.IsTunnel() {
		endpoint.Monitoring = nil
	} else if endpoint.Monitoring == nil {
		endpoint.Monitoring = &Monitoring{Interval: 30, Timeout: 5, Retries: 3, ReconnectDelay: 10,
			ReconnectAttempts: 3}
	}
	if endpoint.Monitoring != nil && strings.TrimSpace(endpoint.Monitoring.Target) == "" {
		endpoint.Monitoring.Target = defaultProbeTarget
	}
	if endpoint.OpenVPN != nil {
		endpoint.OpenVPN.OTPKey = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").
			Replace(endpoint.OpenVPN.OTPKey))
		if endpoint.OpenVPN.OTPMode == "" {
			endpoint.OpenVPN.OTPMode = OTPChallenge
		}
	}
}

func (endpoint *Endpoint) IsTunnel() bool {
	return endpoint.Type == TypeOpenVPN || endpoint.Type == TypeAmneziaWG
}

func (endpoint *Endpoint) IsSplit() bool {
	return endpoint.HTTP != nil && !endpoint.HTTP.Single &&
		(endpoint.HTTP.HTTPSServer != "" || endpoint.HTTP.FTPServer != "")
}

func (endpoint *Endpoint) validate() error {
	if err := validateIdentifier(endpoint.ID); err != nil {
		return err
	} else if len(endpoint.Name) > 96 || len(endpoint.Group) > 48 {
		return errors.New("name or group is too long")
	} else if err = validateRange("slot", endpoint.Slot, 1, maxSlot); err != nil {
		return err
	}
	switch endpoint.Type {
	case TypeHost:
		return endpoint.Host.validate()
	case TypeSocks5:
		return validateServer(endpoint.Socks5.Server, endpoint.Socks5.Port)
	case TypeHTTP:
		return endpoint.HTTP.validate()
	case TypeOpenVPN:
		if err := endpoint.OpenVPN.validate(); err != nil {
			return err
		}
		return endpoint.Monitoring.validate()
	case TypeAmneziaWG:
		if _, err := wgconf.Parse(endpoint.AmneziaWG.Config); err != nil {
			return fmt.Errorf("failed to check AmneziaWG configuration: %w", err)
		}
		return endpoint.Monitoring.validate()
	default:
		return fmt.Errorf("unexpected endpoint type '%v'", endpoint.Type)
	}
}

func (host *HostOptions) validate() error {
	if host.Mode == HostDefault {
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
	if fallback.Endpoints == nil {
		fallback.Endpoints = []string{}
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
	for index, id := range fallback.Endpoints {
		if config.findEndpoint(id) < 0 {
			return fmt.Errorf("endpoint '%v' does not exist", id)
		}
		for _, other := range fallback.Endpoints[:index] {
			if other == id {
				return fmt.Errorf("duplicate endpoint '%v'", id)
			}
		}
	}
	return nil
}

func (filter *Filter) normalize() {
	filter.ID = strings.TrimSpace(filter.ID)
	params := []string{}
	for _, line := range filter.Params {
		if line = strings.TrimSpace(line); line != "" {
			params = append(params, line)
		}
	}
	filter.Params = params
}

func (filter *Filter) validate() error {
	if len(filter.ID) > 64 {
		return errors.New("filter ID is too long")
	} else if _, err := rules.Parse(filter.Directive, filter.Params); err != nil {
		return fmt.Errorf("failed to check rule parameters: %w", err)
	}
	return nil
}
