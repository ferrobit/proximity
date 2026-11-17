package router

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
)

const dialRefused = `[TCP] dial HOST 1 (match Match/) 127.0.0.1:36874 --> 192.0.2.2:9 error: ` +
	`dial tcp 192.0.2.2:9: connect: connection refused`
const dialProxy = `[TCP] dial Split~https (match IPCIDR/10.0.0.0/8) 127.0.0.1:47770 --> ` +
	`10.99.0.1:8080 error: 10.0.0.1:3128 connect error: dial tcp 10.0.0.1:3128: i/o timeout`
const dialResolve = `time="2026-11-14T17:54:08.719001694Z" level=warning msg="[TCP] dial HOST 1 ` +
	`(match Match/) 127.0.0.1:36878 --> no-such-host.invalid:80 error: dns resolve failed: ` +
	`couldn't find ip"`
const dialPlain = `[UDP] dial OpenVPN 1 10.0.0.5:5000(curl, uid=1000) --> [2001:db8::1]:443 ` +
	`error: connect: no route to host`
const dialLogic = `[TCP] dial Fallback 1 (match AND/((DOMAIN,a.test),(DST-PORT,443))) ` +
	`10.0.0.5:5000 --> a.test error: unexpected failure`

func makeCollected() []config.Outbound {
	return []config.Outbound{{ID: "A", Enabled: true, Statistics: true},
		{ID: "B", Enabled: true, Statistics: true}, {ID: "C", Enabled: true},
		{ID: "T", Enabled: true, Statistics: true}}
}

func TestChainOwner_Lines(t *testing.T) {
	{
		owner, found := chainOwner("[TCP] 127.0.0.1:35728 --> www.example.com:80 match " +
			"DomainSuffix(example.com) using Fallback 1[Agent proxy]")
		require.True(t, found)
		require.Equal(t, "Agent proxy", owner)
	}
	{
		owner, found := chainOwner("[UDP] 10.0.0.1:53 --> 8.8.8.8:53 match Match using HTTP 1~https")
		require.True(t, found)
		require.Equal(t, "HTTP 1", owner)
	}
	{
		_, found := chainOwner(dialRefused)
		require.False(t, found)
		_, found = chainOwner("Start initial configuration using defaults")
		require.False(t, found)
		_, found = chainOwner("[TCP] a --> b using X error: late")
		require.False(t, found)
	}
}

func TestConnection_Describe(t *testing.T) {
	{
		connection := Connection{Chains: []string{"Proxy DE", "Streaming"}, Rule: "DomainSuffix",
			Payload: "youtube.com", Metadata: Metadata{Network: "tcp", Source: "127.0.0.1",
				SourcePort: "41000", Destination: "142.250.1.1", Port: "443",
				Host: "www.youtube.com", Process: "curl"}}
		require.Equal(t, "tcp 127.0.0.1:41000 -> www.youtube.com:443 (142.250.1.1), "+
			"rule DomainSuffix(youtube.com), chain Streaming -> Proxy DE, process curl",
			connection.Describe())
		require.Equal(t, "Proxy DE", connection.Outbound())
	}
	{
		connection := Connection{Chains: []string{"DIRECT"}, Rule: "Match",
			Metadata: Metadata{Network: "udp", Source: "10.77.1.2", SourcePort: "5353",
				Destination: "fd00::1", Port: "53"}}
		require.Equal(t, "udp 10.77.1.2:5353 -> [fd00::1]:53, rule Match, chain DIRECT",
			connection.Describe())
		require.Equal(t, "?", Connection{}.Outbound())
	}
	{
		connection := Connection{Chains: []string{"Corp VPN"}, Rule: "Match",
			Metadata: Metadata{Network: "tcp", Source: "127.0.0.1", SourcePort: "35004",
				Port: "443", Host: "speed.cloudflare.com"}}
		require.Equal(t, "tcp 127.0.0.1:35004 -> speed.cloudflare.com:443, rule Match, chain Corp VPN",
			connection.Describe())
	}
}

