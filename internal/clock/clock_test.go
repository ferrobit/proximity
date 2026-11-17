package clock

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func makeNTPServer(t *testing.T, shift time.Duration, mutate func(response []byte)) string {
	connection, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = connection.Close()
	})
	go func() {
		buffer := make([]byte, ntpPacketSize)
		for {
			count, address, err := connection.ReadFrom(buffer)
			if err != nil {
				return
			} else if count < ntpPacketSize {
				continue
			}
			response := make([]byte, ntpPacketSize)
			response[0], response[1] = 0x24, 2
			copy(response[24:32], buffer[40:48])
			putTimestamp(response[32:40], time.Now().Add(shift))
			putTimestamp(response[40:48], time.Now().Add(shift))
			if mutate != nil {
				mutate(response)
			}
			_, _ = connection.WriteTo(response, address)
		}
	}()
	return connection.LocalAddr().String()
}

func makeFakeClock(t *testing.T, offsets map[string]time.Duration,
	synchronized bool) (*Clock, *sync.Mutex) {
	mutex := &sync.Mutex{}
	path := filepath.Join(t.TempDir(), "clock.json")
	result := NewWith(path, func(_ context.Context, server string, _ int) (time.Duration, error) {
		mutex.Lock()
		defer mutex.Unlock()
		if offset, found := offsets[server]; found {
			return offset, nil
		}
		return 0, errors.New("unreachable")
	}, func() (bool, error) {
		mutex.Lock()
		defer mutex.Unlock()
		return synchronized, nil
	})
	result.options = Options{Servers: []string{"a", "b", "c"}, Tolerance: time.Second,
		Wait: time.Second, Interval: time.Hour}
	return result, mutex
}

