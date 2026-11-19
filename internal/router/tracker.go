package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"proximity/internal/config"
)

type Traffic struct {
	Upload       int64 `json:"upload"`
	Download     int64 `json:"download"`
	UploadRate   int64 `json:"upload_rate"`
	DownloadRate int64 `json:"download_rate"`
	Connections  int   `json:"connections"`
}

type Refused struct {
	Host   string `json:"host"`
	Port   string `json:"port"`
	Target string `json:"target"`
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	First  int64  `json:"first"`
	Last   int64  `json:"last"`
	source string
}

type Sample struct {
	Time        int64               `json:"time"`
	Total       [2]int64            `json:"total"`
	Connections int                 `json:"connections"`
	Outbounds   map[string][2]int64 `json:"outbounds"`
	Hits        map[string]int64    `json:"hits,omitempty"`
}

type Hit struct {
	Count int64 `json:"count"`
	Last  int64 `json:"last"`
}

type trackerState struct {
	Traffic  map[string]Traffic `json:"traffic"`
	Refused  []Refused          `json:"refused"`
	Inbounds map[string]Traffic `json:"inbounds,omitempty"`
	Hits     map[string]Hit     `json:"hits,omitempty"`
}

type Tracker struct {
	mutex     sync.Mutex
	path      string
	traffic   map[string]Traffic
	seen      map[string]Connection
	totals    Connections
	counters  map[string][2]int64
	started   map[string]int
	refused   map[string]*Refused
	collected map[string]bool
	polled    time.Time
	history   []Sample
	bucket    map[string][2]int64
	sampled   time.Time
	dirty     bool
	limit     int
	lifetime  time.Duration
	inbounds  map[string]Traffic
	listening map[string]bool
	entered   map[string]Connection
	gauges    map[string][2]int64
	visited   time.Time
	hits      map[string]Hit
	matched   []int64
	session   int
	struck    map[string]int64
}

const defaultRefused = 100
const defaultLifetime = 24 * time.Hour
const maxHistory = 720
const HistoryStep = 5 * time.Second
const totalKey = ""
const unknownOwner = "?"
const usingMarker = " using "
const arrowMarker = " --> "
const errorMarker = " error: "

var dialPattern = regexp.MustCompile(
	`\[(?:TCP|UDP)\] dial (.+?)(?: \(match (.*?)\))? (\S+?(?:\(.*?\))?) --> (\S+) error: (.*?)"?$`)
var reasons = []struct {
	text   string
	reason string
}{{"connection refused", "refused"}, {"i/o timeout", "timeout"},
	{"connection timed out", "timeout"}, {"deadline exceeded", "timeout"},
	{"no route to host", "no route"}, {"network is unreachable", "unreachable"},
	{"host is unreachable", "unreachable"}, {"connection reset", "reset"},
	{"dns resolve failed", "dns"}, {"couldn't find ip", "dns"}}

func NewTracker(path string) *Tracker {
	result := &Tracker{path: path, traffic: map[string]Traffic{}, seen: map[string]Connection{},
		refused: map[string]*Refused{}, collected: map[string]bool{}, started: map[string]int{},
		polled: time.Now(), bucket: map[string][2]int64{}, sampled: time.Now(),
		limit: defaultRefused, lifetime: defaultLifetime, inbounds: map[string]Traffic{},
		listening: map[string]bool{}, entered: map[string]Connection{}, visited: time.Now(),
		hits: map[string]Hit{}, struck: map[string]int64{}}
	state := trackerState{}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &state) == nil {
		for id, traffic := range state.Traffic {
			result.traffic[id] = Traffic{Upload: traffic.Upload, Download: traffic.Download}
		}
		for id, traffic := range state.Inbounds {
			result.inbounds[id] = Traffic{Upload: traffic.Upload, Download: traffic.Download}
		}
		for _, item := range state.Refused {
			result.refused[item.key()] = &item
		}
		maps.Copy(result.hits, state.Hits)
	}
	return result
}

