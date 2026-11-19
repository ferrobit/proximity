package tools

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
)

func makeAnswer(t *testing.T, query []byte, code byte, records ...[]byte) []byte {
	result := append([]byte{}, query...)
	result[2], result[3] = 0x81, 0x80|code
	binary.BigEndian.PutUint16(result[6:], uint16(len(records)))
	for _, record := range records {
		kind := uint16(TypeA)
		if len(record) == 16 {
			kind = TypeAAAA
		}
		result = append(result, 0xc0, 12)
		result = binary.BigEndian.AppendUint16(result, kind)
		result = append(result, 0, 1, 0, 0, 0, 60)
		result = binary.BigEndian.AppendUint16(result, uint16(len(record)))
		result = append(result, record...)
	}
	return result
}

func answerFor(t *testing.T, query []byte) []byte {
	kind := binary.BigEndian.Uint16(query[len(query)-4:])
	if strings.Contains(string(query), "missing") {
		return makeAnswer(t, query, 3)
	} else if kind == TypeAAAA {
		return makeAnswer(t, query, 0, net.ParseIP("2001:db8::1").To16())
	}
	return makeAnswer(t, query, 0, []byte{192, 0, 2, 1}, []byte{192, 0, 2, 2})
}

func serveStream(t *testing.T, connection net.Conn) {
	defer connection.Close()
	for {
		size := make([]byte, 2)
		if _, err := io.ReadFull(connection, size); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(size))
		if _, err := io.ReadFull(connection, query); err != nil {
			return
		}
		answer := answerFor(t, query)
		_, _ = connection.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(answer))),
			answer...))
	}
}

func startStream(t *testing.T, secure bool) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	if secure {
		server := httptest.NewUnstartedServer(nil)
		server.StartTLS()
		certificate := server.TLS.Certificates[0]
		rootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		server.Close()
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}})
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go serveStream(t, connection)
		}
	}()
	return listener.Addr().String()
}

func plainDial(ctx context.Context, target string) (net.Conn, error) {
	dialer := net.Dialer{}
	return dialer.DialContext(ctx, "tcp", target)
}

func makeServer(t *testing.T, text string) config.DNSServer {
	result, err := config.ParseDNSServer(text)
	require.NoError(t, err)
	return result
}

func TestConnect_Proxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				request, err := http.ReadRequest(bufio.NewReader(connection))
				if err != nil {
					return
				} else if request.Header.Get("Proxy-Authorization") != "Basic dXNlcjpzZWNyZXQ=" {
					_, _ = connection.Write([]byte("HTTP/1.1 407 Denied\r\n\r\n"))
					return
				}
				_, _ = connection.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\nhello"))
			}()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := Connect(ctx, listener.Addr().String(), "user", "secret", "a.test:80")
	require.NoError(t, err)
	data, err := io.ReadAll(connection)
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
	{
		_, err := ProxyDial(listener.Addr().String(), "user", "wrong")(ctx, "a.test:80")
		require.EqualError(t, err, "unexpected answer of probe listener (407)")
		_, err = Connect(ctx, "127.0.0.1:1", "user", "secret", "a.test:80")
		require.ErrorContains(t, err, "failed to connect to probe listener '127.0.0.1:1'")
	}
}

