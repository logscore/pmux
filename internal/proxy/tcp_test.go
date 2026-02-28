package proxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- helpers ---

// freePort returns an available TCP port on localhost.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// tcpEchoServer starts a TCP server that echoes back everything it receives.
// Returns the listener and a cleanup function.
func tcpEchoServer(t *testing.T, port int) (net.Listener, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("tcpEchoServer: listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln, func() { ln.Close() }
}

// tcpSinkServer starts a TCP server that reads all data, closes, and sends
// the total bytes read to a channel. Useful for testing one-way flows.
func tcpSinkServer(t *testing.T, port int) (net.Listener, <-chan int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("tcpSinkServer: listen: %v", err)
	}
	ch := make(chan int, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				n, _ := io.Copy(io.Discard, c)
				ch <- int(n)
			}(conn)
		}
	}()
	return ln, ch, func() { ln.Close() }
}

// setupTCPProxy creates a Server with a single TCP route and starts its listener.
// Returns the server and a cleanup function.
func setupTCPProxy(t *testing.T, listenPort, targetPort int, domain string) (*Server, func()) {
	t.Helper()
	srv := &Server{
		routes: []Route{{
			Domain:     domain,
			Port:       targetPort,
			ListenPort: listenPort,
			Type:       "tcp",
		}},
		tcpListeners: make(map[string]net.Listener),
	}
	srv.startTCPListeners()

	cleanup := func() {
		srv.mu.Lock()
		for _, ln := range srv.tcpListeners {
			ln.Close()
		}
		srv.mu.Unlock()
	}
	return srv, cleanup
}

// dialProxy dials the proxy's listen port and returns the connection.
func dialProxy(t *testing.T, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("dialProxy: %v", err)
	}
	return conn
}

// --- TCP Basic Forwarding ---

func TestTCPEchoThroughProxy(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "echo.test")
	defer cleanupProxy()

	conn := dialProxy(t, listenPort)
	defer conn.Close()

	messages := []string{
		"hello\n",
		"world\n",
		"roxy tcp proxy test\n",
	}

	for _, msg := range messages {
		_, err := conn.Write([]byte(msg))
		if err != nil {
			t.Fatalf("write: %v", err)
		}

		buf := make([]byte, len(msg))
		_, err = io.ReadFull(conn, buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}

		if string(buf) != msg {
			t.Errorf("echo mismatch: got %q, want %q", buf, msg)
		}
	}
}

// --- TCP Large Payload (bidirectional) ---

func TestTCPLargePayload(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "large.test")
	defer cleanupProxy()

	conn := dialProxy(t, listenPort)
	defer conn.Close()

	// 1MB payload
	payload := bytes.Repeat([]byte("ABCDEFGHIJ"), 100_000)

	var wg sync.WaitGroup
	var writeErr, readErr error
	var received []byte

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, writeErr = conn.Write(payload)
		// Half-close the write side so the echo server sees EOF
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		received, readErr = io.ReadAll(conn)
	}()
	wg.Wait()

	if writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if !bytes.Equal(received, payload) {
		t.Errorf("payload mismatch: got %d bytes, want %d bytes", len(received), len(payload))
	}
}

// --- TCP Half-Close ---

func TestTCPHalfClose(t *testing.T) {
	// Upstream server reads everything, then writes a response, then closes.
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", upstreamPort))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read until client closes its write side
		data, _ := io.ReadAll(conn)

		// Then send back the length as a response
		response := fmt.Sprintf("received:%d", len(data))
		conn.Write([]byte(response))
	}()

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "halfclose.test")
	defer cleanupProxy()

	conn := dialProxy(t, listenPort)

	// Send data then close write side
	payload := []byte("half-close-test-payload")
	conn.Write(payload)
	conn.(*net.TCPConn).CloseWrite()

	// Read the response
	resp, err := io.ReadAll(conn)
	conn.Close()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	expected := fmt.Sprintf("received:%d", len(payload))
	if string(resp) != expected {
		t.Errorf("half-close response: got %q, want %q", resp, expected)
	}
}

// --- TCP Concurrent Connections ---

