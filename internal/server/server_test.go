package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/router"
	"proximity/web"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const sampleTunnel = "[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.1.2/32\n" +
	"[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = vpn.example.com:51820\n"
const sampleOpenVPN = "client\ndev tun\nremote vpn.example.com 1194 udp\n"

func makeServer(t *testing.T) *Server {
	directory := t.TempDir()
	tool := filepath.Join(directory, "ip")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/bash\nexit 1\n"), 0o700))
	geodata := filepath.Join(directory, "geodata")
	require.NoError(t, os.MkdirAll(geodata, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(geodata, "geosite.dat"), []byte("geo"), 0o600))
	t.Setenv("PROXIMITY_ADMIN_USER", "admin")
	t.Setenv("PROXIMITY_ADMIN_PASSWORD", "secret123")
	server, err := New(Options{Version: "test", Listen: "127.0.0.1:0",
		ConfigPath: filepath.Join(directory, "config"), RunPath: filepath.Join(directory, "run"),
		GeodataPath: geodata, Executable: "/usr/local/bin/proximity", MihomoBinary: "/bin/false",
		Tools: link.Tools{IP: tool}, Logf: t.Logf})
	require.NoError(t, err)
	t.Cleanup(server.manager.Close)
	require.FileExists(t, filepath.Join(directory, "config", "mihomo", "geosite.dat"))
	return server
}

func call(t *testing.T, server *Server, cookie *http.Cookie, method string, path string,
	body any) (int, map[string]any, *http.Cookie) {
	buffer := bytes.Buffer{}
	if body != nil {
		require.NoError(t, json.NewEncoder(&buffer).Encode(body))
	}
	request := httptest.NewRequest(method, path, &buffer)
	request.Header.Set("X-Requested-With", "test")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	result, raw := map[string]any{}, json.RawMessage{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &raw), recorder.Body.String())
	if json.Unmarshal(raw, &result) != nil {
		list := []any{}
		require.NoError(t, json.Unmarshal(raw, &list))
		result["list"] = list
	}
	for _, item := range recorder.Result().Cookies() {
		if item.Name == cookieName {
			return recorder.Code, result, item
		}
	}
	return recorder.Code, result, nil
}

func fetch(server *Server, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func login(t *testing.T, server *Server, name string, password string) *http.Cookie {
	code, _, cookie := call(t, server, nil, http.MethodPost, "/api/login",
		loginRequest{Username: name, Password: password})
	require.Equal(t, http.StatusOK, code)
	require.NotNil(t, cookie)
	return cookie
}

func TestNew_Problems(t *testing.T) {
	directory := t.TempDir()
	blocker := filepath.Join(directory, "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	_, err := New(Options{ConfigPath: filepath.Join(blocker, "config"), Logf: t.Logf})
	require.ErrorContains(t, err, "server.New failed: config.Open failed")
	{
		t.Setenv("PROXIMITY_ADMIN_PASSWORD", "secret123")
		_, err := New(Options{ConfigPath: filepath.Join(directory, "config"),
			RunPath: filepath.Join(directory, "run"), Tools: link.Tools{IP: blocker + "-missing"},
			Logf: t.Logf})
		require.ErrorContains(t, err, "ip tool does not exist")
	}
}

func TestNew_RouterState(t *testing.T) {
	server := makeServer(t)
	path := filepath.Join(server.options.ConfigPath, routerStateName)
	saved := func() string {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(data)
	}
	reopen := func() *Server {
		result, err := New(server.options)
		require.NoError(t, err)
		t.Cleanup(result.manager.Close)
		return result
	}
	autostart := func(enabled bool) {
		require.NoError(t, server.store.Update(func(config *config.Config) error {
			config.Settings.Autostart = enabled
			return nil
		}))
	}
	require.True(t, server.active())
	require.JSONEq(t, `{"running":true}`, saved())
	require.NoError(t, os.WriteFile(path, []byte(`{"running":false}`), 0o600))
	require.False(t, reopen().active())
	require.JSONEq(t, `{"running":false}`, saved())
	require.NoError(t, os.WriteFile(path, []byte(`{"running":true}`), 0o600))
	autostart(false)
	require.False(t, reopen().active())
	require.JSONEq(t, `{"running":false}`, saved())
	autostart(true)
	require.False(t, reopen().active())
	require.NoError(t, os.WriteFile(path, []byte(`{"running":true}`), 0o600))
	require.True(t, reopen().active())
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	require.True(t, reopen().active())
	require.JSONEq(t, `{"running":true}`, saved())
	{
		require.NoError(t, os.Remove(path))
		require.True(t, reopen().active())
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.MkdirAll(filepath.Join(path, "blocked"), 0o700))
		_, err := New(server.options)
		require.ErrorContains(t, err, "server.New failed: failed to save router state")
	}
}

func TestRun_Shutdown(t *testing.T) {
	server := makeServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		finished <- server.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		_, failure := server.problems()
		return strings.Contains(failure, "failed to check router configuration")
	}, 10*time.Second, 20*time.Millisecond)
	cancel()
	require.NoError(t, <-finished)
	{
		server := makeServer(t)
		server.options.Listen = "256.0.0.1:99999"
		require.ErrorContains(t, server.Run(context.Background()), "failed to serve")
	}
}

