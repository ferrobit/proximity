package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const sampleTunnel = "[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.1.2/32\n" +
	"[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = vpn.example.com:51820\n"
const sampleOpenVPN = "client\ndev tun\nremote vpn.example.com 1194 udp\n"

func makeConfig(t *testing.T) *Config {
	result := Default()
	for _, endpoint := range []Endpoint{{Type: TypeHost, Enabled: true},
		{Type: TypeSocks5, Enabled: true, Socks5: &Socks5Options{Server: "10.0.0.1", Port: 1080}},
		{Type: TypeHTTP, Enabled: true, HTTP: &HTTPOptions{Server: "proxy.example.com", Port: 3128,
			HTTPSServer: "secure.example.com", HTTPSPort: 3129}},
		{Type: TypeOpenVPN, Enabled: true, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN}},
		{Type: TypeAmneziaWG, AmneziaWG: &AmneziaWGOptions{Config: sampleTunnel}}} {
		_, err := result.AddEndpoint(endpoint)
		require.NoError(t, err)
	}
	require.NoError(t, result.Validate())
	return &result
}

func TestAddEndpoint_Defaults(t *testing.T) {
	config := makeConfig(t)
	ids, slots := []string{}, []int{}
	for _, endpoint := range config.Endpoints {
		ids, slots = append(ids, endpoint.ID), append(slots, endpoint.Slot)
	}
	require.Equal(t, []string{"HOST 1", "SOCKS5 1", "HTTP 1", "OpenVPN 1", "AmneziaWG 1"}, ids)
	require.Equal(t, []int{1, 2, 3, 4, 5}, slots)
	require.Equal(t, HostDefault, config.Endpoints[0].Host.Mode)
	require.Nil(t, config.Endpoints[0].Monitoring)
	require.Equal(t, Monitoring{Interval: 30, Timeout: 5, Retries: 3, ReconnectDelay: 10,
		ReconnectAttempts: 3, Target: defaultProbeTarget}, *config.Endpoints[3].Monitoring)
	require.Equal(t, OTPChallenge, config.Endpoints[3].OpenVPN.OTPMode)
	require.True(t, config.Endpoints[2].IsSplit())
	require.False(t, config.Endpoints[1].IsSplit())
	require.True(t, config.Endpoints[4].IsTunnel())
	{
		require.NoError(t, config.RemoveEndpoint("SOCKS5 1"))
		endpoint, err := config.AddEndpoint(Endpoint{Type: TypeHost, ID: " Office uplink ",
			Name: " main ", Socks5: &Socks5Options{}})
		require.NoError(t, err)
		require.Equal(t, "Office uplink", endpoint.ID)
		require.Equal(t, "main", endpoint.Name)
		require.Equal(t, 2, endpoint.Slot)
		require.Nil(t, endpoint.Socks5)
		require.Equal(t, "HOST 2", config.NextID(TypeLabel(TypeHost)))
	}
}

