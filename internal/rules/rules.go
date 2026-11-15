package rules

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type Rule struct {
	Directive string
	Value     string
	Port      int
	NoResolve bool
}

type Variant struct {
	Port   int
	Target string
}

type Item struct {
	Key       string
	Label     string
	Directive string
	Target    string
	Rules     []Rule
}

type Warning struct {
	Message string   `json:"message"`
	Keys    []string `json:"keys"`
}

type checkEntry struct {
	item   Item
	rule   Rule
	prefix netip.Prefix
}

const (
	Domain         = "DOMAIN"
	DomainSuffix   = "DOMAIN-SUFFIX"
	DomainKeyword  = "DOMAIN-KEYWORD"
	DomainWildcard = "DOMAIN-WILDCARD"
	DomainRegex    = "DOMAIN-REGEX"
	GeoSite        = "GEOSITE"
	GeoIP          = "GEOIP"
	IPCIDR         = "IP-CIDR"
	IPCIDR6        = "IP-CIDR6"
	IPASN          = "IP-ASN"
	SourceIPCIDR   = "SRC-IP-CIDR"
	TargetPort     = "DST-PORT"
	SourcePort     = "SRC-PORT"
	Network        = "NETWORK"
	ProcessName    = "PROCESS-NAME"
	ProcessPath    = "PROCESS-PATH"
)

const maxWarnings = 100
const noResolveSuffix = ",no-resolve"
const commentPrefix = "#"

var Directives = []string{Domain, DomainSuffix, DomainKeyword, DomainWildcard, DomainRegex,
	GeoSite, GeoIP, IPCIDR, IPASN, SourceIPCIDR, TargetPort, SourcePort, Network, ProcessName,
	ProcessPath}

var labelPattern = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)
var keywordPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)
var wildcardPattern = regexp.MustCompile(`^[a-z0-9._*?-]+$`)
var geoPattern = regexp.MustCompile(`^[a-z0-9!@_.-]+$`)
var schemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)
var hostPortPattern = regexp.MustCompile(`^(.*):(\d+)$`)
var bracketPattern = regexp.MustCompile(`^\[([^\]]+)\](/\d+)?(?::(\d+))?$`)

func Parse(directive string, lines []string) ([]Rule, error) {
	result, _, err := ParseText(directive, lines)
	return result, err
}

func ParseText(directive string, lines []string) ([]Rule, []string, error) {
	if !IsDirective(directive) {
		return nil, nil, fmt.Errorf("unexpected directive '%v'", directive)
	}
	parsed, result := []Rule{}, []string{}
	for index, line := range lines {
		if line = strings.TrimSpace(line); line == "" {
			continue
		} else if IsComment(line) {
			result = append(result, line)
		} else if rule, err := parseLine(directive, line); err != nil {
			return nil, nil, fmt.Errorf("failed to parse line %v '%v': %w", index+1, line, err)
		} else {
			parsed = append(parsed, rule)
			result = append(result, rule.Text())
		}
	}
	if len(parsed) == 0 {
		return nil, nil, errors.New("rule parameters do not exist")
	}
	return parsed, result, nil
}

func IsComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), commentPrefix)
}

func IsDirective(directive string) bool {
	for _, name := range Directives {
		if name == directive {
			return true
		}
	}
	return false
}

func Check(items []Item) []Warning {
	result, entries := []Warning{}, []checkEntry{}
	for _, item := range items {
		for _, rule := range item.Rules {
			entry := checkEntry{item: item, rule: rule}
			if rule.Directive == IPCIDR || rule.Directive == IPCIDR6 {
				entry.prefix, _ = netip.ParsePrefix(rule.Value)
			}
			entries = append(entries, entry)
		}
	}
	for index, first := range entries {
		for _, second := range entries[index+1:] {
			if message := first.conflict(second); message != "" && len(result) < maxWarnings {
				result = append(result, Warning{Message: message + first.targets(second),
					Keys: []string{first.item.Key, second.item.Key}})
			}
		}
	}
	return result
}