func TestTCPConcurrentConnections(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "concurrent.test")
	defer cleanupProxy()

	const numConns = 50
	var wg sync.WaitGroup
	errors := make(chan error, numConns)

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort), 2*time.Second)
			if err != nil {
				errors <- fmt.Errorf("conn %d: dial: %v", id, err)
				return
			}
			defer conn.Close()

			msg := fmt.Sprintf("connection-%d\n", id)
			_, err = conn.Write([]byte(msg))
			if err != nil {
				errors <- fmt.Errorf("conn %d: write: %v", id, err)
				return
			}

			buf := make([]byte, len(msg))
			_, err = io.ReadFull(conn, buf)
			if err != nil {
				errors <- fmt.Errorf("conn %d: read: %v", id, err)
				return
			}

			if string(buf) != msg {
				errors <- fmt.Errorf("conn %d: mismatch: got %q, want %q", id, buf, msg)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

// --- TCP Upstream Unreachable ---

func TestTCPUpstreamUnreachable(t *testing.T) {
	listenPort := freePort(t)
	// Use a port that's definitely not listening
	deadPort := freePort(t)

	_, cleanupProxy := setupTCPProxy(t, listenPort, deadPort, "dead.test")
	defer cleanupProxy()

	conn := dialProxy(t, listenPort)
	defer conn.Close()

	// Write something to trigger the proxy to dial upstream
	conn.Write([]byte("hello"))

	// The connection should be closed by the proxy since upstream is unreachable.
	// Set a read deadline so we don't hang forever.
	conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	buf := make([]byte, 1024)
	_, err := conn.Read(buf)

	if err == nil {
		t.Error("expected error reading from proxy with dead upstream, got nil")
	}
}

// --- TCP ListenPort=0 Skip ---

func TestTCPSkipZeroListenPort(t *testing.T) {
	srv := &Server{
		routes: []Route{{
			Domain:     "nolisten.test",
			Port:       9999,
			ListenPort: 0, // should be skipped
			Type:       "tcp",
		}},
		tcpListeners: make(map[string]net.Listener),
	}
	srv.startTCPListeners()

	srv.mu.RLock()
	count := len(srv.tcpListeners)
	srv.mu.RUnlock()

	if count != 0 {
		t.Errorf("expected 0 TCP listeners for listen_port=0, got %d", count)
	}
}

// --- TCP Duplicate Listener Prevention ---

func TestTCPDuplicateListenerPrevention(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	srv := &Server{
		routes: []Route{{
			Domain:     "dup.test",
			Port:       upstreamPort,
			ListenPort: listenPort,
			Type:       "tcp",
		}},
		tcpListeners: make(map[string]net.Listener),
	}
	srv.startTCPListeners()
	defer func() {
		srv.mu.Lock()
		for _, ln := range srv.tcpListeners {
			ln.Close()
		}
		srv.mu.Unlock()
	}()

	// Call startTCPListeners again -- should NOT create a duplicate listener
	srv.startTCPListeners()

	srv.mu.RLock()
	count := len(srv.tcpListeners)
	srv.mu.RUnlock()

	if count != 1 {
		t.Errorf("expected exactly 1 listener after duplicate call, got %d", count)
	}

	// Verify it still works
	conn := dialProxy(t, listenPort)
	defer conn.Close()

	msg := "still-works\n"
	conn.Write([]byte(msg))
	buf := make([]byte, len(msg))
	io.ReadFull(conn, buf)
	if string(buf) != msg {
		t.Errorf("after duplicate start: got %q, want %q", buf, msg)
	}
}

// --- TCP Listener Reconciliation ---

func TestTCPReconcileAddRoute(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	// Start with no routes
	srv := &Server{
		routes:       []Route{},
		tcpListeners: make(map[string]net.Listener),
	}

	// Add a TCP route and reconcile
	srv.mu.Lock()
	srv.routes = []Route{{
		Domain:     "new.test",
		Port:       upstreamPort,
		ListenPort: listenPort,
		Type:       "tcp",
	}}
	srv.mu.Unlock()

	srv.reconcileTCPListeners()

	defer func() {
		srv.mu.Lock()
		for _, ln := range srv.tcpListeners {
			ln.Close()
		}
		srv.mu.Unlock()
	}()

	// Verify the new listener works
	conn := dialProxy(t, listenPort)
	defer conn.Close()

	msg := "reconciled\n"
	conn.Write([]byte(msg))
	buf := make([]byte, len(msg))
	io.ReadFull(conn, buf)
	if string(buf) != msg {
		t.Errorf("reconcile add: got %q, want %q", buf, msg)
	}
}

func TestTCPReconcileRemoveRoute(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	srv := &Server{
		routes: []Route{{
			Domain:     "remove.test",
			Port:       upstreamPort,
			ListenPort: listenPort,
			Type:       "tcp",
		}},
		tcpListeners: make(map[string]net.Listener),
	}
	srv.startTCPListeners()

	// Verify it's listening
	conn := dialProxy(t, listenPort)
	conn.Close()

	// Remove the route and reconcile
	srv.mu.Lock()
	srv.routes = []Route{}
	srv.mu.Unlock()

	srv.reconcileTCPListeners()

	// Verify listener was stopped -- dial should fail
	_, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort), 500*time.Millisecond)
	if err == nil {
		t.Error("expected dial to fail after route removal, but it succeeded")
	}
}

