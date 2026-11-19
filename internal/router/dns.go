package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/rules"
)

type dnsSection struct {
	Enable       bool       `json:"enable"`
	IPv6         bool       `json:"ipv6"`
	EnhancedMode string     `json:"enhanced-mode,omitempty"`
	FakeIPRange  string     `json:"fake-ip-range,omitempty"`
	Bootstrap    []string   `json:"default-nameserver"`
	Nameserver   []string   `json:"nameserver"`
	Policy       policyList `json:"nameserver-policy"`
}

type policyItem struct {
	Key     string
	Servers []string
}

type policyList []policyItem

type dnsExit struct {
	name    string
	owner   string
	carrier string
	needs   []string
}

type dnsUpstream struct {
	text    string
	needs   []string
	address bool
}

type dnsEntry struct {
	key       string
	domain    string
	suffix    bool
	matcher   bool
	routed    bool
	upstreams []dnsUpstream
}

type dnsOwner struct {
	id    string
	hosts []string
}

type ruleProvider struct {
	Type     string   `json:"type"`
	Behavior string   `json:"behavior"`
	Payload  []string `json:"payload"`
}

const exitRules = "RULES"
const matcherPrefix = "geosite:"
const suffixPrefix = "+."
const ruleSetPrefix = "rule-set:"
const dnsSetPrefix = "proximity-dns-"

// NOTE: The servers that answer for the domains routed to an outbound, as mihomo gets them:
// "server#exit".
func Resolvers(snapshot config.Config, links map[string]link.Status) map[string][]string {
	state := &buildState{config: snapshot, links: links, variants: map[string][]rules.Variant{},
		active: map[string]bool{config.TargetDirect: true, config.TargetReject: true},
		cache:  map[string][]dnsUpstream{}}
	state.buildProxies()
	state.buildGroups()
	result := map[string][]string{}
	for _, outbound := range snapshot.Outbounds {
		if texts := serverTexts(state.resolvers(outbound.ID)); len(texts) != 0 {
			result[outbound.ID] = texts
		}
	}
	return result
}

// NOTE: The DNS servers given by name that the router can resolve for real through the servers
// of the same outbound given by address (see named): the server resolves them and passes
// the answers back as hosts.
func ServerNames(snapshot config.Config, links map[string]link.Status) []string {
	state := &buildState{config: snapshot, links: links, variants: map[string][]rules.Variant{},
		active: map[string]bool{config.TargetDirect: true, config.TargetReject: true},
		cache:  map[string][]dnsUpstream{}}
	state.buildProxies()
	state.buildGroups()
	result := []string{}
	for _, entry := range state.named() {
		result = append(result, entry.domain)
	}
	return result
}

func makeEntry(rule rules.Rule, upstreams []dnsUpstream) (dnsEntry, bool) {
	switch rule.Directive {
	case rules.Domain:
		return dnsEntry{key: rule.Value, domain: rule.Value, routed: true, upstreams: upstreams}, true
	case rules.DomainSuffix:
		return dnsEntry{key: suffixPrefix + rule.Value, domain: rule.Value, suffix: true,
			routed: true, upstreams: upstreams}, true
	case rules.GeoSite:
		return dnsEntry{key: matcherPrefix + rule.Value, matcher: true, routed: true,
			upstreams: upstreams}, true
	default:
		return dnsEntry{}, false
	}
}

func parents(domain string) []string {
	result := []string{domain}
	for {
		_, rest, found := strings.Cut(domain, ".")
		if !found {
			return result
		}
		result, domain = append(result, rest), rest
	}
}

func serverTexts(upstreams []dnsUpstream) []string {
	result := []string{}
	for _, upstream := range upstreams {
		if !slices.Contains(result, upstream.text) {
			result = append(result, upstream.text)
		}
	}
	return result
}

func signature(upstreams []dnsUpstream) string {
	return strings.Join(serverTexts(upstreams), "\n")
}