func TestAddEndpoint_Invalid(t *testing.T) {
	config := makeConfig(t)
	for _, item := range []struct {
		endpoint Endpoint
		message  string
	}{
		{Endpoint{Type: "wireguard"}, "unexpected endpoint type 'wireguard'"},
		{Endpoint{Type: TypeHost, ID: "HOST 1"}, "ID 'HOST 1' already exists"},
		{Endpoint{Type: TypeHost, ID: "a,b"}, "invalid ID 'a,b'"},
		{Endpoint{Type: TypeHost, ID: "direct"}, "reserved ID 'direct'"},
		{Endpoint{Type: TypeHost, Group: string(make([]byte, 49))}, "name or group is too long"},
		{Endpoint{Type: TypeHost, Host: &HostOptions{Mode: "auto"}}, "unexpected host mode 'auto'"},
		{Endpoint{Type: TypeHost, Host: &HostOptions{Mode: HostInterface, Interface: "bad name"}},
			"invalid interface name 'bad name'"},
		{Endpoint{Type: TypeHost, Host: &HostOptions{Mode: HostInterface, Interface: "eth0",
			Gateway: "gw"}}, "cannot parse \"gw\" as gateway"},
		{Endpoint{Type: TypeSocks5, Socks5: &Socks5Options{Server: "bad host", Port: 1}},
			"invalid server address 'bad host'"},
		{Endpoint{Type: TypeSocks5, Socks5: &Socks5Options{Server: "::1", Port: 0}},
			"invalid port (0)"},
		{Endpoint{Type: TypeHTTP, HTTP: &HTTPOptions{Server: "a", Port: 70000}},
			"invalid port (70000)"},
		{Endpoint{Type: TypeHTTP, HTTP: &HTTPOptions{Server: "a", Port: 1, HTTPSServer: "b"}},
			"failed to check HTTPS proxy"},
		{Endpoint{Type: TypeHTTP, HTTP: &HTTPOptions{Server: "a", Port: 1, FTPServer: "b"}},
			"failed to check FTP proxy"},
		{Endpoint{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: "client\n"}},
			"OpenVPN configuration has no 'remote' directive"},
		{Endpoint{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN,
			OTPMode: "sms"}}, "unexpected OTP mode 'sms'"},
		{Endpoint{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN,
			OTPKey: "not base32 1"}}, "failed to decode OTP key"},
		{Endpoint{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &Monitoring{Interval: 1}}, "invalid monitoring interval (1), expected 5..3600"},
		{Endpoint{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &Monitoring{Interval: 5}}, "invalid monitoring timeout (0)"},
		{Endpoint{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &Monitoring{Interval: 5, Timeout: 1}}, "invalid monitoring retries (0)"},
		{Endpoint{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &Monitoring{Interval: 5, Timeout: 1, Retries: 1}}, "invalid reconnect delay"},
		{Endpoint{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &Monitoring{Interval: 5, Timeout: 1, Retries: 1, ReconnectDelay: 1,
				ReconnectAttempts: 101}}, "invalid reconnect attempts (101)"},
		{Endpoint{Type: TypeAmneziaWG, AmneziaWG: &AmneziaWGOptions{Config: sampleTunnel},
			Monitoring: &Monitoring{Interval: 5, Timeout: 1, Retries: 1, ReconnectDelay: 1,
				Target: "nohost"}}, "failed to check monitoring target"},
		{Endpoint{Type: TypeAmneziaWG}, "failed to check AmneziaWG configuration"},
	} {
		_, err := config.AddEndpoint(item.endpoint)
		require.ErrorContains(t, err, item.message)
	}
	require.Len(t, config.Endpoints, 5)
}

func TestAddFallback_Members(t *testing.T) {
	config := makeConfig(t)
	{
		fallback, err := config.AddFallback(Fallback{Enabled: true,
			Endpoints: []string{"SOCKS5 1", "HOST 1"}})
		require.NoError(t, err)
		require.Equal(t, Fallback{ID: "Fallback 1", Enabled: true, URL: defaultHealthURL,
			Interval: 60, Endpoints: []string{"SOCKS5 1", "HOST 1"}}, fallback)
		require.True(t, config.TargetExists("Fallback 1"))
	}
	for _, item := range []struct {
		fallback Fallback
		message  string
	}{
		{Fallback{ID: "Fallback 1"}, "ID 'Fallback 1' already exists"},
		{Fallback{ID: "HOST 1"}, "ID 'HOST 1' already exists"},
		{Fallback{ID: "REJECT"}, "ID 'REJECT' already exists"},
		{Fallback{ID: "x", Endpoints: []string{"missing"}}, "endpoint 'missing' does not exist"},
		{Fallback{ID: "x", Endpoints: []string{"HOST 1", "HOST 1"}}, "duplicate endpoint 'HOST 1'"},
		{Fallback{ID: "x", Interval: 5}, "invalid check interval (5)"},
		{Fallback{ID: "x", URL: "ftp://example.com"}, "invalid check URL 'ftp://example.com'"},
		{Fallback{ID: "x", URL: "http://%zz"}, "as URL"},
	} {
		_, err := config.AddFallback(item.fallback)
		require.ErrorContains(t, err, item.message)
	}
	{
		require.NoError(t, config.UpdateFallback("Fallback 1", Fallback{
			Endpoints: []string{"HOST 1"}}))
		require.Equal(t, "Fallback 1", config.Fallbacks[0].ID)
		require.False(t, config.Fallbacks[0].Enabled)
		require.False(t, config.Fallbacks[0].HealthCheck)
		require.ErrorContains(t, config.UpdateFallback("missing", Fallback{}), "does not exist")
		require.ErrorContains(t, config.UpdateFallback("Fallback 1", Fallback{Interval: 1}),
			"invalid check interval")
		require.ErrorContains(t, config.UpdateFallback("Fallback 1", Fallback{ID: "HOST 1"}),
			"ID 'HOST 1' already exists")
		require.ErrorContains(t, config.UpdateFallback("Fallback 1", Fallback{ID: "bad,id"}),
			"invalid ID 'bad,id'")
	}
	{
		config.Settings.DefaultTarget = "Fallback 1"
		_, err := config.AddFallback(Fallback{})
		require.NoError(t, err)
		require.NoError(t, config.MoveFallback("Fallback 2", -1))
		require.Equal(t, "Fallback 2", config.Fallbacks[0].ID)
		require.NoError(t, config.RemoveFallback("Fallback 1"))
		require.Equal(t, TargetDirect, config.Settings.DefaultTarget)
		require.ErrorContains(t, config.RemoveFallback("Fallback 1"), "does not exist")
		require.NoError(t, config.Validate())
	}
}