// --- Simulate Postgres Wire Protocol ---

// TestTCPPostgresProtocol simulates a Postgres client/server handshake through
// the TCP proxy. This verifies the proxy correctly forwards binary protocol
// data without corruption.
//
// Postgres wire protocol (v3):
//
//	Client sends: StartupMessage (4-byte length + 4-byte protocol version + params)
//	Server responds: 'R' AuthenticationOk (type byte + 4-byte length + 4-byte status)
//	Server sends: 'Z' ReadyForQuery (type byte + 4-byte length + 1-byte txn status)
func TestTCPPostgresProtocol(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	// Simulated Postgres server
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", upstreamPort))
	if err != nil {
		t.Fatalf("pg server listen: %v", err)
	}
	defer ln.Close()

	serverReady := make(chan struct{})
	serverGotStartup := make(chan bool, 1)

	go func() {
		close(serverReady)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read the StartupMessage
		// First 4 bytes: message length (int32, includes self)
		var msgLen int32
		if err := binary.Read(conn, binary.BigEndian, &msgLen); err != nil {
			serverGotStartup <- false
			return
		}

		// Read the rest of the startup message
		remaining := make([]byte, msgLen-4)
		if _, err := io.ReadFull(conn, remaining); err != nil {
			serverGotStartup <- false
			return
		}

		// Verify protocol version (3.0 = 196608)
		version := binary.BigEndian.Uint32(remaining[:4])
		if version != 196608 {
			serverGotStartup <- false
			return
		}

		serverGotStartup <- true

		// Send AuthenticationOk: 'R' + length(8) + status(0)
		authOk := []byte{
			'R',        // message type
			0, 0, 0, 8, // length (int32, includes self)
			0, 0, 0, 0, // auth ok status
		}
		conn.Write(authOk)

		// Send ReadyForQuery: 'Z' + length(5) + 'I' (idle)
		ready := []byte{
			'Z',        // message type
			0, 0, 0, 5, // length
			'I', // transaction status: idle
		}
		conn.Write(ready)
	}()

	<-serverReady

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "postgres.test")
	defer cleanupProxy()

	// Simulate Postgres client connecting through proxy
	conn := dialProxy(t, listenPort)
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Build StartupMessage
	// Protocol version 3.0 (major=3, minor=0) -> 196608
	// Parameters: "user\0postgres\0database\0testdb\0\0"
	params := []byte("user\x00postgres\x00database\x00testdb\x00\x00")
	msgLen := int32(4 + 4 + len(params)) // length + version + params
	var startupBuf bytes.Buffer
	binary.Write(&startupBuf, binary.BigEndian, msgLen)
	binary.Write(&startupBuf, binary.BigEndian, int32(196608)) // version 3.0
	startupBuf.Write(params)

	_, err = conn.Write(startupBuf.Bytes())
	if err != nil {
		t.Fatalf("write startup: %v", err)
	}

	// Check server received valid startup
	select {
	case ok := <-serverGotStartup:
		if !ok {
			t.Fatal("server did not receive valid Postgres startup message")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server to process startup")
	}

	// Read AuthenticationOk response through proxy
	authResp := make([]byte, 9)
	_, err = io.ReadFull(conn, authResp)
	if err != nil {
		t.Fatalf("read auth response: %v", err)
	}
	if authResp[0] != 'R' {
		t.Errorf("expected 'R' message type, got %q", authResp[0])
	}
	authLen := binary.BigEndian.Uint32(authResp[1:5])
	if authLen != 8 {
		t.Errorf("auth message length: got %d, want 8", authLen)
	}
	authStatus := binary.BigEndian.Uint32(authResp[5:9])
	if authStatus != 0 {
		t.Errorf("auth status: got %d, want 0 (OK)", authStatus)
	}

	// Read ReadyForQuery response
	readyResp := make([]byte, 6)
	_, err = io.ReadFull(conn, readyResp)
	if err != nil {
		t.Fatalf("read ready response: %v", err)
	}
	if readyResp[0] != 'Z' {
		t.Errorf("expected 'Z' message type, got %q", readyResp[0])
	}
	if readyResp[5] != 'I' {
		t.Errorf("transaction status: got %q, want 'I' (idle)", readyResp[5])
	}
}

// --- Simulate Redis RESP Protocol ---

