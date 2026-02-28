package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

func TestIsWebSocketUpgrade(t *testing.T) {
	tests := []struct {
		name       string
		connection string
		upgrade    string
		want       bool
	}{
		{"valid upgrade", "Upgrade", "websocket", true},
		{"case insensitive", "upgrade", "WebSocket", true},
		{"multi-value connection", "keep-alive, Upgrade", "websocket", true},
		{"multi-value mixed case", "Keep-Alive, upgrade", "websocket", true},
		{"normal request", "", "", false},
		{"upgrade to h2c", "Upgrade", "h2c", false},
		{"websocket without connection upgrade", "keep-alive", "websocket", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "/", nil)
			if tt.connection != "" {
				r.Header.Set("Connection", tt.connection)
			}
			if tt.upgrade != "" {
				r.Header.Set("Upgrade", tt.upgrade)
			}
			if got := websocket.IsWebSocketUpgrade(r); got != tt.want {
				t.Errorf("IsWebSocketUpgrade() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestProxyIsolatesCompression verifies that the proxy terminates WebSocket
// on both sides, so the upstream never sees the client's compression offer.
// The proxy dials the upstream as a fresh connection without permessage-deflate,
// preventing RSV1 frame errors entirely.
func TestProxyIsolatesCompression(t *testing.T) {
	var mu sync.Mutex
	var receivedExtensions string

	upgrader := websocket.Upgrader{
		EnableCompression: true,
		CheckOrigin:       func(*http.Request) bool { return true },
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedExtensions = r.Header.Get("Sec-WebSocket-Extensions")
		mu.Unlock()

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()

	srv := &Server{
		routes: []Route{{Domain: "app.test", Port: parsePort(t, upstream.URL), Type: "http"}},
	}
	proxyServer := httptest.NewServer(http.HandlerFunc(srv.handleHTTP))
	defer proxyServer.Close()

	dialer := websocket.Dialer{EnableCompression: true}
	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/"
	conn, resp, err := dialer.Dial(wsURL, http.Header{"Host": {"app.test"}})
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	_ = conn.Close()
	_ = resp.Body.Close()

	mu.Lock()
	ext := receivedExtensions
	mu.Unlock()

	if ext != "" {
		t.Errorf("upstream received Sec-WebSocket-Extensions = %q; "+
			"want empty (proxy should open an independent connection without compression)", ext)
	}

	if strings.Contains(resp.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		t.Error("client response negotiated permessage-deflate; proxy should not offer compression")
	}
}

// TestWebSocketEchoThroughProxy exchanges messages through the proxy and
// verifies none are corrupted.
func TestWebSocketEchoThroughProxy(t *testing.T) {
	upgrader := websocket.Upgrader{
		EnableCompression: true,
		CheckOrigin:       func(*http.Request) bool { return true },
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	srv := &Server{
		routes: []Route{{Domain: "app.test", Port: parsePort(t, upstream.URL), Type: "http"}},
	}
	proxyServer := httptest.NewServer(http.HandlerFunc(srv.handleHTTP))
	defer proxyServer.Close()

	dialer := websocket.Dialer{EnableCompression: true}
	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http") + "/"
	conn, resp, err := dialer.Dial(wsURL, http.Header{"Host": {"app.test"}})
	if err != nil {
		t.Fatalf("dial through proxy: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = resp.Body.Close()

	messages := []string{
		"hello",
		`{"type":"update","path":"/src/App.tsx","timestamp":1700000000}`,
		strings.Repeat("hot-module-replacement-payload ", 100),
	}
	for _, want := range messages {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(want)); err != nil {
			t.Fatalf("WriteMessage: %v", err)
		}
		_, got, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		if string(got) != want {
			t.Errorf("echo mismatch:\n got %q\nwant %q", got, want)
		}
	}
}

// TestDirectConnectionNegotiatesCompression is a control: connecting directly
// to the upstream (bypassing the proxy) DOES negotiate permessage-deflate.
// This proves the upstream supports compression — the proxy just doesn't use it.
func TestDirectConnectionNegotiatesCompression(t *testing.T) {
	upgrader := websocket.Upgrader{
		EnableCompression: true,
		CheckOrigin:       func(*http.Request) bool { return true },
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()

	dialer := websocket.Dialer{EnableCompression: true}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http")+"/", nil)
	if err != nil {
		t.Fatalf("direct dial: %v", err)
	}
	_ = conn.Close()

	ext := resp.Header.Get("Sec-WebSocket-Extensions")
	_ = resp.Body.Close()

	if !strings.Contains(ext, "permessage-deflate") {
		t.Errorf("direct connection should negotiate compression; "+
			"got Sec-WebSocket-Extensions = %q", ext)
	}
}

// TestNonWebSocketRequestPreservesHeaders ensures regular HTTP requests
// pass headers through unmodified (WebSocket goes through a separate path).
func TestNonWebSocketRequestPreservesHeaders(t *testing.T) {
	var receivedExtensions string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedExtensions = r.Header.Get("Sec-WebSocket-Extensions")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	srv := &Server{
		routes: []Route{{Domain: "app.test", Port: parsePort(t, upstream.URL), Type: "http"}},
	}
	proxyServer := httptest.NewServer(http.HandlerFunc(srv.handleHTTP))
	defer proxyServer.Close()

	req, _ := http.NewRequest("GET", proxyServer.URL+"/", nil)
	req.Host = "app.test"
	req.Header.Set("Sec-WebSocket-Extensions", "permessage-deflate")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()

	if receivedExtensions != "permessage-deflate" {
		t.Errorf("non-upgrade request: Sec-WebSocket-Extensions = %q; "+
			"want %q (regular HTTP headers should pass through unmodified)",
			receivedExtensions, "permessage-deflate")
	}
}

func parsePort(t *testing.T, rawURL string) int {
	t.Helper()
	parts := strings.Split(rawURL, ":")
	if len(parts) < 3 {
		t.Fatalf("cannot parse port from URL %q", rawURL)
	}
	var port int
	_, _ = fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	if port == 0 {
		t.Fatalf("port is 0 in URL %q", rawURL)
	}
	return port
}