func parseLine(directive string, line string) (Rule, error) {
	rule := Rule{Directive: directive}
	lower := strings.ToLower(line)
	switch directive {
	case IPCIDR, SourceIPCIDR, GeoIP, IPASN:
		if strings.HasSuffix(strings.ReplaceAll(lower, " ", ""), noResolveSuffix) {
			rule.NoResolve = true
			line = strings.TrimSpace(line[:strings.LastIndex(line, ",")])
			lower = strings.ToLower(line)
		}
	}
	if strings.ContainsAny(line, ",") {
		return Rule{}, errors.New("unexpected ',' character")
	}
	switch directive {
	case Domain, DomainSuffix:
		return rule.parseDomain(lower)
	case DomainKeyword:
		return rule.parsePattern(lower, keywordPattern)
	case DomainWildcard:
		return rule.parsePattern(lower, wildcardPattern)
	case GeoSite:
		return rule.parsePattern(lower, geoPattern)
	case GeoIP:
		if result, err := rule.parsePattern(lower, geoPattern); err != nil {
			return Rule{}, err
		} else if len(result.Value) == 2 {
			result.Value = strings.ToUpper(result.Value)
			return result, nil
		} else {
			return result, nil
		}
	case IPCIDR, SourceIPCIDR:
		return rule.parseNetwork(line)
	case IPASN:
		return rule.parseNumber(strings.TrimPrefix(lower, "as"))
	case TargetPort, SourcePort:
		return rule.parsePorts(line)
	case Network:
		if lower != "tcp" && lower != "udp" {
			return Rule{}, fmt.Errorf("unexpected network '%v'", line)
		}
		rule.Value = strings.ToUpper(lower)
		return rule, nil
	default:
		rule.Value = line
		return rule, nil
	}
}

func parsePort(text string) (int, error) {
	if port, err := strconv.Atoi(text); err != nil {
		return 0, fmt.Errorf("cannot parse %q as port: %w", text, err)
	} else if port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port (%v)", port)
	} else {
		return port, nil
	}
}

func punycode(label string) string {
	const base, minimum, maximum, skew, damp = 36, 1, 26, 38, 700
	runes, output := []rune(label), []byte{}
	for _, symbol := range runes {
		if symbol < 0x80 {
			output = append(output, byte(symbol))
		}
	}
	basic := len(output)
	if basic == len(runes) {
		return label
	} else if basic > 0 {
		output = append(output, '-')
	}
	code, delta, bias, handled := rune(0x80), 0, 72, basic
	for handled < len(runes) {
		next := rune(0x10FFFF)
		for _, symbol := range runes {
			if symbol >= code && symbol < next {
				next = symbol
			}
		}
		delta += int(next-code) * (handled + 1)
		code = next
		for _, symbol := range runes {
			if symbol < code {
				delta++
			} else if symbol == code {
				quotient := delta
				for step := base; ; step += base {
					threshold := min(max(step-bias, minimum), maximum)
					if quotient < threshold {
						break
					}
					output = append(output,
						punycodeDigit(threshold+(quotient-threshold)%(base-threshold)))
					quotient = (quotient - threshold) / (base - threshold)
				}
				output = append(output, punycodeDigit(quotient))
				if handled == basic {
					delta /= damp
				} else {
					delta /= 2
				}
				delta += delta / (handled + 1)
				bias = 0
				for delta > ((base-minimum)*maximum)/2 {
					delta /= base - minimum
					bias += base
				}
				bias += (base * delta) / (delta + skew)
				delta = 0
				handled++
			}
		}
		delta++
		code++
	}
	return "xn--" + string(output)
}

func punycodeDigit(value int) byte {
	if value < 26 {
		return byte('a' + value)
	}
	return byte('0' + value - 26)
}

func (rule Rule) Lines(target string, variants []Variant) []string {
	if rule.Port != 0 {
		for _, variant := range variants {
			if variant.Port == rule.Port {
				target = variant.Target
			}
		}
		return []string{rule.line(rule.Port, target)}
	} else if rule.Directive == TargetPort {
		return []string{rule.line(0, target)}
	}
	result := []string{}
	for _, variant := range variants {
		result = append(result, rule.line(variant.Port, variant.Target))
	}
	return append(result, rule.line(0, target))
}

func (rule Rule) line(port int, target string) string {
	condition, suffix := rule.Directive+","+rule.Value, ""
	if rule.NoResolve {
		suffix = noResolveSuffix
	}
	if port == 0 {
		return condition + "," + target + suffix
	}
	return fmt.Sprintf("AND,((%v%v),(%v,%v)),%v", condition, suffix, TargetPort, port, target)
}

func (rule Rule) parseDomain(text string) (Rule, error) {
	text = schemePattern.ReplaceAllString(text, "")
	if offset := strings.IndexAny(text, "/?#"); offset >= 0 {
		text = text[:offset]
	}
	if match := hostPortPattern.FindStringSubmatch(text); match != nil {
		text = match[1]
	}
	if rule.Directive == DomainSuffix {
		text = strings.TrimPrefix(strings.TrimPrefix(text, "+"), "*")
	}
	text = strings.Trim(text, ".")
	if text == "" || len(text) > 253 {
		return Rule{}, fmt.Errorf("invalid domain length (%v)", len(text))
	}
	labels := strings.Split(text, ".")
	for index, label := range labels {
		labels[index] = punycode(label)
		if !labelPattern.MatchString(labels[index]) {
			return Rule{}, fmt.Errorf("unexpected characters in domain label '%v'", label)
		}
	}
	rule.Value = strings.Join(labels, ".")
	return rule, nil
}