func chainOwner(line string) (string, bool) {
	offset := strings.LastIndex(line, usingMarker)
	if offset < 0 || !strings.Contains(line[:offset], arrowMarker) ||
		strings.Contains(line, errorMarker) {
		return "", false
	}
	chain := line[offset+len(usingMarker):]
	if start := strings.LastIndex(chain, "["); start >= 0 && strings.HasSuffix(chain, "]") {
		chain = chain[start+1 : len(chain)-1]
	}
	return Owner(chain), true
}

func classify(text string) string {
	for _, item := range reasons {
		if strings.Contains(text, item.text) {
			return item.reason
		}
	}
	return "error"
}

func rateOf(bytes [2]int64, seconds float64) [2]int64 {
	return [2]int64{int64(float64(bytes[0]) / seconds), int64(float64(bytes[1]) / seconds)}
}

func (tracker *Tracker) SetCollected(outbounds []config.Outbound) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.collected = map[string]bool{}
	for _, outbound := range outbounds {
		tracker.collected[outbound.ID] = outbound.Statistics && outbound.Enabled
	}
	for id := range tracker.traffic {
		if _, found := tracker.collected[id]; !found && id != totalKey {
			delete(tracker.traffic, id)
			tracker.dirty = true
		}
	}
}

func (tracker *Tracker) SetLimits(limit int, lifetime time.Duration) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.limit, tracker.lifetime = limit, lifetime
	tracker.prune(time.Now())
}

func (tracker *Tracker) SetInbounds(inbounds []config.Inbound) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.listening = map[string]bool{}
	for _, inbound := range inbounds {
		tracker.listening[inbound.ID] = inbound.Statistics && inbound.Enabled
	}
	for id := range tracker.inbounds {
		if _, found := tracker.listening[id]; !found {
			delete(tracker.inbounds, id)
			tracker.dirty = true
		}
	}
}

func (tracker *Tracker) RenameInbound(previous string, current string) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if traffic, found := tracker.inbounds[previous]; found && previous != current {
		tracker.inbounds[current] = traffic
		delete(tracker.inbounds, previous)
		tracker.dirty = true
	}
	if listening, found := tracker.listening[previous]; found && previous != current {
		tracker.listening[current] = listening
		delete(tracker.listening, previous)
	}
	if gauge, found := tracker.gauges[previous]; found && previous != current {
		tracker.gauges[current] = gauge
		delete(tracker.gauges, previous)
	}
}

func (tracker *Tracker) UpdateInbounds(connections Connections, gauges map[string][2]int64) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	now := time.Now()
	elapsed := max(now.Sub(tracker.visited).Seconds(), 0.1)
	tracker.visited = now
	deltas, entered := map[string]Traffic{}, map[string]Connection{}
	for _, connection := range connections.Items {
		name := connection.Metadata.Inbound
		if !tracker.listening[name] {
			continue
		}
		previous := tracker.entered[connection.ID]
		entered[connection.ID] = connection
		delta := deltas[name]
		delta.Upload += max(connection.Upload-previous.Upload, 0)
		delta.Download += max(connection.Download-previous.Download, 0)
		delta.Connections++
		deltas[name] = delta
	}
	for id, gauge := range gauges {
		previous, found := tracker.gauges[id]
		delta := deltas[id]
		delta.Upload, delta.Download = 0, 0
		if found && gauge[0] >= previous[0] && gauge[1] >= previous[1] {
			delta.Upload, delta.Download = gauge[0]-previous[0], gauge[1]-previous[1]
		}
		deltas[id] = delta
	}
	tracker.gauges, tracker.entered = gauges, entered
	for id, traffic := range tracker.inbounds {
		traffic.UploadRate, traffic.DownloadRate, traffic.Connections = 0, 0, 0
		tracker.inbounds[id] = traffic
	}
	for id, delta := range deltas {
		if !tracker.listening[id] {
			continue
		}
		traffic := tracker.inbounds[id]
		traffic.Upload, traffic.Download = traffic.Upload+delta.Upload,
			traffic.Download+delta.Download
		traffic.UploadRate = int64(float64(delta.Upload) / elapsed)
		traffic.DownloadRate = int64(float64(delta.Download) / elapsed)
		traffic.Connections = delta.Connections
		tracker.inbounds[id] = traffic
		tracker.dirty = tracker.dirty || delta.Upload != 0 || delta.Download != 0
	}
}