func (list policyList) MarshalJSON() ([]byte, error) {
	buffer := bytes.Buffer{}
	buffer.WriteByte('{')
	for index, item := range list {
		key, err := json.Marshal(item.Key)
		if err != nil {
			return nil, fmt.Errorf("failed to encode policy key '%v': %w", item.Key, err)
		}
		servers, err := json.Marshal(item.Servers)
		if err != nil {
			return nil, fmt.Errorf("failed to encode policy servers of '%v': %w", item.Key, err)
		}
		if index != 0 {
			buffer.WriteByte(',')
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		buffer.Write(servers)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func (list *policyList) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("failed to decode policy: %w", err)
	}
	result := policyList{}
	for decoder.More() {
		item := policyItem{}
		if key, err := decoder.Token(); err != nil {
			return fmt.Errorf("failed to decode policy key: %w", err)
		} else if err = decoder.Decode(&item.Servers); err != nil {
			return fmt.Errorf("failed to decode policy servers of '%v': %w", key, err)
		} else {
			item.Key = fmt.Sprint(key)
		}
		result = append(result, item)
	}
	*list = result
	return nil
}

func (entry dnsEntry) matches(name string) bool {
	if entry.matcher {
		return false
	} else if !entry.suffix {
		return entry.domain == name
	}
	return slices.Contains(parents(name), entry.domain)
}

func (state *buildState) exit(id string) (dnsExit, bool) {
	if id == config.ViaHost {
		return dnsExit{carrier: config.CarrierUDP}, true
	} else if id == config.ViaRouter {
		return dnsExit{name: exitRules, carrier: config.CarrierUDP}, true
	} else if !state.active[id] {
		return dnsExit{}, false
	} else if outbound, found := state.config.FindOutbound(id); found {
		return dnsExit{name: id, owner: id, carrier: outbound.Carrier(), needs: []string{id}}, true
	}
	result := dnsExit{name: id, owner: id, carrier: config.CarrierUDP, needs: state.members(id)}
	for _, member := range result.needs {
		if outbound, found := state.config.FindOutbound(member); found &&
			outbound.Carrier() == config.CarrierTCP {
			result.carrier = config.CarrierTCP
		}
	}
	return result, len(result.needs) != 0
}

func (state *buildState) members(id string) []string {
	result := []string{}
	for _, fallback := range state.config.Fallbacks {
		for _, member := range fallback.Outbounds {
			if fallback.ID == id && state.active[member] {
				result = append(result, member)
			}
		}
	}
	return result
}

// NOTE: mihomo does not switch a plain server to TCP itself: a query over UDP through an
// outbound without UDP fails, so the transport is chosen here.
func (state *buildState) upstreams(servers []string, exit string, label string) []dnsUpstream {
	result := []dnsUpstream{}
	target, found := state.exit(exit)
	if !found && len(servers) != 0 {
		state.warnf("DNS servers of %v are skipped: exit '%v' is disabled or does not exist", label,
			exit)
	}
	for _, server := range servers {
		parsed, err := config.ParseDNSServer(server)
		if !found {
			continue
		} else if err != nil {
			state.warnf("DNS server '%v' of %v is skipped: %v", server, label, err)
			continue
		}
		scheme, name := parsed.Scheme, target.name
		if target.carrier == config.CarrierTCP && parsed.Plain {
			scheme = config.DNSTCP
		} else if target.carrier == config.CarrierTCP &&
			(scheme == config.DNSUDP || scheme == config.DNSQUIC) {
			state.warnf("DNS server '%v' of %v is skipped: '%v' does not carry UDP", server, label,
				exit)
			continue
		}
		for _, variant := range state.variants[target.owner] {
			if variant.Port == parsed.Port {
				name = variant.Target
			}
		}
		text := parsed.URL(scheme)
		if scheme == config.DNSUDP {
			text = strings.TrimPrefix(text, config.DNSUDP+"://")
		}
		if name != "" {
			text += "#" + name
		}
		result = append(result, dnsUpstream{text: text, needs: target.needs,
			address: parsed.Named() == ""})
	}
	return result
}

func (state *buildState) own(outbound config.Outbound) []dnsUpstream {
	label, exit, result := "outbound '"+outbound.ID+"'", outbound.DNS.Exit, []dnsUpstream{}
	if exit == "" {
		exit = outbound.ID
	}
	if outbound.UsesDNS() {
		result = state.upstreams(state.links[outbound.ID].DNS, outbound.ID, label)
	}
	return append(result, state.upstreams(outbound.DNS.Servers, exit, label)...)
}

// NOTE: A fallback has no servers of its own: its domains are asked where its first working
// member asks, an empty answer means the default servers.
func (state *buildState) resolvers(target string) []dnsUpstream {
	if result, found := state.cache[target]; found {
		return result
	}
	result := []dnsUpstream{}
	if outbound, found := state.config.FindOutbound(target); found && state.active[target] {
		result = state.own(outbound)
	} else if members := state.alive(state.members(target)); len(members) != 0 {
		result = state.resolvers(members[0])
	}
	state.cache[target] = result
	return result
}

func (state *buildState) global() []dnsUpstream {
	settings := state.config.Settings.DNS
	if result := state.upstreams(settings.Servers, settings.Exit, "settings"); len(result) != 0 {
		return result
	}
	return []dnsUpstream{{text: config.DNSSystem, address: true}}
}

// NOTE: A keyword, wildcard or regex route cannot be a key of nameserver-policy: its patterns
// go into an inline rule set of the route, and the set is the key.
func (state *buildState) noteDomains(target string, parsed []rules.Rule) {
	patterns, upstreams := []string{}, state.resolvers(target)
	for _, rule := range parsed {
		if entry, found := makeEntry(rule, upstreams); found {
			state.entries = append(state.entries, entry)
		} else if rule.Directive == rules.DomainKeyword || rule.Directive == rules.DomainWildcard ||
			rule.Directive == rules.DomainRegex {
			patterns = append(patterns, rule.Directive+","+rule.Value)
		}
	}
	if len(patterns) == 0 {
		return
	}
	name := dnsSetPrefix + strconv.Itoa(len(state.providers)+1)
	state.providers[name] = ruleProvider{Type: "inline", Behavior: "classical", Payload: patterns}
	state.entries = append(state.entries, dnsEntry{key: ruleSetPrefix + name, matcher: true,
		routed: true, upstreams: upstreams})
}

// NOTE: A DNS server of an outbound given by name is resolved by the default servers, which
// may not know it: the name is asked from the servers of the same outbound given by address.
func (state *buildState) named() []dnsEntry {
	result := []dnsEntry{}
	add := func(servers []string, upstreams []dnsUpstream) {
		addresses := []dnsUpstream{}
		for _, upstream := range upstreams {
			if upstream.address {
				addresses = append(addresses, upstream)
			}
		}
		for _, text := range servers {
			parsed, err := config.ParseDNSServer(text)
			if name := parsed.Named(); err == nil && name != "" && len(addresses) != 0 &&
				!slices.ContainsFunc(result, func(entry dnsEntry) bool { return entry.domain == name }) {
				result = append(result, dnsEntry{key: name, domain: name, upstreams: addresses})
			}
		}
	}
	for _, outbound := range state.config.Outbounds {
		if state.active[outbound.ID] {
			add(outbound.DNS.Servers, state.resolvers(outbound.ID))
		}
	}
	return result
}

// NOTE: The check target of a tunnel and the health check of a fallback leave through the
// tunnel, so their names are asked from its servers, not from those of their routes.
func (state *buildState) checks() []dnsEntry {
	result := []dnsEntry{}
	add := func(host string, upstreams []dnsUpstream) {
		if name := config.HostName(host); name != "" && len(upstreams) != 0 &&
			!slices.ContainsFunc(result, func(entry dnsEntry) bool { return entry.domain == name }) {
			result = append(result, dnsEntry{key: name, domain: name, upstreams: upstreams})
		}
	}
	for _, outbound := range state.config.Outbounds {
		if outbound.Type != config.TypeAmneziaWG || outbound.Monitoring == nil ||
			!state.active[outbound.ID] {
			continue
		} else if host, _, err := net.SplitHostPort(outbound.Monitoring.Target); err == nil {
			add(host, state.resolvers(outbound.ID))
		}
	}
	for _, fallback := range state.config.Fallbacks {
		if address, err := url.Parse(fallback.URL); err == nil && fallback.HealthCheck &&
			state.active[fallback.ID] {
			add(address.Hostname(), state.resolvers(fallback.ID))
		}
	}
	return result
}

func (state *buildState) pushed() []dnsEntry {
	result := []dnsEntry{}
	for _, outbound := range state.config.Outbounds {
		upstreams := state.resolvers(outbound.ID)
		for _, domain := range state.links[outbound.ID].Domains {
			if outbound.UsesDNS() && len(upstreams) != 0 && domain != "" {
				result = append(result, dnsEntry{key: suffixPrefix + domain, domain: domain,
					suffix: true, upstreams: upstreams})
			}
		}
	}
	return result
}

func (state *buildState) owners() []dnsOwner {
	result := []dnsOwner{}
	for _, outbound := range state.config.Outbounds {
		if hosts := outbound.Hosts(); state.active[outbound.ID] && len(hosts) != 0 {
			result = append(result, dnsOwner{outbound.ID, hosts})
		}
	}
	return result
}

func natural(name string, entries []dnsEntry, base []dnsUpstream,
	global []dnsUpstream) []dnsUpstream {
	for _, entry := range entries {
		if entry.matches(name) && len(entry.upstreams) != 0 {
			return entry.upstreams
		} else if entry.matches(name) {
			return global
		}
	}
	return base
}

func allowed(upstreams []dnsUpstream, banned []string) []dnsUpstream {
	result := []dnsUpstream{}
	for _, upstream := range upstreams {
		if !slices.ContainsFunc(upstream.needs, func(id string) bool {
			return slices.Contains(banned, id)
		}) {
			result = append(result, upstream)
		}
	}
	return result
}

func reaches(needs map[string][]string, source string, target string,
	visited map[string]bool) bool {
	if source == target {
		return true
	} else if visited[source] {
		return false
	}
	visited[source] = true
	for _, next := range needs[source] {
		if reaches(needs, next, target, visited) {
			return true
		}
	}
	return false
}

// NOTE: An outbound cannot ask for its own server name through itself, nor through an
// outbound that waits for it: such a name is pinned to the servers left after the loop is
// cut, in the worst case to the resolver of the system. A loop is cut in one place, at the
// outbound that stands first in the list.
func (state *buildState) pins(entries []dnsEntry, base []dnsUpstream,
	global []dnsUpstream) []dnsEntry {
	owners, banned := state.owners(), map[string][]string{}
	for _, owner := range owners {
		banned[owner.id] = []string{owner.id}
	}
	pinned := map[string][]dnsUpstream{}
	for changed := true; changed; {
		needs := map[string][]string{}
		changed, pinned = false, map[string][]dnsUpstream{}
		for _, owner := range owners {
			for _, host := range owner.hosts {
				forbidden := banned[owner.id]
				for _, other := range owners {
					if other.id != owner.id && slices.Contains(other.hosts, host) {
						forbidden = append(slices.Clone(forbidden), banned[other.id]...)
					}
				}
				upstreams := allowed(natural(host, entries, base, global), forbidden)
				if len(upstreams) == 0 {
					upstreams = allowed(global, forbidden)
				}
				if len(upstreams) == 0 {
					upstreams = []dnsUpstream{{text: config.DNSSystem, address: true}}
				}
				pinned[host] = upstreams
				for _, upstream := range upstreams {
					needs[owner.id] = append(needs[owner.id], upstream.needs...)
				}
			}
		}
		for _, owner := range owners {
			for _, next := range needs[owner.id] {
				if !changed && reaches(needs, next, owner.id, map[string]bool{}) {
					banned[owner.id], changed = append(banned[owner.id], next), true
				}
			}
		}
	}
	result := []dnsEntry{}
	for _, owner := range owners {
		for _, host := range owner.hosts {
			if !slices.ContainsFunc(result, func(entry dnsEntry) bool {
				return entry.domain == host
			}) {
				result = append(result, dnsEntry{key: host, domain: host, upstreams: pinned[host]})
			}
		}
	}
	return result
}

// NOTE: Routes take the first match, a policy of mihomo takes the most specific domain: an
// entry that an earlier one already covers is left out, and an entry with the servers of
// everything else stays only where a later entry would take its domains.
func arrange(entries []dnsEntry, base string) policyList {
	kept, exact, suffixes, matchers := []dnsEntry{}, map[string]bool{}, map[string]bool{},
		map[string]bool{}
	for _, entry := range entries {
		covered := matchers[entry.key] || (!entry.matcher && !entry.suffix && exact[entry.domain])
		for _, parent := range parents(entry.domain) {
			covered = covered || (!entry.matcher && suffixes[parent])
		}
		if covered {
			continue
		} else if kept = append(kept, entry); entry.matcher {
			matchers[entry.key] = true
		} else if entry.suffix {
			suffixes[entry.domain] = true
		} else {
			exact[entry.domain] = true
		}
	}
	result, later, wide, other := policyList{}, map[string]bool{}, false, false
	for index := len(kept) - 1; index >= 0; index-- {
		entry := kept[index]
		changed := signature(entry.upstreams) != base
		needed := changed || wide || (entry.matcher && other)
		for _, parent := range parents(entry.domain) {
			needed = needed || (!entry.matcher && later[parent])
		}
		if !needed {
			continue
		} else if other = other || changed; changed && entry.matcher {
			wide = true
		} else if changed && entry.suffix {
			later[entry.domain] = true
		}
		result = append(result, policyItem{entry.key, serverTexts(entry.upstreams)})
	}
	slices.Reverse(result)
	return result
}

func (state *buildState) buildDNS(target string) {
	global := state.global()
	base := state.resolvers(target)
	if len(base) == 0 {
		base = global
	}
	entries := append(slices.Clone(state.entries), state.pushed()...)
	entries = append(append(state.named(), state.checks()...), entries...)
	entries = append(state.pins(entries, base, global), entries...)
	for index := range entries {
		if len(entries[index].upstreams) == 0 {
			entries[index].upstreams = global
		}
	}
	dns := &dnsSection{Enable: true, IPv6: state.config.Settings.IPv6,
		Bootstrap: []string{}, Nameserver: serverTexts(base),
		Policy: arrange(entries, signature(base))}
	for _, upstream := range global {
		if upstream.address {
			dns.Bootstrap = append(dns.Bootstrap, upstream.text)
		}
	}
	if len(dns.Bootstrap) == 0 {
		dns.Bootstrap = []string{config.DNSSystem}
	}
	if state.document.Tun != nil {
		dns.EnhancedMode = "fake-ip"
		dns.FakeIPRange = link.CaptureFakeIPRange
	}
	state.document.DNS = dns
}