func TestServer_Access(t *testing.T) {
	server := makeServer(t)
	code, body, _ := call(t, server, nil, http.MethodGet, "/api/session", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, map[string]any{"user": "", "role": "anonymous", "level": 0.0,
		"version": "test", "anonymous_dashboard": true, "setup": false, "host_network": false},
		body)
	code, body, _ = call(t, server, nil, http.MethodGet, "/api/outbounds", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	require.Equal(t, "authentication required", body["error"])
	{
		request := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
		recorder := httptest.NewRecorder()
		server.mux.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		require.Contains(t, recorder.Body.String(), "request header does not exist")
		require.NotEmpty(t, recorder.Header().Get("X-Proximity-Time"))
	}
	{
		code, body, cookie := call(t, server, nil, http.MethodPost, "/api/login",
			loginRequest{Username: "admin", Password: "wrong"})
		require.Equal(t, http.StatusUnauthorized, code)
		require.Equal(t, "invalid user name or password", body["error"])
		require.Nil(t, cookie)
		code, _, _ = call(t, server, nil, http.MethodPost, "/api/login", "text")
		require.Equal(t, http.StatusBadRequest, code)
	}
	admin := login(t, server, "admin", "secret123")
	{
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/session", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "administrator", body["role"])
		require.Equal(t, 3.0, body["level"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/users",
			userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123", Confirm: "viewer123"})
		require.Equal(t, http.StatusOK, code)
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/users",
			userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123", Confirm: "viewer123"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "user 'viewer' already exists", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/users",
			userRequest{Name: "short", Role: config.RoleUser, Password: "1", Confirm: "1"})
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/users", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []any{map[string]any{"name": "admin", "role": "administrator"},
			map[string]any{"name": "viewer", "role": "user"}}, body["list"])
		code, body, _ = call(t, server, admin, http.MethodPut, "/api/users/admin",
			userRequest{Role: config.RoleUser})
		require.Equal(t, "own role cannot be changed", body["error"])
		code, body, _ = call(t, server, admin, http.MethodDelete, "/api/users/admin", nil)
		require.Equal(t, "own account cannot be removed", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
	}
	viewer := login(t, server, "viewer", "viewer123")
	{
		code, body, _ := call(t, server, viewer, http.MethodPost, "/api/outbounds",
			config.Outbound{Type: config.TypeHost})
		require.Equal(t, http.StatusForbidden, code)
		require.Equal(t, "access denied for role 'user'", body["error"])
		code, _, _ = call(t, server, viewer, http.MethodGet, "/api/users", nil)
		require.Equal(t, http.StatusForbidden, code)
		code, body, _ = call(t, server, viewer, http.MethodPost, "/api/password",
			passwordRequest{Current: "wrong", Password: "viewer456", Confirm: "viewer456"})
		require.Equal(t, "current password is invalid", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, viewer, http.MethodPost, "/api/password",
			passwordRequest{Current: "viewer123", Password: "1", Confirm: "1"})
		require.Equal(t, http.StatusBadRequest, code)
		code, _, cleared := call(t, server, viewer, http.MethodPost, "/api/password",
			passwordRequest{Current: "viewer123", Password: "viewer456", Confirm: "viewer456"})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, -1, cleared.MaxAge)
		code, body, _ = call(t, server, viewer, http.MethodGet, "/api/session", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "anonymous", body["role"])
		viewer = login(t, server, "viewer", "viewer456")
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/users/viewer",
			userRequest{Role: config.RoleOperator, Password: "viewer789", Confirm: "viewer789"})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/users/viewer",
			userRequest{Password: "1", Confirm: "1"})
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, viewer, http.MethodGet, "/api/outbounds", nil)
		require.Equal(t, http.StatusUnauthorized, code)
		operator := login(t, server, "viewer", "viewer789")
		code, _, _ = call(t, server, operator, http.MethodPost, "/api/outbounds",
			config.Outbound{Type: config.TypeHost, Enabled: true})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, operator, http.MethodPut, "/api/settings", config.Settings{})
		require.Equal(t, http.StatusForbidden, code)
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/users/viewer", nil)
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, operator, http.MethodGet, "/api/outbounds", nil)
		require.Equal(t, http.StatusUnauthorized, code)
	}
	{
		settings := server.store.Snapshot().Settings
		settings.AnonymousDashboard = false
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/settings", settings)
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/settings", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, false, body["anonymous_dashboard"])
		code, _, _ = call(t, server, nil, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusUnauthorized, code)
		code, _, _ = call(t, server, nil, http.MethodGet, "/api/history", nil)
		require.Equal(t, http.StatusUnauthorized, code)
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/history?since=1", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []any{}, body["samples"])
		settings.LogLevel = "loud"
		code, body, _ = call(t, server, admin, http.MethodPut, "/api/settings", settings)
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body["error"], "unexpected log level 'loud'")
		code, _, cleared := call(t, server, admin, http.MethodPost, "/api/logout", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, -1, cleared.MaxAge)
	}
}

func TestServer_Backup(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	code, _, _ := call(t, server, admin, http.MethodPost, "/api/outbounds", config.Outbound{
		Type: config.TypeSocks5, Enabled: true, Socks5: &config.Socks5Options{Server: "10.0.0.1",
			Port: 1080, Password: "pass"}})
	require.Equal(t, http.StatusOK, code)
	code, _, _ = call(t, server, admin, http.MethodPost, "/api/routes", config.Route{
		Directive: "DOMAIN", Target: "SOCKS5 1", Enabled: true, Params: []string{"a.test"}})
	require.Equal(t, http.StatusOK, code)
	code, _, _ = call(t, server, admin, http.MethodPost, "/api/users",
		userRequest{Name: "operator", Role: config.RoleOperator, Password: "operator1",
			Confirm: "operator1"})
	require.Equal(t, http.StatusOK, code)
	code, _, _ = call(t, server, admin, http.MethodPost, "/api/users",
		userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123",
			Confirm: "viewer123"})
	require.Equal(t, http.StatusOK, code)
	code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds", config.Inbound{
		Type: config.TypeSocks5, Enabled: true, Outbound: "SOCKS5 1", Anonymous: true})
	require.Equal(t, http.StatusOK, code)
	code, exported, _ := call(t, server, admin, http.MethodGet, "/api/backup", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, config.BackupFormat, exported["format"])
	require.Equal(t, 7.0, exported["version"])
	require.Len(t, exported["inbounds"], 1)
	require.Len(t, exported["outbounds"], 1)
	require.Len(t, exported["routes"], 1)
	require.NotContains(t, exported, "users")
	require.NotContains(t, exported, "secret")
	{
		viewer := login(t, server, "viewer", "viewer123")
		code, _, _ := call(t, server, viewer, http.MethodGet, "/api/backup", nil)
		require.Equal(t, http.StatusForbidden, code)
		code, _, _ = call(t, server, viewer, http.MethodPost, "/api/backup", exported)
		require.Equal(t, http.StatusForbidden, code)
		code, _, _ = call(t, server, nil, http.MethodGet, "/api/backup", nil)
		require.Equal(t, http.StatusUnauthorized, code)
	}
	{
		code, _, _ := call(t, server, admin, http.MethodDelete, "/api/outbounds/SOCKS5%201", nil)
		require.Equal(t, http.StatusOK, code)
		require.Empty(t, server.store.Snapshot().Outbounds)
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/inbounds/SOCKS5%201", nil)
		require.Equal(t, http.StatusOK, code)
		require.Empty(t, server.store.Snapshot().Inbounds)
		settings := exported["settings"].(map[string]any)
		settings["mixed_port"], settings["log_level"] = 7000.0, "debug"
		operator := login(t, server, "operator", "operator1")
		code, body, _ := call(t, server, operator, http.MethodPost, "/api/backup", exported)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, map[string]any{"outbounds": 1.0, "fallbacks": 0.0, "routes": 1.0,
			"inbounds": 1.0}, body)
		snapshot := server.store.Snapshot()
		require.Equal(t, "SOCKS5 1", snapshot.Inbounds[0].Outbound)
		require.Equal(t, "SOCKS5 1", snapshot.Outbounds[0].ID)
		require.Equal(t, "pass", snapshot.Outbounds[0].Socks5.Password)
		require.Equal(t, "SOCKS5 1", snapshot.Routes[0].Target)
		require.Equal(t, 7899, snapshot.Settings.MixedPort)
		require.Len(t, snapshot.Users, 3)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/backup", exported)
		require.Equal(t, http.StatusOK, code)
		snapshot = server.store.Snapshot()
		require.Equal(t, 7000, snapshot.Settings.MixedPort)
		require.Equal(t, "debug", snapshot.Settings.LogLevel)
		require.Len(t, snapshot.Users, 3)
	}
	{
		exported["format"] = "other"
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/backup", exported)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "failed to import backup: unexpected backup format 'other'", body["error"])
		exported["format"] = config.BackupFormat
		exported["fallbacks"] = []any{map[string]any{"id": "Chain", "outbounds": []any{"missing"}}}
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/backup", exported)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "failed to import backup: failed to check fallback 'Chain': "+
			"outbound 'missing' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/backup", 5)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "SOCKS5 1", server.store.Snapshot().Routes[0].Target)
	}
}