func (tracker *Tracker) Inbounds() map[string]Traffic {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	return maps.Clone(tracker.inbounds)
}

func (tracker *Tracker) Rename(previous string, current string) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if traffic, found := tracker.traffic[previous]; found && previous != current {
		tracker.traffic[current] = traffic
		delete(tracker.traffic, previous)
		tracker.dirty = true
	}
	if collected, found := tracker.collected[previous]; found && previous != current {
		tracker.collected[current] = collected
		delete(tracker.collected, previous)
	}
	if counter, found := tracker.counters[previous]; found && previous != current {
		tracker.counters[current] = counter
		delete(tracker.counters, previous)
	}
	if bytes, found := tracker.bucket[previous]; found && previous != current {
		tracker.bucket[current] = bytes
		delete(tracker.bucket, previous)
	}
	for index, sample := range tracker.history {
		if rate, found := sample.Outbounds[previous]; found && previous != current {
			outbounds := maps.Clone(sample.Outbounds)
			delete(outbounds, previous)
			outbounds[current] = rate
			tracker.history[index].Outbounds = outbounds
		}
	}
}

func (tracker *Tracker) Update(connections Connections, counters map[string][2]int64) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	now := time.Now()
	elapsed := max(now.Sub(tracker.polled).Seconds(), 0.1)
	tracker.polled = now
	deltas, seen, weights := map[string]Traffic{}, map[string]Connection{}, map[string]int{}
	alive, started := Traffic{}, tracker.started
	tracker.started = map[string]int{}
	for _, connection := range connections.Items {
		previous, known := tracker.seen[connection.ID]
		seen[connection.ID] = connection
		owner := connection.owner()
		if !known {
			started[owner]--
		}
		delta := deltas[owner]
		upload, download := max(connection.Upload-previous.Upload, 0),
			max(connection.Download-previous.Download, 0)
		alive.Upload, alive.Download = alive.Upload+upload, alive.Download+download
		delta.Upload, delta.Download = delta.Upload+upload, delta.Download+download
		delta.Connections++
		deltas[owner] = delta
	}
	for id, connection := range tracker.seen {
		if _, found := seen[id]; !found {
			weights[connection.owner()]++
		}
	}
	for owner, count := range started {
		weights[owner] += max(count, 0)
	}
	total := Traffic{Upload: connections.Upload - tracker.totals.Upload,
		Download: connections.Download - tracker.totals.Download, Connections: len(seen)}
	if total.Upload < 0 || total.Download < 0 {
		total.Upload, total.Download = connections.Upload, connections.Download
	}
	tracker.share(deltas, weights, max(total.Upload-alive.Upload, 0),
		max(total.Download-alive.Download, 0))
	for id, counter := range counters {
		previous, found := tracker.counters[id]
		delta := deltas[id]
		delta.Upload, delta.Download = 0, 0
		if found && counter[0] >= previous[0] && counter[1] >= previous[1] {
			delta.Upload, delta.Download = counter[0]-previous[0], counter[1]-previous[1]
		}
		deltas[id] = delta
	}
	deltas[totalKey] = total
	tracker.counters, tracker.seen = counters, seen
	tracker.totals = Connections{Upload: connections.Upload, Download: connections.Download}
	for id, traffic := range tracker.traffic {
		traffic.UploadRate, traffic.DownloadRate, traffic.Connections = 0, 0, 0
		tracker.traffic[id] = traffic
	}
	for id, delta := range deltas {
		if id != totalKey && !tracker.collected[id] {
			continue
		}
		traffic := tracker.traffic[id]
		traffic.Upload, traffic.Download = traffic.Upload+delta.Upload,
			traffic.Download+delta.Download
		traffic.UploadRate = int64(float64(delta.Upload) / elapsed)
		traffic.DownloadRate = int64(float64(delta.Download) / elapsed)
		traffic.Connections = delta.Connections
		tracker.traffic[id] = traffic
		tracker.dirty = tracker.dirty || delta.Upload != 0 || delta.Download != 0
		bytes := tracker.bucket[id]
		tracker.bucket[id] = [2]int64{bytes[0] + delta.Upload, bytes[1] + delta.Download}
	}
	if passed := now.Sub(tracker.sampled); passed >= HistoryStep {
		tracker.sample(now, passed.Seconds())
	}
}