func TestHistory_Samples(t *testing.T) {
	tracker := NewTracker("")
	tracker.SetCollected(makeCollected())
	items := []Connection{{ID: "1", Upload: 100, Download: 1000, Chains: []string{"A"}},
		{ID: "2", Upload: 10, Download: 20, Chains: []string{"C"}}}
	tracker.Update(Connections{Items: items, Upload: 110, Download: 1020}, nil)
	require.Empty(t, tracker.History(0))
	tracker.sampled = time.Now().Add(-HistoryStep)
	tracker.Update(Connections{Items: items, Upload: 110, Download: 1020}, nil)
	result := tracker.History(0)
	require.Len(t, result, 1)
	require.InDelta(t, 20, result[0].Outbounds["A"][0], 1)
	require.InDelta(t, 200, result[0].Outbounds["A"][1], 1)
	require.Equal(t, [2]int64{0, 0}, result[0].Outbounds["B"])
	require.NotContains(t, result[0].Outbounds, "C")
	require.InDelta(t, 22, result[0].Total[0], 1)
	require.InDelta(t, 204, result[0].Total[1], 1)
	require.Equal(t, 2, result[0].Connections)
	require.Empty(t, tracker.History(result[0].Time))
	{
		tracker.Rename("A", "Z")
		renamed := tracker.History(0)
		require.Contains(t, renamed[0].Outbounds, "Z")
		require.NotContains(t, renamed[0].Outbounds, "A")
		require.Contains(t, result[0].Outbounds, "A")
	}
	{
		for range maxHistory + 5 {
			tracker.sampled = time.Now().Add(-HistoryStep)
			tracker.Update(Connections{Items: items, Upload: 110, Download: 1020}, nil)
		}
		require.Len(t, tracker.History(0), maxHistory)
	}
}

func TestObserve_DialErrors(t *testing.T) {
	tracker := NewTracker("")
	for _, line := range []string{dialRefused, dialRefused, dialProxy, dialResolve, dialPlain,
		dialLogic, "unrelated line", ""} {
		tracker.Observe(line)
	}
	_, refused := tracker.Snapshot()
	require.Len(t, refused, 5)
	found := map[string]Refused{}
	for _, item := range refused {
		require.NotZero(t, item.First)
		item.First, item.Last, item.source = 0, 0, ""
		found[item.Host] = item
	}
	require.Equal(t, Refused{Host: "192.0.2.2", Port: "9", Target: "HOST 1", Rule: "Match/",
		Reason: "refused", Count: 1}, found["192.0.2.2"])
	require.Equal(t, Refused{Host: "10.99.0.1", Port: "8080", Target: "Split~https",
		Rule: "IPCIDR/10.0.0.0/8", Reason: "timeout", Count: 1}, found["10.99.0.1"])
	require.Equal(t, Refused{Host: "no-such-host.invalid", Port: "80", Target: "HOST 1",
		Rule: "Match/", Reason: "dns", Count: 1}, found["no-such-host.invalid"])
	require.Equal(t, Refused{Host: "2001:db8::1", Port: "443", Target: "OpenVPN 1",
		Reason: "no route", Count: 1}, found["2001:db8::1"])
	require.Equal(t, Refused{Host: "a.test", Target: "Fallback 1",
		Rule: "AND/((DOMAIN,a.test),(DST-PORT,443))", Reason: "error", Count: 1}, found["a.test"])
	{
		tracker.Observe(`[TCP] dial HOST 1 (match Match/) 127.0.0.1:40000 --> 192.0.2.2:9 ` +
			`error: connect: network is unreachable`)
		_, refused := tracker.Snapshot()
		require.Len(t, refused, 5)
		for _, item := range refused {
			if item.Host == "192.0.2.2" {
				require.Equal(t, 2, item.Count)
				require.Equal(t, "unreachable", item.Reason)
			}
		}
	}
	{
		tracker.Clear()
		_, refused := tracker.Snapshot()
		require.Empty(t, refused)
	}
}