func TestLookup_Transports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	plain := startStream(t, false)
	for _, text := range []string{"tcp://" + plain, plain} {
		addresses, err := Lookup(ctx, plainDial, makeServer(t, text), "a.test")
		require.NoError(t, err)
		require.Equal(t, []string{"192.0.2.1", "192.0.2.2", "2001:db8::1"}, addresses)
	}
	{
		_, err := Lookup(ctx, plainDial, makeServer(t, plain), "missing.test")
		require.EqualError(t, err, "name does not exist")
		_, err = Lookup(ctx, plainDial, makeServer(t, plain), "bad..name")
		require.EqualError(t, err, "invalid name 'bad..name'")
		_, err = Lookup(ctx, plainDial, makeServer(t, "quic://127.0.0.1"), "a.test")
		require.EqualError(t, err, "DNS over 'quic' is not checked")
		_, err = Lookup(ctx, plainDial, makeServer(t, "tcp://127.0.0.1:1"), "a.test")
		require.ErrorContains(t, err, "connection refused")
	}
	{
		secure := startStream(t, true)
		addresses, err := Lookup(ctx, plainDial, makeServer(t, "tls://"+secure), "a.test")
		require.NoError(t, err)
		require.Len(t, addresses, 3)
		rootCAs = nil
		_, err = Lookup(ctx, plainDial, makeServer(t, "tls://"+secure), "a.test")
		require.ErrorContains(t, err, "failed to start TLS with '"+secure+"'")
	}
	{
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter,
			request *http.Request) {
			query, _ := io.ReadAll(request.Body)
			if request.Header.Get("Content-Type") != dnsMedia {
				http.Error(writer, "bad", http.StatusBadRequest)
				return
			}
			_, _ = writer.Write(answerFor(t, query))
		}))
		defer server.Close()
		rootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		defer func() { rootCAs = nil }()
		addresses, err := Lookup(ctx, plainDial, makeServer(t, server.URL+"/dns-query"), "a.test")
		require.NoError(t, err)
		require.Equal(t, []string{"192.0.2.1", "192.0.2.2", "2001:db8::1"}, addresses)
		_, err = Lookup(ctx, plainDial, makeServer(t, server.URL+"/dns-query"), "missing.test")
		require.EqualError(t, err, "name does not exist")
		dial := func(ctx context.Context, target string) (net.Conn, error) {
			return plainDial(ctx, server.Listener.Addr().String())
		}
		_, err = Lookup(ctx, dial, makeServer(t, "https://dns.test/dns-query"), "a.test")
		require.ErrorContains(t, err, "failed to send DNS request")
		_, err = Lookup(ctx, plainDial, makeServer(t, "https://127.0.0.1:1/dns-query"), "a.test")
		require.ErrorContains(t, err, "connection refused")
	}
}

func TestParseAnswer_Problems(t *testing.T) {
	query, err := MakeQuery(7, "a.test.", TypeA)
	require.NoError(t, err)
	_, err = ParseAnswer(query[:5], 7)
	require.EqualError(t, err, "short DNS answer (5)")
	_, err = ParseAnswer(query, 8)
	require.EqualError(t, err, "unexpected DNS answer identifier")
	_, err = ParseAnswer(makeAnswer(t, query, 9), 7)
	require.EqualError(t, err, "DNS error code (9)")
	_, err = ParseAnswer(makeAnswer(t, query, 2), 7)
	require.EqualError(t, err, "server failure")
	full := makeAnswer(t, query, 0, []byte{192, 0, 2, 1})
	_, err = ParseAnswer(full[:len(full)-2], 7)
	require.EqualError(t, err, "truncated DNS answer")
	_, err = ParseAnswer(full[:len(query)+4], 7)
	require.EqualError(t, err, "truncated DNS answer")
	_, err = ParseAnswer(query[:14], 7)
	require.EqualError(t, err, "truncated DNS answer")
	addresses, err := ParseAnswer(full, 7)
	require.NoError(t, err)
	require.Equal(t, []string{"192.0.2.1"}, addresses)
	_, err = MakeQuery(1, strings.Repeat("a", 64)+".test", TypeA)
	require.Error(t, err)
}

func TestParsePing_Replies(t *testing.T) {
	output := "PING 192.0.2.1 (192.0.2.1) 56(84) bytes of data.\n" +
		"64 bytes from 192.0.2.1: icmp_seq=1 ttl=64 time=0.046 ms\n" +
		"64 bytes from 192.0.2.1: icmp_seq=3 ttl=64 time=12.5 ms\n\n" +
		"3 packets transmitted, 2 received, 33% packet loss, time 402ms\n"
	require.Equal(t, []Reply{{Sequence: 1, Time: 0.046}, {Sequence: 3, Time: 12.5}},
		ParsePing(output))
	require.Empty(t, ParsePing("ping: connect: Network is unreachable"))
}