func (rule Rule) parsePattern(text string, pattern *regexp.Regexp) (Rule, error) {
	if !pattern.MatchString(text) {
		return Rule{}, fmt.Errorf("unexpected characters in '%v'", text)
	}
	rule.Value = text
	return rule, nil
}

func (rule Rule) parseNumber(text string) (Rule, error) {
	if number, err := strconv.ParseUint(text, 10, 32); err != nil {
		return Rule{}, fmt.Errorf("cannot parse %q as number: %w", text, err)
	} else {
		rule.Value = strconv.FormatUint(number, 10)
		return rule, nil
	}
}

func (rule Rule) parsePorts(text string) (Rule, error) {
	first, second, found := strings.Cut(text, "-")
	low, err := parsePort(strings.TrimSpace(first))
	if err != nil {
		return Rule{}, err
	} else if !found {
		rule.Value = strconv.Itoa(low)
		return rule, nil
	}
	if high, err := parsePort(strings.TrimSpace(second)); err != nil {
		return Rule{}, err
	} else if high < low {
		return Rule{}, fmt.Errorf("invalid port range (%v-%v)", low, high)
	} else {
		rule.Value = fmt.Sprintf("%v-%v", low, high)
		return rule, nil
	}
}

func (rule Rule) parseNetwork(text string) (Rule, error) {
	address, port := text, ""
	if match := bracketPattern.FindStringSubmatch(text); match != nil {
		address, port = match[1]+match[2], match[3]
	} else if strings.Count(text, ":") == 1 {
		address, port, _ = strings.Cut(text, ":")
	}
	prefix, err := netip.ParsePrefix(address)
	if err != nil {
		if host, err := netip.ParseAddr(address); err != nil {
			return Rule{}, fmt.Errorf("cannot parse %q as network: %w", address, err)
		} else {
			prefix = netip.PrefixFrom(host, host.BitLen())
		}
	}
	rule.Value = prefix.Masked().String()
	if rule.Directive == IPCIDR && prefix.Addr().Is6() {
		rule.Directive = IPCIDR6
	}
	if port == "" {
		return rule, nil
	} else if rule.Directive == SourceIPCIDR {
		return Rule{}, errors.New("port is not supported for source network")
	} else if rule.Port, err = parsePort(port); err != nil {
		return Rule{}, err
	}
	return rule, nil
}

func (entry checkEntry) conflict(other checkEntry) string {
	first, second := entry.rule, other.rule
	if first.Directive == second.Directive && first.Value == second.Value &&
		first.Port == second.Port {
		return fmt.Sprintf("duplicate %v '%v' in %v and %v", first.Directive, first.text(),
			entry.item.Label, other.item.Label)
	} else if entry.prefix.IsValid() && other.prefix.IsValid() {
		if entry.prefix.Bits() <= other.prefix.Bits() && entry.prefix.Overlaps(other.prefix) &&
			(first.Port == 0 || first.Port == second.Port) {
			return fmt.Sprintf("network '%v' in %v covers '%v' in %v", first.text(),
				entry.item.Label, second.text(), other.item.Label)
		}
		return ""
	} else if first.covers(second) {
		return fmt.Sprintf("%v '%v' in %v covers %v '%v' in %v", first.Directive, first.Value,
			entry.item.Label, second.Directive, second.Value, other.item.Label)
	}
	return ""
}

func (entry checkEntry) targets(other checkEntry) string {
	if entry.item.Target == other.item.Target {
		return ""
	}
	return fmt.Sprintf(" (different targets: '%v' and '%v')", entry.item.Target, other.item.Target)
}

func (rule Rule) Text() string {
	suffix := ""
	if rule.NoResolve {
		suffix = noResolveSuffix
	}
	return rule.text() + suffix
}

func (rule Rule) text() string {
	if rule.Port == 0 {
		return rule.Value
	} else if address, bits, found := strings.Cut(rule.Value, "/"); found &&
		strings.Contains(address, ":") {
		return fmt.Sprintf("[%v]/%v:%v", address, bits, rule.Port)
	}
	return fmt.Sprintf("%v:%v", rule.Value, rule.Port)
}

func (rule Rule) covers(other Rule) bool {
	if other.Directive != Domain && other.Directive != DomainSuffix {
		return false
	} else if rule.Directive == DomainKeyword {
		return strings.Contains(other.Value, rule.Value)
	} else if rule.Directive != DomainSuffix {
		return false
	}
	return other.Value == rule.Value || strings.HasSuffix(other.Value, "."+rule.Value)
}