func TestServer_Inbounds(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	code, _, _ := call(t, server, admin, http.MethodPost, "/api/users",
		userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123",
			Confirm: "viewer123"})
	require.Equal(t, http.StatusOK, code)
	viewer := login(t, server, "viewer", "viewer123")
	code, body, _ := call(t, server, admin, http.MethodPost, "/api/inbounds", config.Inbound{
		Type: config.TypeSocks5, Enabled: true, Statistics: true,
		Users:  []config.InboundUser{{Enabled: true, Name: "alice", Password: "secret"}},
		Socks5: &config.Socks5Server{TLS: true}})
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "SOCKS5 1", body["id"])
	require.Equal(t, 7800.0, body["port"])
	require.Contains(t, body["socks5"].(map[string]any)["key"], "PRIVATE KEY")
	code, body, _ = call(t, server, admin, http.MethodPost, "/api/inbounds", config.Inbound{
		Type: config.TypeOpenVPN, Enabled: true, Outbound: config.ViaHost,
		Users: []config.InboundUser{{Enabled: true, Name: "bob", Password: "builder"}}})
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "OpenVPN 1", body["id"])
	require.Equal(t, 7801.0, body["port"])
	created := body["openvpn"].(map[string]any)
	require.Equal(t, "10.77.2.0/24", created["network"])
	code, body, _ = call(t, server, admin, http.MethodPost, "/api/inbounds", config.Inbound{
		Type: config.TypeSocks5, Port: 7800})
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "port (7800) is used by inbound 'SOCKS5 1'", body["error"])
	code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds", "text")
	require.Equal(t, http.StatusBadRequest, code)
	code, _, _ = call(t, server, viewer, http.MethodPost, "/api/inbounds", config.Inbound{})
	require.Equal(t, http.StatusForbidden, code)
	{
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/inbounds", nil)
		require.Equal(t, http.StatusOK, code)
		require.NotContains(t, body, "groups")
		require.Equal(t, []any{7800.0, 7898.0}, body["ports"])
		items := body["inbounds"].([]any)
		require.Len(t, items, 2)
		first, second := items[0].(map[string]any), items[1].(map[string]any)
		require.Equal(t, false, first["redacted"])
		require.Equal(t, "secret", first["users"].([]any)[0].(map[string]any)["password"])
		require.Equal(t, map[string]any{"state": "disabled", "message": "", "since": 0.0,
			"device": "", "address": "", "clients": []any{}}, first["status"])
		require.Contains(t, second["openvpn"].(map[string]any)["client_key"], "PRIVATE KEY")
		code, body, _ = call(t, server, viewer, http.MethodGet, "/api/inbounds", nil)
		require.Equal(t, http.StatusOK, code)
		items = body["inbounds"].([]any)
		first, second = items[0].(map[string]any), items[1].(map[string]any)
		require.Equal(t, true, first["redacted"])
		require.Empty(t, first["users"].([]any)[0].(map[string]any)["password"])
		require.Empty(t, first["socks5"].(map[string]any)["key"])
		require.NotEmpty(t, first["socks5"].(map[string]any)["certificate"])
		hidden := second["openvpn"].(map[string]any)
		require.Empty(t, hidden["ca_key"].(string)+hidden["key"].(string)+
			hidden["client_key"].(string)+hidden["tls_crypt"].(string)+hidden["otp_key"].(string))
		require.NotEmpty(t, hidden["ca_certificate"])
		require.NotEmpty(t, server.store.Snapshot().Inbounds[1].OpenVPN.ClientKey)
		code, _, _ = call(t, server, nil, http.MethodGet, "/api/inbounds", nil)
		require.Equal(t, http.StatusUnauthorized, code)
	}
	{
		query := "/api/inbounds/OpenVPN%201/config?host=vpn.example.com"
		code, body, _ := call(t, server, admin, http.MethodGet, query+"&user=bob", nil)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, "OpenVPN 1-bob.conf", body["file"])
		require.Contains(t, body["config"], "remote vpn.example.com 7801\n")
		require.Contains(t, body["config"], "\nauth-user-pass\n")
		require.NotContains(t, body["config"], "builder")
		code, body, _ = call(t, server, admin, http.MethodGet, query+"&user=bob&embed=true", nil)
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, body["config"], "<auth-user-pass>\nbob\nbuilder\n</auth-user-pass>\n")
		code, body, _ = call(t, server, admin, http.MethodGet, query, nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "failed to export configuration: anonymous connections are not allowed",
			body["error"])
		code, body, _ = call(t, server, admin, http.MethodGet,
			"/api/inbounds/missing/config?user=bob", nil)
		require.Equal(t, http.StatusNotFound, code)
		require.Equal(t, "inbound 'missing' does not exist", body["error"])
		code, _, _ = call(t, server, viewer, http.MethodGet, query+"&user=bob", nil)
		require.Equal(t, http.StatusForbidden, code)
	}
	{
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/inbounds/generate",
			generateRequest{Kind: config.MaterialAll})
		require.Equal(t, http.StatusOK, code, body)
		require.Len(t, body, 8)
		material := config.Material{CACertificate: body["ca_certificate"].(string),
			CAKey: body["ca_key"].(string)}
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/generate",
			generateRequest{Kind: config.MaterialClient, Material: material})
		require.Equal(t, http.StatusOK, code, body)
		require.Len(t, body, 2)
		require.Contains(t, body["client_certificate"], "BEGIN CERTIFICATE")
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/generate",
			generateRequest{Kind: "other"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "unexpected material kind 'other'", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/generate", 5)
		require.Equal(t, http.StatusBadRequest, code)
	}
	{
		server.tracker.SetInbounds(server.store.Snapshot().Inbounds)
		server.tracker.UpdateInbounds(router.Connections{}, map[string][2]int64{"SOCKS5 1": {1, 2}})
		server.tracker.UpdateInbounds(router.Connections{}, map[string][2]int64{"SOCKS5 1": {11, 22}})
		current := server.store.Snapshot().Inbounds[0]
		current.ID, current.Anonymous = "Edge", true
		code, body, _ := call(t, server, admin, http.MethodPut, "/api/inbounds/SOCKS5%201", current)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, "Edge", body["id"])
		require.Equal(t, int64(10), server.tracker.Inbounds()["Edge"].Upload)
		code, body, _ = call(t, server, admin, http.MethodPut, "/api/inbounds/SOCKS5%201", current)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "inbound 'SOCKS5 1' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/inbounds/Edge", "text")
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/Edge/move",
			moveRequest{Delta: 1})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "Edge", server.store.Snapshot().Inbounds[1].ID)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/Edge/move", "text")
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/Edge/restart", nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "inbound 'Edge' does not exist", body["error"])
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/inbounds/Edge/log", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []any{}, body["lines"])
	}
	{
		server.synchronize()
		states := server.manager.Inbounds()
		require.Contains(t, states, "Edge")
		require.Contains(t, states, "OpenVPN 1")
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/inbounds", nil)
		require.Equal(t, http.StatusOK, code)
		status := body["inbounds"].([]any)[1].(map[string]any)["status"].(map[string]any)
		require.NotEqual(t, "disabled", status["state"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/Edge/restart", nil)
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, viewer, http.MethodPost, "/api/inbounds/Edge/stop", nil)
		require.Equal(t, http.StatusForbidden, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/Edge/stop", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, link.StateStopped, server.manager.Inbounds()["Edge"].State)
		require.NotContains(t, server.manager.Serving(), "Edge")
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/Edge/start", nil)
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, server.manager.Serving(), "Edge")
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/inbounds/missing/start", nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "inbound 'missing' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/inbounds/Edge", nil)
		require.Equal(t, http.StatusOK, code)
		code, body, _ = call(t, server, admin, http.MethodDelete, "/api/inbounds/Edge", nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "inbound 'Edge' does not exist", body["error"])
		server.synchronize()
		require.NotContains(t, server.manager.Inbounds(), "Edge")
	}
}