func TestObserve_Limit(t *testing.T) {
	tracker := NewTracker("")
	observe := func(count int) {
		for index := range count {
			tracker.Observe("[TCP] dial A (match Match/) 10.0.0.1:1 --> 10.1." +
				string(rune('0'+index%10)) + "." + string(rune('0'+index/10%10)) +
				string(rune('0'+index/100)) + ":80 error: connection reset by peer")
		}
	}
	observe(defaultRefused + 20)
	_, refused := tracker.Snapshot()
	require.Len(t, refused, defaultRefused)
	require.Equal(t, "reset", refused[0].Reason)
	{
		tracker.SetLimits(7, time.Hour)
		_, refused := tracker.Snapshot()
		require.Len(t, refused, 7)
		observe(30)
		_, refused = tracker.Snapshot()
		require.Len(t, refused, 7)
		tracker.SetLimits(0, time.Hour)
		_, refused = tracker.Snapshot()
		require.Empty(t, refused)
	}
	{
		tracker.SetLimits(50, time.Hour)
		observe(3)
		for _, item := range tracker.refused {
			item.Last = time.Now().Add(-2 * time.Hour).Unix()
			break
		}
		_, refused := tracker.Snapshot()
		require.Len(t, refused, 2)
		tracker.SetLimits(50, time.Second)
		for _, item := range tracker.refused {
			item.Last = time.Now().Add(-time.Minute).Unix()
		}
		tracker.dirty = false
		require.EqualError(t, tracker.Save(), "router state path does not exist")
		require.Empty(t, tracker.refused)
	}
}

func TestRename_Traffic(t *testing.T) {
	tracker := NewTracker("")
	tracker.SetCollected(makeCollected())
	items := []Connection{{ID: "1", Upload: 10, Download: 20, Chains: []string{"T"}}}
	tracker.Update(Connections{Items: items, Upload: 10, Download: 20},
		map[string][2]int64{"T": {100, 200}})
	tracker.Update(Connections{Items: items, Upload: 10, Download: 20},
		map[string][2]int64{"T": {150, 260}})
	tracker.Rename("T", "Tunnel")
	tracker.Rename("missing", "other")
	tracker.Rename("A", "A")
	traffic, _ := tracker.Snapshot()
	require.NotContains(t, traffic, "T")
	require.NotContains(t, traffic, "other")
	require.Equal(t, int64(50), traffic["Tunnel"].Upload)
	require.Equal(t, int64(60), traffic["Tunnel"].Download)
	items[0].Chains = []string{"Tunnel"}
	tracker.Update(Connections{Items: items, Upload: 10, Download: 20},
		map[string][2]int64{"Tunnel": {170, 290}})
	traffic, _ = tracker.Snapshot()
	require.Equal(t, int64(70), traffic["Tunnel"].Upload)
	require.Equal(t, int64(90), traffic["Tunnel"].Download)
	require.Equal(t, 1, traffic["Tunnel"].Connections)
}

func TestSave_Reload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	tracker := NewTracker(path)
	require.NoError(t, tracker.Save())
	require.NoFileExists(t, path)
	tracker.SetCollected(makeCollected())
	tracker.Update(Connections{Items: []Connection{{ID: "1", Upload: 10, Download: 20,
		Chains: []string{"A"}}}, Upload: 10, Download: 20}, nil)
	tracker.Observe(dialRefused)
	require.NoError(t, tracker.Save())
	require.FileExists(t, path)
	{
		reloaded := NewTracker(path)
		traffic, refused := reloaded.Snapshot()
		require.Equal(t, Traffic{Upload: 10, Download: 20}, traffic["A"])
		require.Equal(t, Traffic{Upload: 10, Download: 20}, traffic[totalKey])
		require.Len(t, refused, 1)
		require.Equal(t, "192.0.2.2", refused[0].Host)
	}
	{
		broken := NewTracker("")
		broken.Observe(dialRefused)
		require.EqualError(t, broken.Save(), "router state path does not exist")
	}
}