func TestAddFilter_Identifiers(t *testing.T) {
	config := makeConfig(t)
	first, err := config.AddFilter(Filter{Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"a.test"}})
	require.NoError(t, err)
	require.Equal(t, "Filter 1", first.ID)
	second, err := config.AddFilter(Filter{ID: " Filter 3 ", Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"b.test"}})
	require.NoError(t, err)
	require.Equal(t, "Filter 3", second.ID)
	third, err := config.AddFilter(Filter{Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"c.test"}})
	require.NoError(t, err)
	require.Equal(t, "Filter 2", third.ID)
	require.Equal(t, "Filter 4", config.NextFilterID())
	_, err = config.AddFilter(Filter{ID: "Filter 1", Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"d.test"}})
	require.EqualError(t, err, "filter ID 'Filter 1' already exists")
	{
		second.ID = "Filter 1"
		require.EqualError(t, config.UpdateFilter(second.Key, second),
			"filter ID 'Filter 1' already exists")
		second.ID = ""
		require.NoError(t, config.UpdateFilter(second.Key, second))
		require.Equal(t, "Filter 3", config.Filters[1].ID)
		second.ID = "Work sites"
		require.NoError(t, config.UpdateFilter(second.Key, second))
		require.Equal(t, "Work sites", config.Filters[1].ID)
		require.NoError(t, config.Validate())
	}
	{
		config.Filters[2].ID = "Work sites"
		require.EqualError(t, config.Validate(), "duplicate filter ID 'Work sites'")
		config.Filters[2].ID = ""
		require.ErrorContains(t, config.Validate(), "invalid filter ID length (0)")
		_, err := config.SetRoutes("OpenVPN 1", []string{"10.0.0.0/8"})
		require.NoError(t, err)
		config.Filters[2].ID = "Routes of AmneziaWG 1"
		_, err = config.SetRoutes("AmneziaWG 1", []string{"10.1.0.0/16"})
		require.NoError(t, err)
		require.Equal(t, "Routes of OpenVPN 1", config.Filters[3].ID)
		require.Equal(t, "Filter 2", config.Filters[4].ID)
		require.Equal(t, "AmneziaWG 1", config.Filters[4].Provided)
	}
}

func TestAddFilter_Normalizes(t *testing.T) {
	config := makeConfig(t)
	{
		filter, err := config.AddFilter(Filter{Key: "ignored", ID: " corp ", Provided: "x",
			Directive: "IP-CIDR", Target: "SOCKS5 1", Enabled: true,
			Params: []string{" 10.1.2.3 ", "", "8.8.8.8:53"}})
		require.NoError(t, err)
		require.Len(t, filter.Key, 12)
		require.Equal(t, Filter{Key: filter.Key, ID: "corp", Directive: "IP-CIDR",
			Target: "SOCKS5 1", Enabled: true, Params: []string{"10.1.2.3/32", "8.8.8.8/32:53"}},
			filter)
	}
	for _, item := range []struct {
		filter  Filter
		message string
	}{
		{Filter{Directive: "DOMAIN", Target: "missing", Params: []string{"a.test"}},
			"target 'missing' does not exist"},
		{Filter{Directive: "DOMAIN", Target: "DIRECT", Params: []string{"a b"}},
			"failed to check rule parameters"},
		{Filter{Directive: "DOMAIN", Target: "DIRECT"}, "rule parameters do not exist"},
		{Filter{Directive: "NOPE", Target: "DIRECT", Params: []string{"x"}}, "unexpected directive"},
		{Filter{Directive: "DOMAIN", Target: "DIRECT", Params: []string{"a.test"},
			ID: string(make([]byte, 65))}, "filter ID is too long"},
	} {
		_, err := config.AddFilter(item.filter)
		require.ErrorContains(t, err, item.message)
	}
	{
		key := config.Filters[0].Key
		require.NoError(t, config.UpdateFilter(key, Filter{Directive: "DOMAIN-SUFFIX",
			Target: "REJECT", Params: []string{"*.Example.com"}}))
		require.Equal(t, []string{"example.com"}, config.Filters[0].Params)
		require.Equal(t, key, config.Filters[0].Key)
		require.ErrorContains(t, config.UpdateFilter(key, Filter{}), "target '' does not exist")
		require.ErrorContains(t, config.UpdateFilter("missing", Filter{}), "does not exist")
		_, err := config.AddFilter(Filter{Directive: "GEOIP", Target: "DIRECT", Params: []string{"ru"}})
		require.NoError(t, err)
		require.NoError(t, config.MoveFilter(key, 1))
		require.Equal(t, key, config.Filters[1].Key)
		require.NoError(t, config.MoveFilter(key, 1))
		require.Equal(t, key, config.Filters[1].Key)
		require.ErrorContains(t, config.MoveFilter(key, 2), "invalid move direction (2)")
		require.ErrorContains(t, config.MoveFilter("missing", 1), "item does not exist")
		require.NoError(t, config.RemoveFilter(key))
		require.ErrorContains(t, config.RemoveFilter(key), "does not exist")
		require.NoError(t, config.Validate())
	}
}