func TestServer_Outbounds(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	for _, outbound := range []config.Outbound{
		{Type: config.TypeHost, Enabled: true, Autostart: true, Statistics: true},
		{Type: config.TypeSocks5, Enabled: true, Autostart: true, Socks5: &config.Socks5Options{
			Server: "127.0.0.1", Port: 1, Username: "user", Password: "pass"}},
		{Type: config.TypeHTTP, HTTP: &config.HTTPOptions{Server: "proxy.test",
			Port: 3128, Password: "pass", Single: true}},
		{Type: config.TypeOpenVPN, OpenVPN: &config.OpenVPNOptions{Config: sampleOpenVPN,
			Password: "pass", OTPKey: "GEZDGNBVGY3TQOJQ", KeyPassword: "key"}},
		{Type: config.TypeAmneziaWG, AmneziaWG: &config.AmneziaWGOptions{Config: sampleTunnel}}} {
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/outbounds", outbound)
		require.Equal(t, http.StatusOK, code, body)
	}
	code, body, _ := call(t, server, admin, http.MethodPost, "/api/outbounds",
		config.Outbound{Type: "unknown"})
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "unexpected outbound type 'unknown'", body["error"])
	code, _, _ = call(t, server, admin, http.MethodPost, "/api/outbounds", "text")
	require.Equal(t, http.StatusBadRequest, code)
	server.synchronize()
	require.Eventually(t, func() bool {
		state := server.manager.Status()["SOCKS5 1"].State
		return state == link.StateReconnecting || state == link.StateFailed
	}, 10*time.Second, 20*time.Millisecond)
	{
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/outbounds", nil)
		require.Equal(t, http.StatusOK, code)
		require.NotContains(t, body, "groups")
		views := body["outbounds"].([]any)
		require.Len(t, views, 5)
		first, second := views[0].(map[string]any), views[1].(map[string]any)
		require.Equal(t, "HOST 1", first["id"])
		require.Equal(t, "unused", first["usage"])
		require.Equal(t, false, first["redacted"])
		require.Equal(t, "pass", second["socks5"].(map[string]any)["password"])
		require.Equal(t, "reconnecting", second["status"].(map[string]any)["state"])
		require.Equal(t, "disabled", views[2].(map[string]any)["usage"])
		require.Equal(t, "disabled", views[3].(map[string]any)["status"].(map[string]any)["state"])
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/users",
			userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123", Confirm: "viewer123"})
		require.Equal(t, http.StatusOK, code)
		viewer := login(t, server, "viewer", "viewer123")
		code, body, _ := call(t, server, viewer, http.MethodGet, "/api/outbounds", nil)
		require.Equal(t, http.StatusOK, code)
		views := body["outbounds"].([]any)
		require.Equal(t, true, views[1].(map[string]any)["redacted"])
		require.Equal(t, "", views[1].(map[string]any)["socks5"].(map[string]any)["password"])
		require.Equal(t, "user", views[1].(map[string]any)["socks5"].(map[string]any)["username"])
		require.Equal(t, "", views[2].(map[string]any)["http"].(map[string]any)["password"])
		tunnel := views[3].(map[string]any)["openvpn"].(map[string]any)
		require.Equal(t, "", tunnel["config"])
		require.Equal(t, "", tunnel["otp_key"])
		require.Equal(t, "", views[4].(map[string]any)["amneziawg"].(map[string]any)["config"])
		code, _, _ = call(t, server, viewer, http.MethodGet, "/api/outbounds/HOST%201/log", nil)
		require.Equal(t, http.StatusForbidden, code)
	}
	{
		code, body, _ := call(t, server, nil, http.MethodGet, "/api/history", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, 5.0, body["step"])
		require.Equal(t, []any{}, body["samples"])
		code, body, _ = call(t, server, nil, http.MethodGet, "/api/history?since=bad", nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, `cannot parse "bad" as time`, body["error"])
		code, body, _ = call(t, server, nil, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusOK, code)
		views := body["outbounds"].([]any)
		second := views[1].(map[string]any)
		require.Equal(t, "SOCKS5 1", second["id"])
		require.NotContains(t, second, "socks5")
		require.Equal(t, "reconnecting", second["status"].(map[string]any)["state"])
		require.Equal(t, "", second["status"].(map[string]any)["message"])
		require.Empty(t, body["warnings"])
		require.Equal(t, map[string]any{"outbounds": 5.0, "outbounds_enabled": 2.0,
			"outbounds_up": 1.0, "fallbacks": 0.0, "routes": 0.0, "rules": 0.0}, body["counts"])
		require.Contains(t, body["failure"], "failed to check router configuration")
		require.Equal(t, 7899.0, body["proxy_port"])
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/outbounds/SOCKS5%201",
			config.Outbound{Enabled: true, Socks5: &config.Socks5Options{
				Server: "127.0.0.1", Port: 2}})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/outbounds/SOCKS5%201", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ := call(t, server, admin, http.MethodPut, "/api/outbounds/missing",
			config.Outbound{})
		require.Equal(t, "outbound 'missing' does not exist", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/outbounds/SOCKS5%201/move",
			moveRequest{Delta: -1})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/outbounds/SOCKS5%201/move", "x")
		require.Equal(t, http.StatusBadRequest, code)
		snapshot := server.store.Snapshot()
		require.Equal(t, "SOCKS5 1", snapshot.Outbounds[0].ID)
		require.False(t, snapshot.Outbounds[0].Autostart)
		require.Equal(t, 2, snapshot.Outbounds[0].Socks5.Port)
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/outbounds/HOST%201/restart", nil)
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/outbounds/HTTP%201/restart",
			nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "outbound 'HTTP 1' is disabled", body["error"])
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/outbounds/HOST%201/stop",
			nil)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, link.StateStopped, server.manager.Status()["HOST 1"].State)
		code, body, _ = call(t, server, admin, http.MethodPost,
			"/api/outbounds/HOST%201/start", nil)
		require.Equal(t, http.StatusOK, code, body)
		require.NotEqual(t, link.StateStopped, server.manager.Status()["HOST 1"].State)
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/outbounds",
			map[string]any{"type": config.TypeHost})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, true, body["autostart"])
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/inbounds",
			map[string]any{"type": config.TypeSocks5, "anonymous": true})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, true, body["autostart"])
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/inbounds/SOCKS5%201", nil)
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/outbounds/HOST%202", nil)
		require.Equal(t, http.StatusOK, code)
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/outbounds/HOST%201/log", nil)
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, body, "lines")
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/interfaces", nil)
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, body, "list")
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/targets", nil)
		require.Equal(t, http.StatusOK, code)
		require.Len(t, body["list"], 7)
		require.Equal(t, map[string]any{"id": "DIRECT", "kind": "builtin", "enabled": true},
			body["list"].([]any)[0])
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/import/amneziawg",
			importRequest{Text: sampleTunnel})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, strings.TrimSpace(sampleTunnel)+"\n", body["config"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/import/amneziawg",
			importRequest{Text: "vpn://broken"})
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/import/amneziawg", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/outbounds/HTTP%201", nil)
		require.Equal(t, http.StatusOK, code)
		require.Len(t, server.store.Snapshot().Outbounds, 4)
	}
}

