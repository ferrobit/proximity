package router

import (
	"encoding/json"
	"errors"
	"fmt"
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

type trackerState struct {
	Traffic map[string]Traffic `json:"traffic"`
	Refused []Refused          `json:"refused"`
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
	dirty     bool
}

const maxRefused = 500
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
		polled: time.Now()}
	state := trackerState{}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &state) == nil {
		for id, traffic := range state.Traffic {
			result.traffic[id] = Traffic{Upload: traffic.Upload, Download: traffic.Download}
		}
		for _, item := range state.Refused {
			result.refused[item.key()] = &item
		}
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

func (tracker *Tracker) SetCollected(endpoints []config.Endpoint) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.collected = map[string]bool{}
	for _, endpoint := range endpoints {
		tracker.collected[endpoint.ID] = endpoint.Statistics && endpoint.Enabled
	}
	for id := range tracker.traffic {
		if _, found := tracker.collected[id]; !found && id != totalKey {
			delete(tracker.traffic, id)
			tracker.dirty = true
		}
	}
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
	if len(tracker.refused) > maxRefused {
		oldest := ""
		for key, current := range tracker.refused {
			if oldest == "" || current.Last < tracker.refused[oldest].Last {
				oldest = key
			}
		}
		delete(tracker.refused, oldest)
	}
}

func (tracker *Tracker) Snapshot() (map[string]Traffic, []Refused) {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
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

func (tracker *Tracker) Clear() {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	tracker.refused, tracker.dirty = map[string]*Refused{}, true
}

func (tracker *Tracker) Save() error {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	if !tracker.dirty {
		return nil
	}
	state := trackerState{Traffic: tracker.traffic, Refused: []Refused{}}
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

func (connection Connection) owner() string {
	if len(connection.Chains) == 0 {
		return unknownOwner
	}
	return Owner(connection.Chains[0])
}

func (refused Refused) key() string {
	return refused.Host + "|" + refused.Port + "|" + refused.Target
}
