package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"proximity/internal/config"
)

type DialFunc func(ctx context.Context, target string) (net.Conn, error)

type Reply struct {
	Sequence int     `json:"sequence"`
	Time     float64 `json:"time"`
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

const (
	TypeA    = 1
	TypeAAAA = 28
)

const headerSize = 12
const maxLabel = 63
const maxMessage = 65535
const dnsMedia = "application/dns-message"

// NOTE: Certificates to trust for DNS over TLS and HTTPS; nil is the pool of the system.
var rootCAs *x509.CertPool

var replyPattern = regexp.MustCompile(`icmp_seq=(\d+) .*time=([\d.]+) ms`)
var rcodeNames = map[int]string{1: "format error", 2: "server failure", 3: "name does not exist",
	4: "not implemented", 5: "refused"}

// NOTE: The CONNECT of the probe listener of mihomo: the answer comes at once, the connection
// to the target is made after it, so a failure shows up as a closed connection.
func Connect(ctx context.Context, proxy string, user string, password string,
	target string) (net.Conn, error) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to probe listener '%v': %w", proxy, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	credentials := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	reader := bufio.NewReader(connection)
	if _, err = fmt.Fprintf(connection, "CONNECT %v HTTP/1.1\r\nHost: %v\r\n"+
		"Proxy-Authorization: Basic %v\r\n\r\n", target, target, credentials); err != nil {
		connection.Close()
		return nil, fmt.Errorf("failed to send request to '%v': %w", proxy, err)
	} else if response, err := http.ReadResponse(reader, nil); err != nil {
		connection.Close()
		return nil, fmt.Errorf("failed to read answer of '%v': %w", proxy, err)
	} else if response.StatusCode != http.StatusOK {
		connection.Close()
		return nil, fmt.Errorf("unexpected answer of probe listener (%v)", response.StatusCode)
	}
	return &bufferedConn{Conn: connection, reader: reader}, nil
}

func ProxyDial(proxy string, user string, password string) DialFunc {
	return func(ctx context.Context, target string) (net.Conn, error) {
		return Connect(ctx, proxy, user, password, target)
	}
}

// NOTE: A plain (UDP) server is asked over TCP: the query goes through a proxy as a stream.
func Lookup(ctx context.Context, dialFunc DialFunc, server config.DNSServer,
	name string) ([]string, error) {
	switch server.Scheme {
	case "", config.DNSUDP, config.DNSTCP:
		return lookupStream(ctx, dialFunc, server, name, false)
	case config.DNSTLS:
		return lookupStream(ctx, dialFunc, server, name, true)
	case config.DNSHTTPS:
		return lookupHTTPS(ctx, dialFunc, server, name)
	default:
		return nil, fmt.Errorf("DNS over '%v' is not checked", server.Scheme)
	}
}

func MakeQuery(id uint16, name string, kind uint16) ([]byte, error) {
	result := []byte{byte(id >> 8), byte(id), 1, 0, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" || len(label) > maxLabel {
			return nil, fmt.Errorf("invalid name '%v'", name)
		}
		result = append(result, byte(len(label)))
		result = append(result, label...)
	}
	return append(result, 0, byte(kind>>8), byte(kind), 0, 1), nil
}

func ParseAnswer(data []byte, id uint16) ([]string, error) {
	if len(data) < headerSize {
		return nil, fmt.Errorf("short DNS answer (%v)", len(data))
	} else if binary.BigEndian.Uint16(data) != id {
		return nil, errors.New("unexpected DNS answer identifier")
	} else if code := int(data[3] & 0x0f); code != 0 {
		if text, found := rcodeNames[code]; found {
			return nil, errors.New(text)
		}
		return nil, fmt.Errorf("DNS error code (%v)", code)
	}
	questions, answers := int(binary.BigEndian.Uint16(data[4:])),
		int(binary.BigEndian.Uint16(data[6:]))
	offset, result := headerSize, []string{}
	for range questions {
		if offset = skipName(data, offset) + 4; offset > len(data) {
			return nil, errors.New("truncated DNS answer")
		}
	}
	for range answers {
		if offset = skipName(data, offset); offset+10 > len(data) {
			return nil, errors.New("truncated DNS answer")
		}
		kind, length := binary.BigEndian.Uint16(data[offset:]),
			int(binary.BigEndian.Uint16(data[offset+8:]))
		if offset += 10; offset+length > len(data) {
			return nil, errors.New("truncated DNS answer")
		}
		if address, ok := netip.AddrFromSlice(data[offset : offset+length]); ok &&
			(kind == TypeA && length == 4 || kind == TypeAAAA && length == 16) {
			result = append(result, address.String())
		}
		offset += length
	}
	return result, nil
}

