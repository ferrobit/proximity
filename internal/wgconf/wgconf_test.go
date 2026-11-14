package wgconf

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

const sampleConfig = `[Interface]
PrivateKey = ` + sampleKey + `
Address = 10.8.1.2/32, fd00::2/128
DNS = 1.1.1.1, example.local, 8.8.8.8
MTU = 1300
Jc = 4 # junk packets
Jmin = 10
H1 = 12345

[Peer]
PublicKey = ` + sampleKey + `
PresharedKey = ` + sampleKey + `
AllowedIPs = 0.0.0.0/0, 10.20.0.0/16
AllowedIPs = 192.168.5.7
Endpoint = vpn.example.com:51820
PersistentKeepalive = 25
`

func makeAmneziaKey(t *testing.T, config string, compressed bool) string {
	last, err := json.Marshal(map[string]string{"config": config})
	require.NoError(t, err)
	document, err := json.Marshal(map[string]any{"dns1": "9.9.9.9", "dns2": "149.112.112.112",
		"containers": []any{map[string]any{"container": "amnezia-awg",
			"awg": map[string]any{"last_config": string(last)}}}})
	require.NoError(t, err)
	if !compressed {
		return amneziaPrefix + base64.RawURLEncoding.EncodeToString(document)
	}
	buffer := bytes.Buffer{}
	require.NoError(t, binary.Write(&buffer, binary.BigEndian, uint32(len(document))))
	writer := zlib.NewWriter(&buffer)
	_, err = writer.Write(document)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return amneziaPrefix + base64.URLEncoding.EncodeToString(buffer.Bytes())
}

func TestImport_AmneziaKey(t *testing.T) {
	template := strings.Replace(sampleConfig, "1.1.1.1, example.local, 8.8.8.8",
		"$PRIMARY_DNS, $SECONDARY_DNS", 1)
	{
		result, err := Import(makeAmneziaKey(t, template, true))
		require.NoError(t, err)
		require.Contains(t, result, "DNS = 9.9.9.9, 149.112.112.112")
		parsed, err := Parse(result)
		require.NoError(t, err)
		require.Equal(t, []string{"9.9.9.9", "149.112.112.112"}, parsed.DNS())
	}
	{
		result, err := Import(makeAmneziaKey(t, template, false))
		require.NoError(t, err)
		require.Contains(t, result, "Endpoint = vpn.example.com:51820")
	}
	{
		_, err := Import("vpn://%%%")
		require.ErrorContains(t, err, "failed to decode base64 data")
	}
	{
		_, err := Import("vpn://" + base64.RawURLEncoding.EncodeToString([]byte("not json")))
		require.ErrorContains(t, err, "failed to decode JSON document")
	}
	{
		_, err := Import("vpn://" + base64.RawURLEncoding.EncodeToString([]byte(`{"containers":[]}`)))
		require.ErrorContains(t, err, "AmneziaWG configuration does not exist")
	}
	{
		_, err := Import(makeAmneziaKey(t, "[Interface]\n", true))
		require.ErrorContains(t, err, "failed to import Amnezia key")
	}
}

func TestImport_Plain(t *testing.T) {
	{
		result, err := Import("\n" + sampleConfig + "\n\n")
		require.NoError(t, err)
		require.Equal(t, strings.TrimSpace(sampleConfig)+"\n", result)
	}
	{
		_, err := Import("hello")
		require.ErrorContains(t, err, "failed to import configuration")
	}
}

func TestParse_Errors(t *testing.T) {
	peer := "[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = host:1\n"
	head := "[Interface]\nPrivateKey = " + sampleKey + "\n"
	for _, item := range [][2]string{
		{"[Wrong]\n", "unexpected section 'wrong' at line 1"},
		{"Key = 1\n", "unexpected key outside of section at line 1"},
		{"[Interface]\ngarbage\n", "unexpected text at line 2"},
		{peer, "invalid number of [Interface] sections (0)"},
		{head + "Address = 10.0.0.2\n", "[Peer] section does not exist"},
		{"[Interface]\nAddress = 10.0.0.2\n" + peer, "failed to check PrivateKey"},
		{"[Interface]\nPrivateKey = ???\nAddress = 10.0.0.2\n" + peer, "failed to decode base64 key"},
		{"[Interface]\nPrivateKey = AAAA\nAddress = 10.0.0.2\n" + peer, "invalid key length (3)"},
		{head + peer, "interface Address does not exist"},
		{head + "Address = 10.0.0.300\n" + peer, "failed to check Address '10.0.0.300'"},
		{head + "Address = 10.0.0.2\n" + peer + "AllowedIPs = x\n", "failed to check AllowedIPs 'x'"},
		{head + "Address = 10.0.0.2\n[Peer]\nPublicKey = " + sampleKey + "\n",
			"peer Endpoint does not exist"},
		{head + "Address = 10.0.0.2\n[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = host\n",
			"failed to check Endpoint 'host'"},
		{head + "Address = 10.0.0.2\n[Peer]\nEndpoint = host:1\n", "failed to check PublicKey"},
	} {
		_, err := Parse(item[0])
		require.ErrorContains(t, err, item[1])
		require.ErrorContains(t, err, "wgconf.Parse failed")
	}
}

func TestParse_Valid(t *testing.T) {
	parsed, err := Parse(sampleConfig)
	require.NoError(t, err)
	require.Equal(t, []string{"10.8.1.2/32", "fd00::2/128"}, parsed.Addresses())
	require.Equal(t, []string{"1.1.1.1", "8.8.8.8"}, parsed.DNS())
	require.Equal(t, 1300, parsed.MTU())
	require.Equal(t, []string{"0.0.0.0/0", "10.20.0.0/16", "192.168.5.7"}, parsed.AllowedIPs())
	{
		parsed, err := Parse(strings.Replace(sampleConfig, "MTU = 1300\n", "", 1))
		require.NoError(t, err)
		require.Equal(t, 0, parsed.MTU())
	}
}

func TestStrip_QuickKeys(t *testing.T) {
	parsed, err := Parse(sampleConfig)
	require.NoError(t, err)
	require.Equal(t, "[Interface]\nPrivateKey = "+sampleKey+"\nJc = 4\nJmin = 10\nH1 = 12345\n"+
		"[Peer]\nPublicKey = "+sampleKey+"\nPresharedKey = "+sampleKey+
		"\nAllowedIPs = 0.0.0.0/0, 10.20.0.0/16\nAllowedIPs = 192.168.5.7\n"+
		"Endpoint = vpn.example.com:51820\nPersistentKeepalive = 25\n", parsed.Strip())
}
