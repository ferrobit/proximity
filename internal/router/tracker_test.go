package router

import (
	"path/filepath"
	"testing"

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

func makeCollected() []config.Endpoint {
	return []config.Endpoint{{ID: "A", Enabled: true, Statistics: true},
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
	for index := range maxRefused + 20 {
		tracker.Observe("[TCP] dial A (match Match/) 10.0.0.1:1 --> 10.1." +
			string(rune('0'+index%10)) + "." + string(rune('0'+index/10%10)) +
			string(rune('0'+index/100)) + ":80 error: connection reset by peer")
	}
	_, refused := tracker.Snapshot()
	require.Len(t, refused, maxRefused)
	require.Equal(t, "reset", refused[0].Reason)
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
