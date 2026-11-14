package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckHealth_Status(t *testing.T) {
	code := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
		request *http.Request) {
		require.Equal(t, "/api/session", request.URL.Path)
		writer.WriteHeader(code)
	}))
	defer server.Close()
	require.NoError(t, checkHealth(server.Listener.Addr().String()))
	t.Setenv("PROXIMITY_LISTEN", server.Listener.Addr().String())
	require.NoError(t, run([]string{"health"}))
	{
		code = http.StatusInternalServerError
		require.EqualError(t, checkHealth(server.Listener.Addr().String()),
			"unexpected response status (500)")
	}
	{
		require.ErrorContains(t, checkHealth("no-port"), `cannot parse "no-port" as listen address`)
		require.ErrorContains(t, checkHealth("127.0.0.1:1"), "failed to get session state")
	}
}

func TestLookup_Fallback(t *testing.T) {
	t.Setenv("PROXIMITY_TEST_VALUE", "")
	require.Equal(t, "fallback", lookup("PROXIMITY_TEST_VALUE", "fallback"))
	t.Setenv("PROXIMITY_TEST_VALUE", "value")
	require.Equal(t, "value", lookup("PROXIMITY_TEST_VALUE", "fallback"))
}

func TestRun_Arguments(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "environment.json")
	t.Setenv("route_vpn_gateway", "10.8.0.1")
	require.NoError(t, run([]string{"hook", path}))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"route_vpn_gateway":"10.8.0.1"`)
	{
		require.ErrorContains(t, run([]string{"-unknown"}), "failed to parse arguments")
		blocker := filepath.Join(directory, "file")
		require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
		require.ErrorContains(t, run([]string{"-config", filepath.Join(blocker, "config")}),
			"server.New failed")
	}
}