func TestServer_ProvidedRoutes(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	code, _, _ := call(t, server, admin, http.MethodPost, "/api/outbounds", config.Outbound{
		Type: config.TypeAmneziaWG, Enabled: true, AmneziaWG: &config.AmneziaWGOptions{
			Config: sampleTunnel + "AllowedIPs = 10.55.0.0/16\n"}})
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, server.store.Update(func(config *config.Config) error {
		_, err := config.SetRoutes("AmneziaWG 1", []string{"10.55.0.0/16"})
		return err
	}))
	snapshot := server.store.Snapshot()
	require.Len(t, snapshot.Routes, 1)
	require.Equal(t, "AmneziaWG 1", snapshot.Routes[0].Provided)
	require.False(t, snapshot.Routes[0].Enabled)
	key := url.PathEscape(snapshot.Routes[0].Key)
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/routes/"+key,
			config.Route{Enabled: true, Directive: "DOMAIN", Params: []string{"ignored.test"}})
		require.Equal(t, http.StatusOK, code)
		snapshot := server.store.Snapshot()
		require.True(t, snapshot.Routes[0].Enabled)
		require.Equal(t, []string{"10.55.0.0/16"}, snapshot.Routes[0].Params)
		code, body, _ := call(t, server, admin, http.MethodDelete, "/api/routes/"+key, nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "provided route is read-only", body["error"])
	}
}