func ParsePing(output string) []Reply {
	result := []Reply{}
	for _, line := range strings.Split(output, "\n") {
		if match := replyPattern.FindStringSubmatch(line); match != nil {
			sequence, _ := strconv.Atoi(match[1])
			value, _ := strconv.ParseFloat(match[2], 64)
			result = append(result, Reply{Sequence: sequence, Time: value})
		}
	}
	return result
}

func lookupStream(ctx context.Context, dialFunc DialFunc, server config.DNSServer, name string,
	secure bool) ([]string, error) {
	connection, err := dialFunc(ctx, server.Address())
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if secure {
		client := tls.Client(connection, &tls.Config{ServerName: server.Host, RootCAs: rootCAs})
		if err = client.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("failed to start TLS with '%v': %w", server.Address(), err)
		}
		connection = client
	}
	result := []string{}
	for index, kind := range []uint16{TypeA, TypeAAAA} {
		id := uint16(index + 1)
		query, err := MakeQuery(id, name, kind)
		if err != nil {
			return nil, err
		}
		frame := binary.BigEndian.AppendUint16(nil, uint16(len(query)))
		if _, err = connection.Write(append(frame, query...)); err != nil {
			return nil, fmt.Errorf("failed to send DNS query to '%v': %w", server.Address(), err)
		}
		size := make([]byte, 2)
		if _, err = io.ReadFull(connection, size); err != nil {
			return nil, fmt.Errorf("failed to read DNS answer of '%v': %w", server.Address(), err)
		}
		answer := make([]byte, binary.BigEndian.Uint16(size))
		if _, err = io.ReadFull(connection, answer); err != nil {
			return nil, fmt.Errorf("failed to read DNS answer of '%v': %w", server.Address(), err)
		}
		addresses, err := ParseAnswer(answer, id)
		if err != nil {
			return nil, err
		}
		result = append(result, addresses...)
	}
	return result, nil
}

func lookupHTTPS(ctx context.Context, dialFunc DialFunc, server config.DNSServer,
	name string) ([]string, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _ string,
		address string) (net.Conn, error) {
		return dialFunc(ctx, address)
	}, TLSClientConfig: &tls.Config{ServerName: server.Host, RootCAs: rootCAs}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport}
	result := []string{}
	for _, kind := range []uint16{TypeA, TypeAAAA} {
		query, err := MakeQuery(0, name, kind)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			server.URL(config.DNSHTTPS), bytes.NewReader(query))
		if err != nil {
			return nil, fmt.Errorf("failed to create DNS request: %w", err)
		}
		request.Header.Set("Content-Type", dnsMedia)
		request.Header.Set("Accept", dnsMedia)
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("failed to send DNS request: %w", err)
		}
		answer, err := io.ReadAll(io.LimitReader(response.Body, maxMessage))
		response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read DNS answer: %w", err)
		} else if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected DNS answer status (%v)", response.StatusCode)
		}
		addresses, err := ParseAnswer(answer, 0)
		if err != nil {
			return nil, err
		}
		result = append(result, addresses...)
	}
	return result, nil
}

func skipName(data []byte, offset int) int {
	for offset < len(data) {
		if length := int(data[offset]); length == 0 {
			return offset + 1
		} else if length&0xc0 == 0xc0 {
			return offset + 2
		} else {
			offset += length + 1
		}
	}
	return len(data) + 1
}

func (connection *bufferedConn) Read(buffer []byte) (int, error) {
	return connection.reader.Read(buffer)
}
