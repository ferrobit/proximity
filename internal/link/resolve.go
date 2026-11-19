package link

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

type ResolveFunc func(ctx context.Context, host string) ([]string, error)

var placeholders = netip.MustParsePrefix(CaptureFakeIPRange).Masked()
var openvpnResolved = []string{"remote", "http-proxy", "socks-proxy"}

func SystemResolve(ctx context.Context, host string) ([]string, error) {
	result, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve '%v': %w", host, err)
	}
	return result, nil
}

// NOTE: openvpn resolves a name with the resolver of the system, which gives placeholder
// (fake-IP) addresses while the host is captured, so the names are replaced before the start.
// A name with several addresses gives several "remote" lines, a connection block and a proxy
// take the first address.
func resolveOpenVPN(text string, lookupFunc func(host string) ([]string, error)) (string,
	[]string, error) {
	result, notes, block, remotes := []string{}, []string{}, "", 0
	var failure error
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if match := blockPattern.FindStringSubmatch(strings.TrimSpace(line)); match != nil &&
			block == "" && match[1] == "" {
			block = match[2]
		} else if match != nil && match[1] == "/" && match[2] == block {
			block = ""
		} else if len(fields) > 1 && (block == "" || block == "connection") &&
			slices.Contains(openvpnResolved, strings.TrimPrefix(fields[0], "--")) {
			name, single := strings.TrimPrefix(fields[0], "--"), block != ""
			single = single || name != openvpnResolved[0]
			if _, err := netip.ParseAddr(fields[1]); err == nil {
				remotes++
			} else if addresses, err := lookupFunc(fields[1]); err != nil && !single {
				notes = append(notes, fmt.Sprintf("remote '%v' is skipped: %v", fields[1], err))
				failure = errors.Join(failure, err)
				continue
			} else if err != nil {
				return "", nil, err
			} else {
				if single {
					addresses = addresses[:1]
				}
				notes = append(notes, fmt.Sprintf("'%v' is resolved to %v", fields[1],
					strings.Join(addresses, ", ")))
				for _, address := range addresses {
					fields[1], remotes = address, remotes+1
					result = append(result, strings.Join(fields, " "))
				}
				continue
			}
		}
		result = append(result, line)
	}
	if remotes == 0 && failure != nil {
		return "", notes, failure
	}
	return strings.Join(result, "\n"), notes, nil
}

func usable(text string) bool {
	address, err := netip.ParseAddr(text)
	return err != nil || !placeholders.Contains(address.Unmap())
}

func (manager *Manager) Lookup(ctx context.Context, host string) ([]string, error) {
	if address, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []string{address.String()}, nil
	}
	resolveFunc := manager.options.Resolve
	if resolveFunc == nil {
		resolveFunc = SystemResolve
	}
	addresses, err := resolveFunc(ctx, host)
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, wanted := range []bool{true, false} {
		for _, item := range addresses {
			if address, err := netip.ParseAddr(item); err == nil &&
				address.Unmap().Is4() == wanted && !placeholders.Contains(address.Unmap()) {
				result = append(result, address.Unmap().String())
			}
		}
	}
	if len(result) == 0 && len(addresses) != 0 && !slices.ContainsFunc(addresses, usable) {
		// NOTE: A Proximity OpenVPN inbound gives its clients placeholder answers unless its
		// DNS servers are set: a tunnel to it cannot resolve the names of the settings.
		return nil, fmt.Errorf("name '%v' has only placeholder (fake-IP) addresses (%v): its DNS "+
			"server answers with fake-IP, for a Proximity OpenVPN inbound set real DNS servers "+
			"in the inbound", host, strings.Join(addresses, ", "))
	} else if len(result) == 0 {
		return nil, fmt.Errorf("name '%v' has no usable address (%v)", host,
			strings.Join(addresses, ", "))
	}
	return result, nil
}

func (manager *Manager) ResolveTarget(ctx context.Context, target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		host, port = target, ""
	}
	addresses, err := manager.Lookup(ctx, host)
	if err != nil {
		return "", err
	} else if port == "" {
		return addresses[0], nil
	}
	return net.JoinHostPort(addresses[0], port), nil
}

func (runner *runner) dial(ctx context.Context, target string, mark int, device string,
	timeout time.Duration) (time.Duration, error) {
	address, err := runner.manager.ResolveTarget(ctx, target)
	if err != nil {
		return 0, err
	} else if address != target {
		runner.debugf("'%v' is resolved to '%v'", target, address)
	}
	latency, err := dialProbe(ctx, address, mark, device, timeout)
	if err != nil && address != target {
		return 0, fmt.Errorf("failed to check '%v': %w", target, err)
	}
	return latency, err
}