func TestServer_Rename(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	for _, outbound := range []config.Outbound{
		{Type: config.TypeHost, Enabled: true, Statistics: true},
		{Type: config.TypeHost, Enabled: true, Statistics: true}} {
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/outbounds", outbound)
		require.Equal(t, http.StatusOK, code)
	}
	code, _, _ := call(t, server, admin, http.MethodPost, "/api/fallbacks",
		config.Fallback{Enabled: true, Outbounds: []string{"HOST 1", "HOST 2"}})
	require.Equal(t, http.StatusOK, code)
	code, body, _ := call(t, server, admin, http.MethodPost, "/api/routes", config.Route{
		Directive: "DOMAIN", Target: "HOST 1", Enabled: true, Params: []string{"a.test"}})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "Route 1", body["id"])
	key := body["key"].(string)
	server.synchronize()
	server.tracker.Update(router.Connections{Items: []router.Connection{{ID: "1", Upload: 10,
		Download: 20, Chains: []string{"HOST 1"}}}, Upload: 10, Download: 20}, nil)
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/outbounds/HOST%201",
			config.Outbound{ID: " Uplink ", Enabled: true, Statistics: true})
		require.Equal(t, http.StatusOK, code)
		snapshot := server.store.Snapshot()
		require.Equal(t, "Uplink", snapshot.Outbounds[0].ID)
		require.Equal(t, []string{"Uplink", "HOST 2"}, snapshot.Fallbacks[0].Outbounds)
		require.Equal(t, "Uplink", snapshot.Routes[0].Target)
		traffic, _ := server.tracker.Snapshot()
		require.Equal(t, int64(10), traffic["Uplink"].Upload)
		require.NotContains(t, traffic, "HOST 1")
		code, body, _ := call(t, server, admin, http.MethodPut, "/api/outbounds/Uplink",
			config.Outbound{ID: "HOST 2", Enabled: true})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "ID 'HOST 2' already exists", body["error"])
		code, body, _ = call(t, server, admin, http.MethodPut, "/api/outbounds/Uplink",
			config.Outbound{ID: "Fallback 1", Enabled: true})
		require.Equal(t, "ID 'Fallback 1' already exists", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/fallbacks/Fallback%201",
			config.Fallback{ID: "Chain", Enabled: true, Outbounds: []string{"Uplink"}})
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodPut, "/api/fallbacks/Chain",
			config.Fallback{ID: "Uplink", Enabled: true, Outbounds: []string{"Uplink"}})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "ID 'Uplink' already exists", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/routes/"+key, config.Route{
			ID: "Work", Directive: "DOMAIN", Target: "Chain", Enabled: true,
			Params: []string{"a.test"}})
		require.Equal(t, http.StatusOK, code)
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/routes", config.Route{
			ID: "Work", Directive: "DOMAIN", Target: "Chain", Params: []string{"b.test"}})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "route ID 'Work' already exists", body["error"])
		snapshot := server.store.Snapshot()
		require.Equal(t, "Chain", snapshot.Fallbacks[0].ID)
		require.Equal(t, "Work", snapshot.Routes[0].ID)
		require.Equal(t, "Chain", snapshot.Routes[0].Target)
	}
}

func TestServer_RouterControl(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	code, _, _ := call(t, server, admin, http.MethodPost, "/api/users",
		userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123",
			Confirm: "viewer123"})
	require.Equal(t, http.StatusOK, code)
	viewer := login(t, server, "viewer", "viewer123")
	code, _, _ = call(t, server, admin, http.MethodPost, "/api/inbounds", config.Inbound{
		Type: config.TypeSocks5, Enabled: true, Autostart: true, Anonymous: true})
	require.Equal(t, http.StatusOK, code)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		server.loop(ctx)
		close(finished)
	}()
	defer func() {
		cancel()
		<-finished
	}()
	server.notify()
	control := func(action string) (map[string]any, map[string]any) {
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/router/"+action, nil)
		require.Equal(t, http.StatusOK, code, body)
		require.Equal(t, true, body["completed"])
		return body["router"].(map[string]any), body
	}
	saved := func() string {
		data, err := os.ReadFile(filepath.Join(server.options.ConfigPath, routerStateName))
		require.NoError(t, err)
		return string(data)
	}
	{
		code, body, _ := call(t, server, viewer, http.MethodPost, "/api/router/stop", nil)
		require.Equal(t, http.StatusForbidden, code)
		require.Equal(t, "access denied for role 'user'", body["error"])
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/router/pause", nil)
		require.Equal(t, http.StatusNotFound, code)
		require.Equal(t, "unexpected router action 'pause'", body["error"])
	}
	state, body := control("stop")
	require.Equal(t, true, state["stopped"])
	require.Equal(t, false, state["running"])
	require.Empty(t, body["failure"])
	require.JSONEq(t, `{"running":false}`, saved())
	require.False(t, server.active())
	{
		code, body, _ := call(t, server, viewer, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, true, body["router"].(map[string]any)["stopped"])
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/inbounds", nil)
		require.Equal(t, http.StatusOK, code)
		status := body["inbounds"].([]any)[0].(map[string]any)["status"].(map[string]any)
		require.Equal(t, "router is stopped", status["message"])
	}
	state, body = control("start")
	require.Equal(t, false, state["stopped"])
	require.Contains(t, body["failure"], "failed to check router configuration")
	require.JSONEq(t, `{"running":true}`, saved())
	require.True(t, server.active())
	state, body = control("restart")
	require.Equal(t, false, state["stopped"])
	require.Contains(t, body["failure"], "failed to check router configuration")
	require.JSONEq(t, `{"running":true}`, saved())
}