func TestClone_Independent(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddFallback(Fallback{Endpoints: []string{"HOST 1"}})
	require.NoError(t, err)
	_, err = config.AddFilter(Filter{Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"a.test"}})
	require.NoError(t, err)
	clone := config.Clone()
	require.Equal(t, *config, clone)
	clone.Endpoints[1].Socks5.Port, clone.Endpoints[2].HTTP.Port = 1, 1
	clone.Endpoints[0].Host.Mode, clone.Endpoints[3].OpenVPN.Config = "x", "x"
	clone.Endpoints[4].AmneziaWG.Config, clone.Endpoints[4].Monitoring.Interval = "x", 1
	clone.Fallbacks[0].Endpoints[0], clone.Filters[0].Params[0] = "x", "x"
	require.Equal(t, 1080, config.Endpoints[1].Socks5.Port)
	require.Equal(t, 3128, config.Endpoints[2].HTTP.Port)
	require.Equal(t, HostDefault, config.Endpoints[0].Host.Mode)
	require.Equal(t, sampleOpenVPN, config.Endpoints[3].OpenVPN.Config)
	require.Equal(t, sampleTunnel, config.Endpoints[4].AmneziaWG.Config)
	require.Equal(t, 30, config.Endpoints[4].Monitoring.Interval)
	require.Equal(t, "HOST 1", config.Fallbacks[0].Endpoints[0])
	require.Equal(t, "a.test", config.Filters[0].Params[0])
}