func (tracker *Tracker) Observe(line string) {
	if owner, found := chainOwner(line); found {
		tracker.mutex.Lock()
		defer tracker.mutex.Unlock()
		tracker.started[owner]++
		return
	}
	match := dialPattern.FindStringSubmatch(line)
	if match == nil {
		return
	}
	host, port, err := net.SplitHostPort(match[4])
	if err != nil {
		host, port = match[4], ""
	}
	item := Refused{Host: host, Port: port, Target: match[1], Rule: match[2],
		Reason: classify(match[5]), Count: 1, First: time.Now().Unix(), Last: time.Now().Unix(),
		source: match[3]}
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.dirty = true
	if current, found := tracker.refused[item.key()]; found {
		if current.Last, current.Reason = item.Last, item.Reason; current.source != item.source {
			current.Count++
			current.source = item.source
		}
		return
	}
	tracker.refused[item.key()] = &item
	tracker.prune(time.Now())
}

// NOTE: mihomo counts hits per rule since its last reload or start (session). Origins map the
// rule index to its route; a counter below the previous one means mihomo started counting again.
func (tracker *Tracker) UpdateHits(session int, origins []string, rules []Rule) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if session != tracker.session {
		tracker.matched, tracker.session = nil, session
	}
	if len(rules) != len(origins) {
		tracker.matched = nil
		return
	}
	matched := make([]int64, len(rules))
	for index, rule := range rules {
		count, delta := rule.Extra.HitCount, rule.Extra.HitCount
		if matched[index] = count; len(tracker.matched) == len(rules) &&
			count >= tracker.matched[index] {
			delta = count - tracker.matched[index]
		}
		origin := origins[index]
		if delta <= 0 || origin == "" {
			continue
		}
		hit := tracker.hits[origin]
		hit.Count += delta
		if last := rule.Extra.HitAt.Unix(); last > hit.Last {
			hit.Last = last
		}
		tracker.hits[origin], tracker.dirty = hit, true
		tracker.struck[origin] += delta
	}
	tracker.matched = matched
}

func (tracker *Tracker) Hits() map[string]Hit {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	return maps.Clone(tracker.hits)
}

func (tracker *Tracker) KeepHits(known map[string]bool) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	for origin := range tracker.hits {
		if !known[origin] {
			delete(tracker.hits, origin)
			tracker.dirty = true
		}
	}
}

func (tracker *Tracker) ClearHits() {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.hits, tracker.dirty = map[string]Hit{}, true
	for index := range tracker.history {
		tracker.history[index].Hits = nil
	}
}

func (tracker *Tracker) Snapshot() (map[string]Traffic, []Refused) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.prune(time.Now())
	traffic, refused := map[string]Traffic{}, []Refused{}
	for id, item := range tracker.traffic {
		traffic[id] = item
	}
	for _, item := range tracker.refused {
		refused = append(refused, *item)
	}
	slices.SortFunc(refused, func(first Refused, second Refused) int {
		if first.Last != second.Last {
			return int(second.Last - first.Last)
		}
		return strings.Compare(first.key(), second.key())
	})
	return traffic, refused
}

