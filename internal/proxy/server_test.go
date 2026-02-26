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
			if got := isWebSocketUpgrade(r); got != tt.want {
				t.Errorf("isWebSocketUpgrade() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestProxyStripsWebSocketExtensions verifies the fix: the proxy must remove
// Sec-WebSocket-Extensions from upgrade requests so that the upstream never
// negotiates permessage-deflate compression.  Without this, compressed frames
// (RSV1 set) can be corrupted by the HTTP transport layer, producing:
//
//	RangeError: Invalid WebSocket frame: RSV1 must be clear
func TestProxyStripsWebSocketExtensions(t *testing.T) {
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
		conn.Close()
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
	conn.Close()
	resp.Body.Close()

	mu.Lock()
	ext := receivedExtensions
	mu.Unlock()

	if ext != "" {
		t.Errorf("proxy forwarded Sec-WebSocket-Extensions = %q to upstream; "+
			"want empty (the proxy must strip this to prevent RSV1 errors)", ext)
	}

	if strings.Contains(resp.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate") {
		t.Error("response negotiated permessage-deflate; the proxy should have prevented this")
	}
}

// TestWebSocketEchoThroughProxy exchanges messages through the proxy and
// verifies none are corrupted.  Before the fix, compressed frames from the
// upstream would carry RSV1=1 and the client would reject them with
// "Invalid WebSocket frame: RSV1 must be clear".
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
		defer conn.Close()
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
	defer conn.Close()
	resp.Body.Close()

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
			t.Fatalf("ReadMessage: %v (RSV1 frame error would appear here without the fix)", err)
		}
		if string(got) != want {
			t.Errorf("echo mismatch:\n got %q\nwant %q", got, want)
		}
	}
}

// TestDirectConnectionNegotiatesCompression is a control: connecting directly
// to the upstream (bypassing the proxy) DOES negotiate permessage-deflate.
// This proves the proxy is the component that strips the extension.
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
		conn.Close()
	}))
	defer upstream.Close()

	dialer := websocket.Dialer{EnableCompression: true}
	conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(upstream.URL, "http")+"/", nil)
	if err != nil {
		t.Fatalf("direct dial: %v", err)
	}
	conn.Close()

	ext := resp.Header.Get("Sec-WebSocket-Extensions")
	resp.Body.Close()

	if !strings.Contains(ext, "permessage-deflate") {
		t.Errorf("direct connection should negotiate compression; "+
			"got Sec-WebSocket-Extensions = %q", ext)
	}
}

// TestNonWebSocketRequestPreservesHeaders ensures the proxy only strips
// Sec-WebSocket-Extensions on upgrade requests, not on regular HTTP.
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
	resp.Body.Close()

	if receivedExtensions != "permessage-deflate" {
		t.Errorf("non-upgrade request: Sec-WebSocket-Extensions = %q; "+
			"want %q (proxy should only strip on WebSocket upgrades)",
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
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	if port == 0 {
		t.Fatalf("port is 0 in URL %q", rawURL)
	}
	return port
}