func TestServer_Routes(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	for _, outbound := range []config.Outbound{{Type: config.TypeHost, Enabled: true},
		{Type: config.TypeHost, Enabled: true}} {
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/outbounds", outbound)
		require.Equal(t, http.StatusOK, code)
	}
	code, body, _ := call(t, server, admin, http.MethodPost, "/api/fallbacks",
		config.Fallback{Enabled: true, Outbounds: []string{"HOST 1"}})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "Fallback 1", body["id"])
	{
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/fallbacks",
			config.Fallback{ID: "Second", Outbounds: []string{"HOST 2", "HOST 1"}})
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/fallbacks",
			config.Fallback{Outbounds: []string{"missing"}})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "outbound 'missing' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/fallbacks", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/fallbacks/Second",
			config.Fallback{Enabled: true, Outbounds: []string{"HOST 2"}, Interval: 120})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/fallbacks/Second", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/fallbacks/Second/move",
			moveRequest{Delta: -1})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/fallbacks/Second/move", "x")
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/fallbacks", nil)
		require.Equal(t, http.StatusOK, code)
		list := body["list"].([]any)
		require.Equal(t, "Second", list[0].(map[string]any)["id"])
		require.Equal(t, 120.0, list[0].(map[string]any)["interval"])
	}
	keys := []string{}
	for _, route := range []config.Route{
		{ID: "corp", Directive: "DOMAIN-SUFFIX", Target: "Fallback 1", Enabled: true,
			Params: []string{"https://Corp.Test/path", ""}},
		{Directive: "DOMAIN", Target: "HOST 2", Enabled: true, Params: []string{"wiki.corp.test"}},
		{Directive: "IP-CIDR", Target: "REJECT", Enabled: true, Params: []string{"1.2.3.4:443"}}} {
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/routes", route)
		require.Equal(t, http.StatusOK, code, body)
		keys = append(keys, body["key"].(string))
	}
	{
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/routes",
			config.Route{Directive: "DOMAIN", Target: "missing", Params: []string{"a.test"}})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "target 'missing' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/routes", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/routes/preview",
			config.Route{Directive: "IP-CIDR", Target: "HOST 1",
				Params: []string{"10.1.2.3:8080", "192.168.0.1"}})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []any{"AND,((IP-CIDR,10.1.2.3/32),(DST-PORT,8080)),HOST 1",
			"IP-CIDR,192.168.0.1/32,HOST 1"}, body["lines"])
		require.Equal(t, []any{"10.1.2.3/32:8080", "192.168.0.1/32"}, body["params"])
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/routes/preview",
			config.Route{Directive: "DOMAIN", Target: "HOST 1",
				Params: []string{"# corporate", "Wiki.Corp.Test", " # off.test"}})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []any{"DOMAIN,wiki.corp.test,HOST 1"}, body["lines"])
		require.Equal(t, []any{"# corporate", "wiki.corp.test", "# off.test"}, body["params"])
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/routes/preview",
			config.Route{Directive: "DOMAIN", Params: []string{"bad domain"}})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body["error"], "failed to parse line 1")
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/routes/preview", 5)
		require.Equal(t, http.StatusBadRequest, code)
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/routes/default",
			targetRequest{Target: "Second"})
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodPut, "/api/routes/default",
			targetRequest{Target: "missing"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body["error"], "default target 'missing' does not exist")
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/routes/default", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/routes/"+keys[2]+"/move",
			moveRequest{Delta: -1})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/routes/"+keys[2]+"/move", "x")
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/routes/"+keys[1],
			config.Route{Directive: "DOMAIN", Target: "HOST 2", Enabled: true,
				Params: []string{"wiki.corp.test", "corp.test"}})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/routes/"+keys[1], 5)
		require.Equal(t, http.StatusBadRequest, code)
		server.synchronize()
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/routes", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "Second", body["default_target"])
		require.Contains(t, body["directives"], "GEOSITE")
		require.Contains(t, body["failure"], "failed to check router configuration")
		routes := body["routes"].([]any)
		require.Len(t, routes, 3)
		require.Equal(t, keys[2], routes[1].(map[string]any)["key"])
		require.Equal(t, []any{"corp.test"}, routes[0].(map[string]any)["params"])
		warnings := body["warnings"].([]any)
		require.Len(t, warnings, 2)
		require.Contains(t, warnings[0].(map[string]any)["message"],
			"DOMAIN-SUFFIX 'corp.test' in 'corp' covers DOMAIN 'wiki.corp.test' in 'Route 1'")
	}
	{
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/router/config", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "", body["config"])
		server.tracker.Observe("[TCP] dial HOST 1 (match Match/) 10.0.0.1:1 --> a.test:80 " +
			"error: connect: connection refused")
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusOK, code)
		require.Len(t, body["refused"], 1)
		require.Equal(t, 4.0, body["counts"].(map[string]any)["rules"])
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/refused", nil)
		require.Equal(t, http.StatusOK, code)
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusOK, code)
		require.Empty(t, body["refused"])
	}
	{
		code, _, _ := call(t, server, admin, http.MethodDelete, "/api/routes/"+keys[0], nil)
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodDelete, "/api/routes/"+keys[0], nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "route '"+keys[0]+"' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/fallbacks/Second", nil)
		require.Equal(t, http.StatusOK, code)
		snapshot := server.store.Snapshot()
		require.Equal(t, config.TargetDirect, snapshot.Settings.DefaultTarget)
		require.Len(t, snapshot.Routes, 2)
	}
}