// TestTCPRedisProtocol simulates a Redis client/server exchange using the RESP
// (REdis Serialization Protocol) through the TCP proxy. Tests PING, SET, GET
// commands to verify binary-safe forwarding.
//
// RESP format:
//
//	Arrays: *<count>\r\n
//	Bulk strings: $<length>\r\n<data>\r\n
//	Simple strings: +<string>\r\n
func TestTCPRedisProtocol(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	// Simulated Redis server
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", upstreamPort))
	if err != nil {
		t.Fatalf("redis server listen: %v", err)
	}
	defer ln.Close()

	store := make(map[string]string)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)

		for {
			// Read RESP array
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")

			if !strings.HasPrefix(line, "*") {
				continue
			}

			var count int
			fmt.Sscanf(line[1:], "%d", &count)

			args := make([]string, count)
			for i := 0; i < count; i++ {
				// Read bulk string header
				sizeLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				sizeLine = strings.TrimRight(sizeLine, "\r\n")
				var size int
				fmt.Sscanf(sizeLine[1:], "%d", &size)

				// Read bulk string data
				data := make([]byte, size+2) // +2 for \r\n
				if _, err := io.ReadFull(reader, data); err != nil {
					return
				}
				args[i] = string(data[:size])
			}

			if len(args) == 0 {
				continue
			}

			cmd := strings.ToUpper(args[0])
			switch cmd {
			case "PING":
				conn.Write([]byte("+PONG\r\n"))

			case "SET":
				if len(args) >= 3 {
					store[args[1]] = args[2]
					conn.Write([]byte("+OK\r\n"))
				}

			case "GET":
				if len(args) >= 2 {
					val, ok := store[args[1]]
					if ok {
						resp := fmt.Sprintf("$%d\r\n%s\r\n", len(val), val)
						conn.Write([]byte(resp))
					} else {
						conn.Write([]byte("$-1\r\n"))
					}
				}
			}
		}
	}()

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "redis.test")
	defer cleanupProxy()

	conn := dialProxy(t, listenPort)
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	reader := bufio.NewReader(conn)

	// Helper to send RESP command
	sendCommand := func(args ...string) {
		t.Helper()
		cmd := fmt.Sprintf("*%d\r\n", len(args))
		for _, arg := range args {
			cmd += fmt.Sprintf("$%d\r\n%s\r\n", len(arg), arg)
		}
		_, err := conn.Write([]byte(cmd))
		if err != nil {
			t.Fatalf("sendCommand: %v", err)
		}
	}

	// Helper to read one line
	readLine := func() string {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("readLine: %v", err)
		}
		return strings.TrimRight(line, "\r\n")
	}

	// Test PING
	sendCommand("PING")
	resp := readLine()
	if resp != "+PONG" {
		t.Errorf("PING: got %q, want +PONG", resp)
	}

	// Test SET
	sendCommand("SET", "mykey", "hello-from-roxy")
	resp = readLine()
	if resp != "+OK" {
		t.Errorf("SET: got %q, want +OK", resp)
	}

	// Test GET
	sendCommand("GET", "mykey")
	resp = readLine()
	if resp != "$15" {
		t.Errorf("GET bulk header: got %q, want $15", resp)
	}
	resp = readLine()
	if resp != "hello-from-roxy" {
		t.Errorf("GET value: got %q, want hello-from-roxy", resp)
	}

	// Test GET non-existent key
	sendCommand("GET", "nokey")
	resp = readLine()
	if resp != "$-1" {
		t.Errorf("GET nil: got %q, want $-1", resp)
	}
}

// --- TCP Binary Data Integrity ---

// TestTCPBinaryIntegrity sends every possible byte value through the proxy
// to verify no byte is corrupted or filtered.
func TestTCPBinaryIntegrity(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "binary.test")
	defer cleanupProxy()

	conn := dialProxy(t, listenPort)
	defer conn.Close()

	// Build a payload containing every byte value 0x00-0xFF, repeated
	payload := make([]byte, 256*4)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	var wg sync.WaitGroup
	var received []byte
	var readErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		conn.Write(payload)
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		received, readErr = io.ReadAll(conn)
	}()
	wg.Wait()

	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if !bytes.Equal(received, payload) {
		t.Errorf("binary integrity check failed: got %d bytes, want %d bytes", len(received), len(payload))
		// Find the first mismatch
		for i := range payload {
			if i >= len(received) {
				t.Errorf("  truncated at byte %d", i)
				break
			}
			if received[i] != payload[i] {
				t.Errorf("  first mismatch at byte %d: got 0x%02x, want 0x%02x", i, received[i], payload[i])
				break
			}
		}
	}
}

// --- TCP Multiple Routes ---