func TestImport_Backup(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddFallback(Fallback{Enabled: true, Endpoints: []string{"HOST 1"}})
	require.NoError(t, err)
	_, err = config.AddFilter(Filter{Directive: "DOMAIN", Target: "Fallback 1", Enabled: true,
		Params: []string{"a.test"}})
	require.NoError(t, err)
	config.Settings.DefaultTarget, config.Settings.MixedPort = "Fallback 1", 7000
	require.NoError(t, config.AddUser(User{Name: "admin", Role: RoleAdministrator,
		PasswordHash: "h"}))
	backup := config.Export(time.Unix(1700000000, 0))
	require.Equal(t, BackupFormat, backup.Format)
	require.Equal(t, currentVersion, backup.Version)
	require.Equal(t, "2023-11-14T22:13:20Z", backup.Created)
	require.Len(t, backup.Endpoints, 5)
	data, err := json.Marshal(backup)
	require.NoError(t, err)
	require.NotContains(t, string(data), "password_hash")
	{
		target := Default()
		target.Users, target.Secret = config.Users, "secret"
		target.Settings.LogLevel = "debug"
		require.NoError(t, target.Import(backup, false))
		require.NoError(t, target.Validate())
		require.Equal(t, config.Endpoints, target.Endpoints)
		require.Equal(t, config.Fallbacks, target.Fallbacks)
		require.Equal(t, config.Filters, target.Filters)
		require.Equal(t, 7899, target.Settings.MixedPort)
		require.Equal(t, "debug", target.Settings.LogLevel)
		require.Equal(t, "Fallback 1", target.Settings.DefaultTarget)
		require.Equal(t, "secret", target.Secret)
		require.Len(t, target.Users, 1)
		backup.Endpoints[0].Name = "changed"
		require.Empty(t, target.Endpoints[0].Name)
	}
	{
		target := Default()
		require.NoError(t, target.Import(backup, true))
		require.NoError(t, target.Validate())
		require.Equal(t, 7000, target.Settings.MixedPort)
	}
	{
		legacy := Backup{Format: BackupFormat, Version: 1, Settings: Default().Settings,
			Endpoints: backup.Endpoints[:1], Fallbacks: []Fallback{{ID: "Old", Enabled: true,
				Endpoints: []string{"HOST 1"}}}, Filters: []Filter{{Directive: "DOMAIN",
				Target: "Old", Params: []string{"a.test"}}, {Directive: "DOMAIN", Target: "Old",
				Params: []string{"b.test"}}}}
		legacy.Settings.ConnectivityChecks, legacy.Settings.Capture = false, false
		target := Default()
		require.NoError(t, target.Import(legacy, true))
		require.NoError(t, target.Validate())
		require.True(t, target.Fallbacks[0].HealthCheck)
		require.True(t, target.Settings.ConnectivityChecks)
		require.True(t, target.Settings.Capture)
		require.Equal(t, "Filter 1", target.Filters[0].ID)
		require.Equal(t, "Filter 2", target.Filters[1].ID)
		require.Len(t, target.Filters[0].Key, 12)
		require.NotEqual(t, target.Filters[0].Key, target.Filters[1].Key)
	}
	{
		target := Default()
		require.NoError(t, target.Import(Backup{Format: BackupFormat, Version: currentVersion,
			Settings: Default().Settings}, true))
		require.NotNil(t, target.Endpoints)
		require.NotNil(t, target.Fallbacks)
		require.NotNil(t, target.Filters)
		require.EqualError(t, target.Import(Backup{Format: "other", Version: 1}, true),
			"unexpected backup format 'other'")
		require.EqualError(t, target.Import(Backup{Format: BackupFormat, Version: 99}, true),
			"unexpected backup version (99)")
		require.EqualError(t, target.Import(Backup{Format: BackupFormat}, true),
			"unexpected backup version (0)")
	}
}

func TestMoveEndpoint_Order(t *testing.T) {
	config := makeConfig(t)
	require.NoError(t, config.MoveEndpoint("HOST 1", -1))
	require.Equal(t, "HOST 1", config.Endpoints[0].ID)
	require.NoError(t, config.MoveEndpoint("HOST 1", 1))
	require.Equal(t, "SOCKS5 1", config.Endpoints[0].ID)
	require.Equal(t, "HOST 1", config.Endpoints[1].ID)
	require.ErrorContains(t, config.MoveEndpoint("missing", 1), "item does not exist")
}

func TestOpen_CreatesAndReloads(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested")
	store, err := Open(directory)
	require.NoError(t, err)
	secret := store.Snapshot().Secret
	require.Len(t, secret, 44)
	calls := 0
	store.Subscribe(func() {
		calls++
	})
	require.NoError(t, store.Update(func(config *Config) error {
		_, err := config.AddEndpoint(Endpoint{Type: TypeHost, Enabled: true})
		return err
	}))
	require.Equal(t, 1, calls)
	{
		failure := errors.New("rejected")
		require.ErrorIs(t, store.Update(func(config *Config) error {
			config.Endpoints = nil
			return failure
		}), failure)
		require.ErrorContains(t, store.Update(func(config *Config) error {
			config.Settings.MixedPort = 0
			return nil
		}), "invalid proxy port (0)")
		require.Equal(t, 1, calls)
		require.Len(t, store.Snapshot().Endpoints, 1)
		require.Equal(t, 7899, store.Snapshot().Settings.MixedPort)
	}
	{
		reloaded, err := Open(directory)
		require.NoError(t, err)
		require.Equal(t, store.Snapshot(), reloaded.Snapshot())
		information, err := os.Stat(filepath.Join(directory, fileName))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), information.Mode().Perm())
	}
	{
		require.NoError(t, os.WriteFile(filepath.Join(directory, fileName), []byte("{"), 0o600))
		_, err := Open(directory)
		require.ErrorContains(t, err, "config.Open failed: failed to decode file")
		require.NoError(t, os.WriteFile(filepath.Join(directory, fileName),
			[]byte(`{"settings":{"log_level":"loud"}}`), 0o600))
		_, err = Open(directory)
		require.ErrorContains(t, err, "unexpected log level 'loud'")
		_, err = Open(filepath.Join(directory, fileName, "sub"))
		require.ErrorContains(t, err, "failed to create directory")
	}
}

