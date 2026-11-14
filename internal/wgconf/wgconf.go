package wgconf

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

type Entry struct {
	Key   string
	Value string
}

type Section struct {
	Name    string
	Entries []Entry
}

type Config struct {
	Sections []Section
}

type amneziaContainer struct {
	Container string                     `json:"container"`
	AWG       map[string]json.RawMessage `json:"awg"`
	WireGuard map[string]json.RawMessage `json:"wireguard"`
}

type amneziaExport struct {
	Containers []amneziaContainer `json:"containers"`
	PrimaryDNS string             `json:"dns1"`
	SecondDNS  string             `json:"dns2"`
}

type amneziaLastConfig struct {
	Config string `json:"config"`
}

const sectionInterface = "interface"
const sectionPeer = "peer"
const amneziaPrefix = "vpn://"

var quickOnlyKeys = []string{"address", "dns", "mtu", "table", "preup", "postup", "predown",
	"postdown", "saveconfig"}

func Parse(text string) (Config, error) {
	result, err := internalParse(text)
	if err != nil {
		return Config{}, fmt.Errorf("wgconf.Parse failed: %w", err)
	}
	return result, nil
}

func Import(text string) (string, error) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, amneziaPrefix) {
		if _, err := Parse(text); err != nil {
			return "", fmt.Errorf("failed to import configuration: %w", err)
		}
		return text + "\n", nil
	}
	result, err := importAmnezia(strings.TrimPrefix(text, amneziaPrefix))
	if err != nil {
		return "", fmt.Errorf("failed to import Amnezia key: %w", err)
	} else if _, err = Parse(result); err != nil {
		return "", fmt.Errorf("failed to import Amnezia key: %w", err)
	}
	return result, nil
}

func internalParse(text string) (Config, error) {
	result := Config{}
	for index, line := range strings.Split(text, "\n") {
		if offset := strings.IndexAny(line, "#;"); offset >= 0 {
			line = line[:offset]
		}
		if line = strings.TrimSpace(line); line == "" {
			continue
		} else if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if name != sectionInterface && name != sectionPeer {
				return Config{}, fmt.Errorf("unexpected section '%v' at line %v", name, index+1)
			}
			result.Sections = append(result.Sections, Section{Name: name})
		} else if key, value, found := strings.Cut(line, "="); !found {
			return Config{}, fmt.Errorf("unexpected text at line %v", index+1)
		} else if len(result.Sections) == 0 {
			return Config{}, fmt.Errorf("unexpected key outside of section at line %v", index+1)
		} else {
			section := &result.Sections[len(result.Sections)-1]
			section.Entries = append(section.Entries,
				Entry{Key: strings.TrimSpace(key), Value: strings.TrimSpace(value)})
		}
	}
	if err := result.validate(); err != nil {
		return Config{}, err
	}
	return result, nil
}

func importAmnezia(data string) (string, error) {
	data = strings.TrimRight(strings.TrimSpace(data), "=")
	packed, err := base64.RawURLEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 data: %w", err)
	}
	document := packed
	if len(packed) > 4 {
		if reader, err := zlib.NewReader(bytes.NewReader(packed[4:])); err == nil {
			if unpacked, err := io.ReadAll(io.LimitReader(reader, 4<<20)); err == nil {
				document = unpacked
			}
		}
	}
	export := amneziaExport{}
	if err = json.Unmarshal(document, &export); err != nil {
		return "", fmt.Errorf("failed to decode JSON document: %w", err)
	}
	for _, container := range export.Containers {
		for _, protocol := range []map[string]json.RawMessage{container.AWG, container.WireGuard} {
			if result := export.extract(protocol); result != "" {
				return result, nil
			}
		}
	}
	return "", errors.New("AmneziaWG configuration does not exist")
}

func (export amneziaExport) extract(protocol map[string]json.RawMessage) string {
	raw, found := protocol["last_config"]
	if !found {
		return ""
	}
	text, last := "", amneziaLastConfig{}
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	} else if err = json.Unmarshal([]byte(text), &last); err != nil {
		return ""
	}
	result := strings.ReplaceAll(last.Config, "$PRIMARY_DNS", export.PrimaryDNS)
	result = strings.ReplaceAll(result, "$SECONDARY_DNS", export.SecondDNS)
	return strings.TrimSpace(result) + "\n"
}