func TestTCPMultipleRoutes(t *testing.T) {
	upstream1Port := freePort(t)
	upstream2Port := freePort(t)
	listen1Port := freePort(t)
	listen2Port := freePort(t)

	// Two separate echo servers
	_, cleanup1 := tcpEchoServer(t, upstream1Port)
	defer cleanup1()
	_, cleanup2 := tcpEchoServer(t, upstream2Port)
	defer cleanup2()

	srv := &Server{
		routes: []Route{
			{Domain: "db.test", Port: upstream1Port, ListenPort: listen1Port, Type: "tcp"},
			{Domain: "cache.test", Port: upstream2Port, ListenPort: listen2Port, Type: "tcp"},
		},
		tcpListeners: make(map[string]net.Listener),
	}
	srv.startTCPListeners()
	defer func() {
		srv.mu.Lock()
		for _, ln := range srv.tcpListeners {
			ln.Close()
		}
		srv.mu.Unlock()
	}()

	// Test route 1
	conn1 := dialProxy(t, listen1Port)
	msg1 := "route-one\n"
	conn1.Write([]byte(msg1))
	buf1 := make([]byte, len(msg1))
	io.ReadFull(conn1, buf1)
	conn1.Close()
	if string(buf1) != msg1 {
		t.Errorf("route 1: got %q, want %q", buf1, msg1)
	}

	// Test route 2
	conn2 := dialProxy(t, listen2Port)
	msg2 := "route-two\n"
	conn2.Write([]byte(msg2))
	buf2 := make([]byte, len(msg2))
	io.ReadFull(conn2, buf2)
	conn2.Close()
	if string(buf2) != msg2 {
		t.Errorf("route 2: got %q, want %q", buf2, msg2)
	}
}

// --- TCP Mixed HTTP and TCP Routes ---

func TestTCPMixedRoutes(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	srv := &Server{
		routes: []Route{
			{Domain: "web.test", Port: 3000, Type: "http"},
			{Domain: "db.test", Port: upstreamPort, ListenPort: listenPort, Type: "tcp"},
			{Domain: "api.test", Port: 4000, Type: "http"},
		},
		tcpListeners: make(map[string]net.Listener),
	}
	srv.startTCPListeners()
	defer func() {
		srv.mu.Lock()
		for _, ln := range srv.tcpListeners {
			ln.Close()
		}
		srv.mu.Unlock()
	}()

	// Only 1 TCP listener should be created (for db.test)
	srv.mu.RLock()
	count := len(srv.tcpListeners)
	srv.mu.RUnlock()
	if count != 1 {
		t.Errorf("expected 1 TCP listener, got %d", count)
	}

	// Verify TCP route works
	conn := dialProxy(t, listenPort)
	defer conn.Close()

	msg := "mixed-route-tcp\n"
	conn.Write([]byte(msg))
	buf := make([]byte, len(msg))
	io.ReadFull(conn, buf)
	if string(buf) != msg {
		t.Errorf("mixed route TCP: got %q, want %q", buf, msg)
	}
}

// --- TCP Rapid Connect/Disconnect ---

func TestTCPRapidConnectDisconnect(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, cleanupEcho := tcpEchoServer(t, upstreamPort)
	defer cleanupEcho()

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "rapid.test")
	defer cleanupProxy()

	// Rapidly open and close connections
	const iterations = 100
	for i := 0; i < iterations; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort), time.Second)
		if err != nil {
			t.Fatalf("iteration %d: dial: %v", i, err)
		}
		conn.Close()
	}

	// After all rapid connections, verify the proxy still works
	conn := dialProxy(t, listenPort)
	defer conn.Close()

	msg := "still-alive\n"
	conn.Write([]byte(msg))
	buf := make([]byte, len(msg))
	_, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("post-rapid read: %v", err)
	}
	if string(buf) != msg {
		t.Errorf("post-rapid: got %q, want %q", buf, msg)
	}
}

// --- TCP One-Way Data Flow ---

func TestTCPOneWayDataToUpstream(t *testing.T) {
	upstreamPort := freePort(t)
	listenPort := freePort(t)

	_, bytesReceived, cleanupSink := tcpSinkServer(t, upstreamPort)
	defer cleanupSink()

	_, cleanupProxy := setupTCPProxy(t, listenPort, upstreamPort, "sink.test")
	defer cleanupProxy()

	conn := dialProxy(t, listenPort)

	payload := bytes.Repeat([]byte("X"), 50_000)
	conn.Write(payload)
	conn.Close()

	select {
	case n := <-bytesReceived:
		if n != len(payload) {
			t.Errorf("sink received %d bytes, want %d", n, len(payload))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for sink to receive data")
	}
}