func TestOpen_Migrates(t *testing.T) {
	directory := t.TempDir()
	legacy := `{"version":1,"secret":"s","settings":{"mixed_port":7890,"bind_address":"*",` +
		`"default_target":"DIRECT","log_level":"warning","probe_target":"1.1.1.1:443",` +
		`"check_interval":30},"users":[],"endpoints":[{"id":"HOST 1","type":"host","slot":1,` +
		`"enabled":true,"host":{"mode":"default"}}],"fallbacks":[{"id":"Fallback 1",` +
		`"enabled":true,"endpoints":["HOST 1"],"url":"https://example.com/","interval":60}],` +
		`"filters":[{"key":"aaaaaaaaaaaa","id":"","directive":"DOMAIN","target":"DIRECT",` +
		`"params":["a.test"],"enabled":true},{"key":"bbbbbbbbbbbb","id":"same",` +
		`"directive":"DOMAIN","target":"DIRECT","params":["b.test"]},{"key":"cccccccccccc",` +
		`"id":"same","directive":"DOMAIN","target":"DIRECT","params":["c.test"]}]}`
	require.NoError(t, os.WriteFile(filepath.Join(directory, fileName), []byte(legacy), 0o600))
	store, err := Open(directory)
	require.NoError(t, err)
	snapshot := store.Snapshot()
	require.Equal(t, currentVersion, snapshot.Version)
	require.Equal(t, 7890, snapshot.Settings.MixedPort)
	require.True(t, snapshot.Settings.ConnectivityChecks)
	require.True(t, snapshot.Settings.Capture)
	require.True(t, snapshot.Fallbacks[0].HealthCheck)
	require.Equal(t, &HostOptions{Mode: HostDefault, Attempts: 3, ReconnectTimeout: 10},
		snapshot.Endpoints[0].Host)
	require.Equal(t, "Filter 1", snapshot.Filters[0].ID)
	require.Equal(t, "same", snapshot.Filters[1].ID)
	require.Equal(t, "Filter 2", snapshot.Filters[2].ID)
}

func TestRemoveEndpoint_Cascade(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddFallback(Fallback{Endpoints: []string{"SOCKS5 1", "OpenVPN 1"}})
	require.NoError(t, err)
	changed, err := config.SetRoutes("OpenVPN 1", []string{"10.0.0.0/8"})
	require.NoError(t, err)
	require.True(t, changed)
	config.Settings.DefaultTarget = "OpenVPN 1"
	require.NoError(t, config.RemoveEndpoint("OpenVPN 1"))
	require.Equal(t, []string{"SOCKS5 1"}, config.Fallbacks[0].Endpoints)
	require.Empty(t, config.Filters)
	require.Equal(t, TargetDirect, config.Settings.DefaultTarget)
	require.ErrorContains(t, config.RemoveEndpoint("OpenVPN 1"), "does not exist")
	require.NoError(t, config.Validate())
}

func TestSetRoutes_Lifecycle(t *testing.T) {
	config := makeConfig(t)
	{
		changed, err := config.SetRoutes("OpenVPN 1", nil)
		require.NoError(t, err)
		require.False(t, changed)
	}
	{
		changed, err := config.SetRoutes("OpenVPN 1", []string{"10.0.0.0/8", "172.16.0.0/12"})
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, Filter{Key: config.Filters[0].Key, ID: "Routes of OpenVPN 1",
			Directive: "IP-CIDR", Target: "OpenVPN 1", Provided: "OpenVPN 1",
			Params: []string{"10.0.0.0/8", "172.16.0.0/12"}}, config.Filters[0])
		require.NoError(t, config.Validate())
	}
	{
		key := config.Filters[0].Key
		require.NoError(t, config.UpdateFilter(key, Filter{Enabled: true, Target: "REJECT",
			Params: []string{"1.1.1.1"}}))
		require.True(t, config.Filters[0].Enabled)
		require.Equal(t, "OpenVPN 1", config.Filters[0].Target)
		require.Len(t, config.Filters[0].Params, 2)
		require.ErrorContains(t, config.RemoveFilter(key), "provided filter is read-only")
	}
	{
		changed, err := config.SetRoutes("OpenVPN 1", []string{"10.0.0.0/8", "172.16.0.0/12"})
		require.NoError(t, err)
		require.False(t, changed)
		changed, err = config.SetRoutes("OpenVPN 1", []string{"10.0.0.0/8"})
		require.NoError(t, err)
		require.True(t, changed)
		require.True(t, config.Filters[0].Enabled)
		require.Equal(t, []string{"10.0.0.0/8"}, config.Filters[0].Params)
		changed, err = config.SetRoutes("OpenVPN 1", []string{})
		require.NoError(t, err)
		require.True(t, changed)
		require.Empty(t, config.Filters)
	}
}