func TestUpdateInbounds_Traffic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	tracker := NewTracker(path)
	tracker.SetInbounds([]config.Inbound{{ID: "S", Enabled: true, Statistics: true},
		{ID: "V", Enabled: true, Statistics: true}, {ID: "Q", Enabled: true},
		{ID: "D", Statistics: true}})
	first := Connection{ID: "1", Upload: 100, Download: 1000, Metadata: Metadata{Inbound: "S"}}
	quiet := Connection{ID: "2", Upload: 5, Download: 5, Metadata: Metadata{Inbound: "Q"}}
	mixed := Connection{ID: "3", Upload: 7, Download: 7, Metadata: Metadata{Inbound: "DEFAULT"}}
	tracker.UpdateInbounds(Connections{Items: []Connection{first, quiet, mixed}},
		map[string][2]int64{"V": {1000, 2000}})
	traffic := tracker.Inbounds()
	require.Equal(t, int64(100), traffic["S"].Upload)
	require.Equal(t, int64(1000), traffic["S"].Download)
	require.Equal(t, 1, traffic["S"].Connections)
	require.Positive(t, traffic["S"].DownloadRate)
	require.Equal(t, Traffic{}, traffic["V"])
	require.NotContains(t, traffic, "Q")
	require.NotContains(t, traffic, "DEFAULT")
	{
		first.Upload, first.Download = 150, 1700
		tracker.UpdateInbounds(Connections{Items: []Connection{first}},
			map[string][2]int64{"V": {1500, 2600}, "Q": {1, 1}})
		traffic := tracker.Inbounds()
		require.Equal(t, int64(150), traffic["S"].Upload)
		require.Equal(t, int64(1700), traffic["S"].Download)
		require.Equal(t, int64(500), traffic["V"].Upload)
		require.Equal(t, int64(600), traffic["V"].Download)
		require.Equal(t, 0, traffic["V"].Connections)
		require.NotContains(t, traffic, "Q")
	}
	{
		tracker.UpdateInbounds(Connections{Items: []Connection{first}},
			map[string][2]int64{"V": {10, 10}, "S": {5000, 5000}})
		traffic := tracker.Inbounds()
		require.Equal(t, int64(500), traffic["V"].Upload)
		require.Equal(t, int64(0), traffic["V"].UploadRate)
		require.Equal(t, int64(150), traffic["S"].Upload)
		require.Equal(t, 1, traffic["S"].Connections)
		tracker.UpdateInbounds(Connections{Items: []Connection{first}},
			map[string][2]int64{"V": {30, 40}, "S": {5010, 5020}})
		traffic = tracker.Inbounds()
		require.Equal(t, int64(520), traffic["V"].Upload)
		require.Equal(t, int64(630), traffic["V"].Download)
		require.Equal(t, int64(160), traffic["S"].Upload)
		require.Equal(t, int64(1720), traffic["S"].Download)
	}
	{
		tracker.RenameInbound("V", "Office")
		tracker.RenameInbound("missing", "other")
		tracker.RenameInbound("S", "S")
		tracker.UpdateInbounds(Connections{}, map[string][2]int64{"Office": {40, 60}})
		traffic := tracker.Inbounds()
		require.NotContains(t, traffic, "V")
		require.NotContains(t, traffic, "other")
		require.Equal(t, int64(530), traffic["Office"].Upload)
		require.Equal(t, int64(650), traffic["Office"].Download)
		require.Equal(t, 0, traffic["S"].Connections)
	}
	{
		require.NoError(t, tracker.Save())
		reloaded := NewTracker(path)
		require.Equal(t, Traffic{Upload: 530, Download: 650}, reloaded.Inbounds()["Office"])
		reloaded.SetInbounds([]config.Inbound{{ID: "S", Enabled: true, Statistics: true}})
		require.NotContains(t, reloaded.Inbounds(), "Office")
		require.Contains(t, reloaded.Inbounds(), "S")
	}
}