func (tracker *Tracker) History(since int64) []Sample {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	result := []Sample{}
	for _, sample := range tracker.history {
		if sample.Time > since {
			result = append(result, sample)
		}
	}
	return result
}

func (tracker *Tracker) Clear() {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.refused, tracker.dirty = map[string]*Refused{}, true
}

func (tracker *Tracker) Save() error {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if tracker.prune(time.Now()); !tracker.dirty {
		return nil
	}
	state := trackerState{Traffic: tracker.traffic, Refused: []Refused{},
		Inbounds: tracker.inbounds, Hits: tracker.hits}
	for _, item := range tracker.refused {
		state.Refused = append(state.Refused, *item)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode router state: %w", err)
	} else if tracker.path == "" {
		return errors.New("router state path does not exist")
	} else if err = config.WriteFile(tracker.path, data); err != nil {
		return err
	}
	tracker.dirty = false
	return nil
}

func (tracker *Tracker) prune(now time.Time) {
	deadline := now.Add(-tracker.lifetime).Unix()
	for key, item := range tracker.refused {
		if item.Last < deadline {
			delete(tracker.refused, key)
			tracker.dirty = true
		}
	}
	for len(tracker.refused) > max(tracker.limit, 0) {
		oldest := ""
		for key, item := range tracker.refused {
			if oldest == "" || item.Last < tracker.refused[oldest].Last || (item.Last ==
				tracker.refused[oldest].Last && key < oldest) {
				oldest = key
			}
		}
		delete(tracker.refused, oldest)
		tracker.dirty = true
	}
}

func (tracker *Tracker) sample(now time.Time, seconds float64) {
	result := Sample{Time: now.Unix(), Connections: tracker.traffic[totalKey].Connections,
		Outbounds: map[string][2]int64{}}
	for id, collected := range tracker.collected {
		if collected {
			result.Outbounds[id] = rateOf(tracker.bucket[id], seconds)
		}
	}
	result.Total = rateOf(tracker.bucket[totalKey], seconds)
	if len(tracker.struck) != 0 {
		result.Hits, tracker.struck = tracker.struck, map[string]int64{}
	}
	if tracker.history = append(tracker.history, result); len(tracker.history) > maxHistory {
		tracker.history = tracker.history[len(tracker.history)-maxHistory:]
	}
	tracker.bucket, tracker.sampled = map[string][2]int64{}, now
}

func (tracker *Tracker) share(deltas map[string]Traffic, weights map[string]int, upload int64,
	download int64) {
	sum := 0
	for _, count := range weights {
		sum += count
	}
	for owner, count := range weights {
		if sum == 0 {
			break
		}
		delta := deltas[owner]
		delta.Upload += upload * int64(count) / int64(sum)
		delta.Download += download * int64(count) / int64(sum)
		deltas[owner] = delta
	}
}

func (connection Connection) Outbound() string {
	return connection.owner()
}

func (connection Connection) Describe() string {
	metadata := connection.Metadata
	target := net.JoinHostPort(metadata.Destination, metadata.Port)
	if metadata.Host != "" {
		if target = net.JoinHostPort(metadata.Host, metadata.Port); metadata.Destination != "" {
			target += " (" + metadata.Destination + ")"
		}
	}
	rule := connection.Rule
	if connection.Payload != "" {
		rule += "(" + connection.Payload + ")"
	}
	chain := slices.Clone(connection.Chains)
	slices.Reverse(chain)
	result := fmt.Sprintf("%v %v -> %v, rule %v, chain %v", metadata.Network,
		net.JoinHostPort(metadata.Source, metadata.SourcePort), target, rule,
		strings.Join(chain, " -> "))
	if metadata.Process != "" {
		result += ", process " + metadata.Process
	}
	return result
}

func (connection Connection) owner() string {
	if len(connection.Chains) == 0 {
		return unknownOwner
	}
	return Owner(connection.Chains[0])
}

func (refused Refused) key() string {
	return refused.Host + "|" + refused.Port + "|" + refused.Target
}