func TestUpdateEndpoint_Identity(t *testing.T) {
	config := makeConfig(t)
	require.NoError(t, config.UpdateEndpoint("SOCKS5 1", Endpoint{Type: TypeHost, Slot: 77,
		Name: "renamed", Socks5: &Socks5Options{Server: "10.0.0.9", Port: 1081}}))
	endpoint, found := config.FindEndpoint("SOCKS5 1")
	require.True(t, found)
	require.Equal(t, Endpoint{ID: "SOCKS5 1", Type: TypeSocks5, Slot: 2, Name: "renamed",
		Socks5: &Socks5Options{Server: "10.0.0.9", Port: 1081}}, endpoint)
	require.ErrorContains(t, config.UpdateEndpoint("SOCKS5 1", Endpoint{}), "invalid server address")
	require.ErrorContains(t, config.UpdateEndpoint("missing", Endpoint{}), "does not exist")
	_, found = config.FindEndpoint("missing")
	require.False(t, found)
	{
		endpoint.ID = "HOST 1"
		require.EqualError(t, config.UpdateEndpoint("SOCKS5 1", endpoint), "ID 'HOST 1' already exists")
		endpoint.ID = "DIRECT"
		require.EqualError(t, config.UpdateEndpoint("SOCKS5 1", endpoint), "ID 'DIRECT' already exists")
		endpoint.ID = "bad,id"
		require.EqualError(t, config.UpdateEndpoint("SOCKS5 1", endpoint), "invalid ID 'bad,id'")
		endpoint.ID = " SOCKS5 1 "
		require.NoError(t, config.UpdateEndpoint("SOCKS5 1", endpoint))
	}
}

func TestUpdateEndpoint_Rename(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddFallback(Fallback{Enabled: true, Endpoints: []string{"HOST 1",
		"OpenVPN 1"}})
	require.NoError(t, err)
	_, err = config.AddFilter(Filter{Directive: "DOMAIN", Target: "OpenVPN 1",
		Params: []string{"a.test"}})
	require.NoError(t, err)
	_, err = config.AddFilter(Filter{Directive: "DOMAIN", Target: "Fallback 1",
		Params: []string{"b.test"}})
	require.NoError(t, err)
	_, err = config.SetRoutes("OpenVPN 1", []string{"10.0.0.0/8"})
	require.NoError(t, err)
	config.Settings.DefaultTarget = "OpenVPN 1"
	endpoint, _ := config.FindEndpoint("OpenVPN 1")
	endpoint.ID = "Office VPN"
	require.NoError(t, config.UpdateEndpoint("OpenVPN 1", endpoint))
	require.NoError(t, config.Validate())
	require.Equal(t, []string{"HOST 1", "Office VPN"}, config.Fallbacks[0].Endpoints)
	require.Equal(t, "Office VPN", config.Filters[0].Target)
	require.Equal(t, "Fallback 1", config.Filters[1].Target)
	require.Equal(t, Filter{Key: config.Filters[2].Key, ID: "Routes of Office VPN",
		Directive: "IP-CIDR", Target: "Office VPN", Params: []string{"10.0.0.0/8"},
		Provided: "Office VPN"}, config.Filters[2])
	require.Equal(t, "Office VPN", config.Settings.DefaultTarget)
	require.False(t, config.TargetExists("OpenVPN 1"))
	{
		config.Settings.DefaultTarget = "Fallback 1"
		require.NoError(t, config.UpdateFallback("Fallback 1", Fallback{ID: "Main chain",
			Enabled: true, HealthCheck: true, Endpoints: []string{"HOST 1"}}))
		require.NoError(t, config.Validate())
		require.Equal(t, "Main chain", config.Fallbacks[0].ID)
		require.True(t, config.Fallbacks[0].HealthCheck)
		require.Equal(t, "Main chain", config.Filters[1].Target)
		require.Equal(t, "Main chain", config.Settings.DefaultTarget)
	}
	{
		key := config.Filters[2].Key
		config.Filters[2].ID = "custom"
		endpoint.ID = "VPN 2"
		require.NoError(t, config.UpdateEndpoint("Office VPN", endpoint))
		require.Equal(t, "custom", config.Filters[2].ID)
		require.Equal(t, "VPN 2", config.Filters[2].Provided)
		require.Equal(t, key, config.Filters[2].Key)
	}
}