func TestUpdate_Traffic(t *testing.T) {
	tracker := NewTracker("")
	tracker.SetCollected(makeCollected())
	tracker.Update(Connections{Items: []Connection{
		{ID: "1", Upload: 100, Download: 1000, Chains: []string{"A", "Fallback 1"}},
		{ID: "2", Upload: 10, Download: 20, Chains: []string{"C"}},
		{ID: "3", Upload: 1, Download: 2}}, Upload: 111, Download: 1022}, nil)
	traffic, _ := tracker.Snapshot()
	require.Equal(t, int64(100), traffic["A"].Upload)
	require.Equal(t, int64(1000), traffic["A"].Download)
	require.Equal(t, 1, traffic["A"].Connections)
	require.Positive(t, traffic["A"].DownloadRate)
	require.NotContains(t, traffic, "C")
	require.NotContains(t, traffic, unknownOwner)
	require.Equal(t, int64(111), traffic[totalKey].Upload)
	require.Equal(t, 3, traffic[totalKey].Connections)
	{
		tracker.Observe("[TCP] 10.0.0.1:1 --> b.test:443 match Match using B")
		tracker.Update(Connections{Items: []Connection{
			{ID: "1", Upload: 150, Download: 1500, Chains: []string{"A", "Fallback 1"}},
			{ID: "3", Upload: 1, Download: 2}}, Upload: 401, Download: 2002}, nil)
		traffic, _ := tracker.Snapshot()
		require.Equal(t, int64(150), traffic["A"].Upload)
		require.Equal(t, int64(1500), traffic["A"].Download)
		require.Equal(t, int64(120), traffic["B"].Upload)
		require.Equal(t, int64(240), traffic["B"].Download)
		require.Equal(t, 0, traffic["B"].Connections)
		require.NotContains(t, traffic, "C")
		require.Equal(t, int64(401), traffic[totalKey].Upload)
		require.Equal(t, int64(2002), traffic[totalKey].Download)
	}
	{
		items := []Connection{{ID: "9", Upload: 5, Download: 5, Chains: []string{"T"}}}
		tracker.Update(Connections{Items: items, Upload: 500, Download: 2100},
			map[string][2]int64{"T": {1000, 2000}})
		traffic, _ := tracker.Snapshot()
		require.Equal(t, int64(0), traffic["T"].Upload)
		require.Equal(t, 1, traffic["T"].Connections)
		tracker.Update(Connections{Items: items, Upload: 500, Download: 2100},
			map[string][2]int64{"T": {1500, 2600}})
		traffic, _ = tracker.Snapshot()
		require.Equal(t, int64(500), traffic["T"].Upload)
		require.Equal(t, int64(600), traffic["T"].Download)
		tracker.Update(Connections{Items: items, Upload: 500, Download: 2100},
			map[string][2]int64{"T": {100, 100}})
		traffic, _ = tracker.Snapshot()
		require.Equal(t, int64(500), traffic["T"].Upload)
		require.Equal(t, int64(600), traffic["T"].Download)
		require.Equal(t, int64(0), traffic["T"].UploadRate)
	}
	{
		tracker.Update(Connections{Upload: 7, Download: 9}, nil)
		traffic, _ := tracker.Snapshot()
		require.Equal(t, int64(507), traffic[totalKey].Upload)
		require.Equal(t, int64(2109), traffic[totalKey].Download)
		require.Equal(t, 0, traffic[totalKey].Connections)
	}
	{
		tracker.SetCollected(makeCollected()[1:])
		traffic, _ := tracker.Snapshot()
		require.NotContains(t, traffic, "A")
		require.Contains(t, traffic, "B")
		require.Contains(t, traffic, totalKey)
	}
}