func TestClock_Apply(t *testing.T) {
	offsets := map[string]time.Duration{"a": 5 * time.Second}
	clock, _ := makeFakeClock(t, offsets, false)
	options := Options{Servers: []string{"a"}, Tolerance: time.Second, Wait: time.Second,
		Interval: time.Hour}
	clock.Apply(options)
	status, err := clock.Wait(context.Background())
	require.NoError(t, err)
	require.Equal(t, StateCorrected, status.State)
	done := clock.done
	clock.Apply(options)
	require.Equal(t, done, clock.done)
	options.Tolerance = 10 * time.Second
	clock.Apply(options)
	require.NotEqual(t, done, clock.done)
	require.Eventually(t, func() bool {
		return clock.Status().State == StateHost
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, time.Second, clock.WaitLimit())
	clock.Close()
	clock.Close()
	require.Nil(t, clock.done)
}

func TestClock_Check(t *testing.T) {
	{
		clock, _ := makeFakeClock(t, map[string]time.Duration{"a": 100 * time.Millisecond,
			"b": 200 * time.Millisecond, "c": 900 * time.Millisecond}, false)
		status := clock.Check(context.Background())
		require.Equal(t, StateHost, status.State)
		require.Equal(t, int64(200), status.Offset)
		require.Equal(t, int64(0), status.Applied)
		require.Equal(t, "b", status.Server)
		require.Equal(t, 3, status.Answered)
		require.Equal(t, "host clock is accurate: 200ms from 'b'", status.Message)
		require.Equal(t, time.Duration(0), clock.Offset())
		require.True(t, status.Synchronized())
	}
	{
		offsets := map[string]time.Duration{"a": -90 * time.Second, "b": -91 * time.Second}
		clock, mutex := makeFakeClock(t, offsets, false)
		status := clock.Check(context.Background())
		require.Equal(t, StateCorrected, status.State)
		require.Equal(t, int64(-90000), status.Applied)
		require.Equal(t, "host clock is -1m30s off 'a', Proximity uses its own time",
			status.Message)
		require.WithinDuration(t, time.Now().Add(-90*time.Second), clock.Now(), time.Second)
		offset, err := ReadOffset(clock.Path())
		require.NoError(t, err)
		require.Equal(t, -90*time.Second, offset)
		mutex.Lock()
		delete(offsets, "a")
		delete(offsets, "b")
		mutex.Unlock()
		status = clock.Check(context.Background())
		require.Equal(t, StateCorrected, status.State)
		require.Equal(t, "NTP servers are unreachable, the last offset -1m30s is kept",
			status.Message)
		require.Equal(t, -90*time.Second, clock.Offset())
	}
	{
		clock, _ := makeFakeClock(t, map[string]time.Duration{}, true)
		status := clock.Check(context.Background())
		require.Equal(t, StateHost, status.State)
		require.Equal(t, "NTP servers are unreachable, host clock is synchronized", status.Message)
	}
	{
		clock, _ := makeFakeClock(t, map[string]time.Duration{}, false)
		status := clock.Check(context.Background())
		require.Equal(t, StateUnsynchronized, status.State)
		require.False(t, status.Synchronized())
		require.Equal(t, "NTP servers are unreachable and host clock is not synchronized",
			status.Message)
	}
	{
		clock, _ := makeFakeClock(t, map[string]time.Duration{"a": time.Second}, false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.Equal(t, StateChecking, clock.Check(ctx).State)
		clock.path = filepath.Join(t.TempDir(), "missing", "clock.json")
		require.Equal(t, StateHost, clock.Check(context.Background()).State)
		clock.path = ""
		require.Equal(t, StateHost, clock.Check(context.Background()).State)
	}
}

func TestClock_Nil(t *testing.T) {
	var clock *Clock
	clock.Apply(Options{})
	clock.Close()
	require.WithinDuration(t, time.Now(), clock.Now(), time.Second)
	require.Equal(t, StateHost, clock.Status().State)
	require.Equal(t, StateHost, clock.Check(context.Background()).State)
	status, err := clock.Wait(context.Background())
	require.NoError(t, err)
	require.True(t, status.Synchronized())
	require.Empty(t, clock.Path())
	require.Zero(t, clock.Offset())
	require.Zero(t, clock.WaitLimit())
}

func TestClock_Wait(t *testing.T) {
	offsets := map[string]time.Duration{}
	clock, mutex := makeFakeClock(t, offsets, false)
	clock.options.Wait = 200 * time.Millisecond
	require.Equal(t, StateChecking, clock.Status().State)
	_, err := clock.Wait(context.Background())
	require.EqualError(t, err, "time is not synchronized after 200ms: time is being checked")
	clock.Check(context.Background())
	_, err = clock.Wait(context.Background())
	require.EqualError(t, err, "time is not synchronized after 200ms: NTP servers are "+
		"unreachable and host clock is not synchronized")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = clock.Wait(ctx)
	require.ErrorIs(t, err, context.Canceled)
	clock.options.Wait = 5 * time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		mutex.Lock()
		offsets["a"] = 2 * time.Second
		mutex.Unlock()
		clock.Check(context.Background())
	}()
	status, err := clock.Wait(context.Background())
	require.NoError(t, err)
	require.Equal(t, StateCorrected, status.State)
}

func TestKernelSynchronized(t *testing.T) {
	_, err := KernelSynchronized()
	require.NoError(t, err)
}

func TestQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	{
		offset, err := Query(ctx, makeNTPServer(t, 42*time.Second, nil), 0)
		require.NoError(t, err)
		require.InDelta(t, 42, offset.Seconds(), 0.5)
		offset, err = Query(ctx, makeNTPServer(t, -3*time.Hour, nil), 1)
		if err == nil {
			require.InDelta(t, -3*60*60, offset.Seconds(), 0.5)
		}
	}
	{
		address := makeNTPServer(t, 0, func(response []byte) {
			response[0] = 0x23
		})
		_, err := Query(ctx, address, 0)
		require.EqualError(t, err, "unexpected NTP mode (3)")
		address = makeNTPServer(t, 0, func(response []byte) {
			response[0] = 0xe4
		})
		_, err = Query(ctx, address, 0)
		require.EqualError(t, err, "NTP server '"+address+"' is not synchronized")
		address = makeNTPServer(t, 0, func(response []byte) {
			response[1] = 0
		})
		_, err = Query(ctx, address, 0)
		require.EqualError(t, err, "NTP server '"+address+"' is not synchronized")
		address = makeNTPServer(t, 0, func(response []byte) {
			binary.BigEndian.PutUint64(response[24:32], 1)
		})
		_, err = Query(ctx, address, 0)
		require.EqualError(t, err, "unexpected NTP originate timestamp")
	}
	{
		connection, err := net.ListenPacket("udp4", "127.0.0.1:0")
		require.NoError(t, err)
		defer connection.Close()
		go func() {
			buffer := make([]byte, ntpPacketSize)
			if _, address, err := connection.ReadFrom(buffer); err == nil {
				_, _ = connection.WriteTo([]byte{1, 2, 3}, address)
			}
		}()
		_, err = Query(ctx, connection.LocalAddr().String(), 0)
		require.EqualError(t, err, "unexpected NTP response size (3)")
		short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer stop()
		_, err = Query(short, connection.LocalAddr().String(), 0)
		require.ErrorContains(t, err, "failed to read response of NTP server")
		_, err = Query(ctx, "bad host name", 0)
		require.ErrorContains(t, err, "failed to connect to NTP server 'bad host name'")
	}
}

func TestReadOffset(t *testing.T) {
	directory := t.TempDir()
	_, err := ReadOffset(filepath.Join(directory, "missing.json"))
	require.ErrorContains(t, err, "failed to read file")
	path := filepath.Join(directory, "clock.json")
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	_, err = ReadOffset(path)
	require.ErrorContains(t, err, "failed to decode file")
	require.NoError(t, os.WriteFile(path, []byte(`{"offset":1500}`), 0o600))
	offset, err := ReadOffset(path)
	require.NoError(t, err)
	require.Equal(t, 1500*time.Millisecond, offset)
	require.Equal(t, "1.5s", round(offset))
}