func (config Config) Addresses() []string {
	return config.values(sectionInterface, "address")
}

func (config Config) DNS() []string {
	result := []string{}
	for _, value := range config.values(sectionInterface, "dns") {
		if _, err := netip.ParseAddr(value); err == nil {
			result = append(result, value)
		}
	}
	return result
}

func (config Config) MTU() int {
	for _, value := range config.values(sectionInterface, "mtu") {
		if result, err := strconv.Atoi(value); err == nil {
			return result
		}
	}
	return 0
}

func (config Config) AllowedIPs() []string {
	return config.values(sectionPeer, "allowedips")
}

func (config Config) Strip() string {
	builder := strings.Builder{}
	for _, section := range config.Sections {
		if section.Name == sectionInterface {
			builder.WriteString("[Interface]\n")
		} else {
			builder.WriteString("[Peer]\n")
		}
		for _, entry := range section.Entries {
			if !isQuickOnly(entry.Key) {
				builder.WriteString(entry.Key + " = " + entry.Value + "\n")
			}
		}
	}
	return builder.String()
}

func (config Config) values(name string, key string) []string {
	result := []string{}
	for _, section := range config.Sections {
		if section.Name != name {
			continue
		}
		for _, entry := range section.Entries {
			if strings.ToLower(entry.Key) != key {
				continue
			}
			for _, value := range strings.Split(entry.Value, ",") {
				if value = strings.TrimSpace(value); value != "" {
					result = append(result, value)
				}
			}
		}
	}
	return result
}

func (config Config) validate() error {
	count := 0
	for _, section := range config.Sections {
		if section.Name == sectionInterface {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("invalid number of [Interface] sections (%v)", count)
	} else if len(config.Sections) < 2 {
		return errors.New("[Peer] section does not exist")
	} else if err := validateKey(config.values(sectionInterface, "privatekey")); err != nil {
		return fmt.Errorf("failed to check PrivateKey: %w", err)
	} else if len(config.Addresses()) == 0 {
		return errors.New("interface Address does not exist")
	}
	for _, address := range config.Addresses() {
		if err := validatePrefix(address); err != nil {
			return fmt.Errorf("failed to check Address '%v': %w", address, err)
		}
	}
	for _, address := range config.AllowedIPs() {
		if err := validatePrefix(address); err != nil {
			return fmt.Errorf("failed to check AllowedIPs '%v': %w", address, err)
		}
	}
	endpoints := config.values(sectionPeer, "endpoint")
	if len(endpoints) == 0 {
		return errors.New("peer Endpoint does not exist")
	}
	for _, endpoint := range endpoints {
		if _, _, err := net.SplitHostPort(endpoint); err != nil {
			return fmt.Errorf("failed to check Endpoint '%v': %w", endpoint, err)
		}
	}
	for _, section := range config.Sections {
		if section.Name != sectionPeer {
			continue
		}
		peer := Config{Sections: []Section{section}}
		if err := validateKey(peer.values(sectionPeer, "publickey")); err != nil {
			return fmt.Errorf("failed to check PublicKey: %w", err)
		}
	}
	return nil
}

func isQuickOnly(key string) bool {
	key = strings.ToLower(key)
	for _, name := range quickOnlyKeys {
		if key == name {
			return true
		}
	}
	return false
}

func validateKey(values []string) error {
	if len(values) != 1 {
		return fmt.Errorf("invalid number of keys (%v)", len(values))
	} else if key, err := base64.StdEncoding.DecodeString(values[0]); err != nil {
		return fmt.Errorf("failed to decode base64 key: %w", err)
	} else if len(key) != 32 {
		return fmt.Errorf("invalid key length (%v)", len(key))
	}
	return nil
}

func validatePrefix(value string) error {
	if _, err := netip.ParsePrefix(value); err == nil {
		return nil
	} else if _, err = netip.ParseAddr(value); err != nil {
		return fmt.Errorf("cannot parse %q as address: %w", value, err)
	}
	return nil
}