func TestUpdateUser_LastAdministrator(t *testing.T) {
	config := makeConfig(t)
	require.NoError(t, config.AddUser(User{Name: "admin", Role: RoleAdministrator, PasswordHash: "h"}))
	require.NoError(t, config.AddUser(User{Name: "viewer", Role: RoleUser, PasswordHash: "h"}))
	require.ErrorContains(t, config.AddUser(User{Name: "admin", Role: RoleUser, PasswordHash: "h"}),
		"user 'admin' already exists")
	require.ErrorContains(t, config.AddUser(User{Name: "Bad Name", Role: RoleUser,
		PasswordHash: "h"}), "invalid user name 'Bad Name'")
	require.ErrorContains(t, config.AddUser(User{Name: "x", Role: "root", PasswordHash: "h"}),
		"unexpected role 'root'")
	require.ErrorContains(t, config.AddUser(User{Name: "x", Role: RoleUser}),
		"password of 'x' does not exist")
	require.ErrorContains(t, config.UpdateUser("admin", RoleOperator, ""),
		"last administrator cannot be removed")
	require.ErrorContains(t, config.UpdateUser("missing", "", ""), "does not exist")
	require.ErrorContains(t, config.UpdateUser("viewer", "root", ""), "unexpected role")
	config.Users[0].Role = RoleAdministrator
	require.NoError(t, config.UpdateUser("viewer", RoleAdministrator, "new"))
	user, found := config.FindUser("viewer")
	require.True(t, found)
	require.Equal(t, User{Name: "viewer", Role: RoleAdministrator, PasswordHash: "new", Epoch: 1},
		user)
	require.NoError(t, config.RemoveUser("admin"))
	require.ErrorContains(t, config.RemoveUser("viewer"), "last administrator cannot be removed")
	require.ErrorContains(t, config.RemoveUser("admin"), "does not exist")
	_, found = config.FindUser("admin")
	require.False(t, found)
	require.True(t, IsRole(RoleOperator))
	require.False(t, IsRole(RoleAnonymous))
}

func TestValidate_Problems(t *testing.T) {
	for _, item := range []struct {
		mutate  func(config *Config)
		message string
	}{
		{func(config *Config) { config.Users = []User{{Name: "a", Role: RoleUser}} }, "password of 'a'"},
		{func(config *Config) {
			config.Users = []User{{Name: "a", Role: RoleUser, PasswordHash: "h"},
				{Name: "a", Role: RoleUser, PasswordHash: "h"}}
		}, "duplicate user 'a'"},
		{func(config *Config) { config.Endpoints[1].ID = "HOST 1" }, "duplicate endpoint ID 'HOST 1'"},
		{func(config *Config) { config.Endpoints[1].Slot = 1 }, "duplicate endpoint slot (1)"},
		{func(config *Config) { config.Endpoints[1].Slot = 0 }, "invalid slot (0)"},
		{func(config *Config) { config.Endpoints[1].Type = "x" }, "unexpected endpoint type 'x'"},
		{func(config *Config) { config.Fallbacks = []Fallback{{ID: "HOST 1"}} },
			"duplicate fallback ID 'HOST 1'"},
		{func(config *Config) { config.Fallbacks = []Fallback{{ID: "f", Endpoints: []string{"x"}}} },
			"failed to check fallback 'f'"},
		{func(config *Config) { config.Filters = []Filter{{Directive: "DOMAIN"}} },
			"duplicate filter key ''"},
		{func(config *Config) { config.Filters = []Filter{{Key: "k", Directive: "DOMAIN"}} },
			"failed to check filter 1"},
		{func(config *Config) { config.Settings.CheckInterval = 1 }, "invalid check interval (1)"},
		{func(config *Config) { config.Settings.ProbeTarget = "host" }, "failed to check probe target"},
		{func(config *Config) { config.Settings.DefaultTarget = "x" }, "default target 'x'"},
		{func(config *Config) { config.Settings.BindAddress = "all" }, "invalid bind address 'all'"},
	} {
		config := makeConfig(t)
		item.mutate(config)
		require.ErrorContains(t, config.Validate(), item.message)
	}
}