func TestServer_Setup(t *testing.T) {
	directory := t.TempDir()
	tool := filepath.Join(directory, "ip")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/bash\nexit 1\n"), 0o700))
	t.Setenv("PROXIMITY_ADMIN_PASSWORD", "")
	server, err := New(Options{Version: "test", ConfigPath: filepath.Join(directory, "config"),
		RunPath: filepath.Join(directory, "run"), MihomoBinary: "/bin/false", HostNetwork: true,
		Tools: link.Tools{IP: tool}, Logf: t.Logf})
	require.NoError(t, err)
	t.Cleanup(server.manager.Close)
	code, body, _ := call(t, server, nil, http.MethodGet, "/api/session", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, body["setup"])
	require.Equal(t, true, body["host_network"])
	require.NoFileExists(t, filepath.Join(directory, "config", "initial_admin_password.txt"))
	{
		code, body, cookie := call(t, server, nil, http.MethodPost, "/api/setup",
			setupRequest{Username: "owner", Password: "secret123", Confirm: "secret124"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "passwords do not match", body["error"])
		require.Nil(t, cookie)
		code, body, _ = call(t, server, nil, http.MethodPost, "/api/setup",
			setupRequest{Username: "owner", Password: "1", Confirm: "1"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body["error"], "invalid password length (1)")
		code, body, _ = call(t, server, nil, http.MethodPost, "/api/setup",
			setupRequest{Username: "Bad Name", Password: "secret123", Confirm: "secret123"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body["error"], "invalid user name 'Bad Name'")
		code, _, _ = call(t, server, nil, http.MethodPost, "/api/setup", 5)
		require.Equal(t, http.StatusBadRequest, code)
		require.Empty(t, server.store.Snapshot().Users)
	}
	{
		code, _, cookie := call(t, server, nil, http.MethodPost, "/api/setup",
			setupRequest{Username: "owner", Password: "secret123", Confirm: "secret123"})
		require.Equal(t, http.StatusOK, code)
		require.NotNil(t, cookie)
		code, body, _ := call(t, server, cookie, http.MethodGet, "/api/session", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "owner", body["user"])
		require.Equal(t, "administrator", body["role"])
		require.Equal(t, false, body["setup"])
		code, body, _ = call(t, server, nil, http.MethodPost, "/api/setup",
			setupRequest{Username: "second", Password: "secret123", Confirm: "secret123"})
		require.Equal(t, http.StatusForbidden, code)
		require.Equal(t, "administrator already exists", body["error"])
		login(t, server, "owner", "secret123")
		code, body, _ = call(t, server, cookie, http.MethodPost, "/api/users",
			userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123",
				Confirm: "other"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "passwords do not match", body["error"])
		code, body, _ = call(t, server, cookie, http.MethodPut, "/api/users/owner",
			userRequest{Password: "viewer123"})
		require.Equal(t, "passwords do not match", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ = call(t, server, cookie, http.MethodPost, "/api/password",
			passwordRequest{Current: "secret123", Password: "secret456", Confirm: "secret457"})
		require.Equal(t, "passwords do not match", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
	}
	{
		server.synchronize()
		require.False(t, server.capturing())
		code, body, _ := call(t, server, nil, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, false, body["capture"])
	}
}

func TestServer_Trace(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	for _, outbound := range []config.Outbound{
		{ID: "Quiet", Type: config.TypeHost, Enabled: true},
		{ID: "Loud", Type: config.TypeHost, Enabled: true, Verbose: true}} {
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/outbounds", outbound)
		require.Equal(t, http.StatusOK, code)
	}
	code, _, _ := call(t, server, admin, http.MethodPost, "/api/inbounds", config.Inbound{
		ID: "Team", Type: config.TypeSocks5, Enabled: true, Verbose: true, Anonymous: true,
		Outbound: config.ViaRouter, Socks5: &config.Socks5Server{Listen: config.ListenAny}})
	require.Equal(t, http.StatusOK, code)
	server.synchronize()
	require.True(t, server.store.Snapshot().Outbounds[1].Verbose)
	loud := router.Connection{ID: "1", Upload: 2048, Download: 3 << 20, Chains: []string{"Loud"},
		Rule: "Match", Metadata: router.Metadata{Network: "tcp", Source: "127.0.0.1",
			SourcePort: "40000", Destination: "192.0.2.10", Port: "443", Host: "a.test"}}
	quiet := router.Connection{ID: "2", Chains: []string{"Quiet"}, Rule: "Match",
		Metadata: router.Metadata{Network: "tcp", Inbound: "Team", Source: "127.0.0.1",
			SourcePort: "40001", Destination: "192.0.2.11", Port: "80"}}
	other := router.Connection{ID: "3", Chains: []string{"Quiet"}, Rule: "Match",
		Metadata: router.Metadata{Network: "tcp", Inbound: "DEFAULT-MIXED"}}
	server.trace(router.Connections{Items: []router.Connection{loud, quiet, other}})
	require.Len(t, server.traced, 2)
	server.trace(router.Connections{Items: []router.Connection{loud}})
	server.trace(router.Connections{})
	require.Empty(t, server.traced)
	outbound := strings.Join(server.manager.Log("Loud"), "\n")
	require.Contains(t, outbound, "debug: connection tcp 127.0.0.1:40000 -> a.test:443 "+
		"(192.0.2.10), rule Match, chain Loud")
	require.Contains(t, outbound, "s, sent 2.0 KB, received 3.0 MB: tcp 127.0.0.1:40000")
	require.NotContains(t, strings.Join(server.manager.Log("Quiet"), "\n"), "debug:")
	inbound := strings.Join(server.manager.InboundLog("Team"), "\n")
	require.Contains(t, inbound, "debug: connection tcp 127.0.0.1:40001 -> 192.0.2.11:80, "+
		"rule Match, chain Quiet")
	require.Contains(t, inbound, "debug: closed after ")
	require.Equal(t, "512 B", traceSize(512))
}

func TestStatic_Files(t *testing.T) {
	server := makeServer(t)
	dist := fstest.MapFS{"dist/index.html": {Data: []byte(`<div id="app"></div>`)},
		"dist/favicon.svg":       {Data: []byte("<svg></svg>")},
		"dist/assets/index-1.js": {Data: []byte("console.log(1)")}}
	serve := func(handler http.Handler, path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}
	handler := server.static(dist)
	{
		recorder := serve(handler, "/")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
		require.Contains(t, recorder.Body.String(), "<div id=\"app\">")
	}
	{
		recorder := serve(handler, "/unknown/page")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
		require.Contains(t, recorder.Body.String(), "<div id=\"app\">")
	}
	{
		recorder := serve(handler, "/favicon.svg")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Empty(t, recorder.Header().Get("Cache-Control"))
		require.Contains(t, recorder.Body.String(), "<svg")
	}
	{
		recorder := serve(handler, "/assets/index-1.js")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "public, max-age=31536000, immutable",
			recorder.Header().Get("Cache-Control"))
	}
	{
		recorder := serve(server.mux, "/api/unknown")
		require.Equal(t, http.StatusNotFound, recorder.Code)
		require.Equal(t, http.StatusNotFound, serve(handler, "/api/unknown").Code)
	}
	{
		recorder := serve(server.static(fstest.MapFS{"dist/.gitkeep": {}}), "/")
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Contains(t, recorder.Body.String(), "web UI is not built")
		_, err := fs.Stat(web.Dist, "dist")
		require.NoError(t, err)
	}
}
