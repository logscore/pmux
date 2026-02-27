package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	porterdns "github.com/logscore/porter/internal/dns"
)

const (
	// routePollInterval is how often the routes file is checked for changes.
	routePollInterval = 500 * time.Millisecond
	// tcpDialTimeout is the timeout for dialing upstream TCP connections.
	tcpDialTimeout = 5 * time.Second
	// ProxyStartRetries is the max number of retries when waiting for the proxy to start.
	ProxyStartRetries = 20
	// ProxyStartRetryInterval is the delay between proxy start retries.
	ProxyStartRetryInterval = 100 * time.Millisecond
	// shutdownTimeout is the max time allowed for graceful shutdown.
	shutdownTimeout = 10 * time.Second
)

// Route is the in-memory representation of a proxy route.
type Route struct {
	Domain     string `json:"domain"`
	Port       int    `json:"port"`                  // upstream service port
	ListenPort int    `json:"listen_port,omitempty"` // proxy listen port (TCP routes only)
	Type       string `json:"type"`                  // "http" or "tcp"
}

// Server is the built-in reverse proxy.
type Server struct {
	httpAddr   string
	httpsAddr  string
	tlsEnabled bool
	certsDir   string
	routesFile string

	mu     sync.RWMutex
	routes []Route

	httpServer   *http.Server
	httpsServer  *http.Server
	tcpListeners map[string]net.Listener // domain -> listener
}

// Options configures the proxy server.
type Options struct {
	HTTPPort   int
	HTTPSPort  int
	TLS        bool
	CertsDir   string
	RoutesFile string
}

// New creates a new proxy server.
func New(opts Options) *Server {
	if opts.HTTPPort == 0 {
		opts.HTTPPort = 80
	}
	if opts.HTTPSPort == 0 {
		opts.HTTPSPort = 443
	}

	return &Server{
		httpAddr:     fmt.Sprintf(":%d", opts.HTTPPort),
		httpsAddr:    fmt.Sprintf(":%d", opts.HTTPSPort),
		tlsEnabled:   opts.TLS,
		certsDir:     opts.CertsDir,
		routesFile:   opts.RoutesFile,
		tcpListeners: make(map[string]net.Listener),
	}
}

// Run starts the proxy + DNS server, watches for route changes, and blocks until signaled.
func (s *Server) Run() error {
	if err := s.loadRoutes(); err != nil {
		log.Printf("warning: failed to load routes: %v", err)
	}

	// Start built-in DNS server
	dnsServer, err := porterdns.Start()
	if err != nil {
		log.Printf("warning: failed to start DNS server: %v (is port 53 in use?)", err)
	}

	// Start HTTP server
	mux := http.HandlerFunc(s.handleHTTP)
	s.httpServer = &http.Server{
		Addr:    s.httpAddr,
		Handler: mux,
	}

	errCh := make(chan error, 2)

	go func() {
		log.Printf("proxy listening on http://0.0.0.0%s", s.httpAddr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	// Start HTTPS server if TLS enabled
	if s.tlsEnabled {
		tlsConfig, err := s.buildTLSConfig()
		if err != nil {
			log.Printf("warning: TLS setup failed: %v (HTTPS disabled)", err)
			s.tlsEnabled = false
		} else {
			s.httpsServer = &http.Server{
				Addr:      s.httpsAddr,
				Handler:   mux,
				TLSConfig: tlsConfig,
			}

			go func() {
				ln, err := tls.Listen("tcp", s.httpsAddr, tlsConfig)
				if err != nil {
					log.Printf("warning: could not listen on %s: %v (HTTPS disabled)", s.httpsAddr, err)
					return
				}
				log.Printf("proxy listening on https://0.0.0.0%s", s.httpsAddr)
				if err := s.httpsServer.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Printf("https server error: %v", err)
				}
			}()
		}
	}

	// Start TCP listeners for tcp-type routes
	s.startTCPListeners()

	// Watch routes file for changes
	go s.watchRoutes()

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received %v, shutting down...", sig)
	case err := <-errCh:
		return err
	}

	if dnsServer != nil {
		dnsServer.Stop()
	}
	return s.shutdown()
}

func (s *Server) shutdown() error {
	s.mu.Lock()
	for domain, ln := range s.tcpListeners {
		ln.Close()
		delete(s.tcpListeners, domain)
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			s.httpServer.Close()
		}
	}
	if s.httpsServer != nil {
		if err := s.httpsServer.Shutdown(ctx); err != nil {
			s.httpsServer.Close()
		}
	}
	return nil
}

// handleHTTP is the core HTTP handler. It matches the Host header to a route
// and reverse-proxies the request. WebSocket upgrades work automatically.
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	// Strip port if present
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	s.mu.RLock()
	var matched *Route
	for i := range s.routes {
		if strings.EqualFold(s.routes[i].Domain, host) && s.routes[i].Type != "tcp" {
			matched = &s.routes[i]
			break
		}
	}
	s.mu.RUnlock()

	if matched == nil {
		s.serveNotFound(w, host)
		return
	}

	upstream := fmt.Sprintf("localhost:%d", matched.Port)

	// WebSocket upgrades bypass httputil.ReverseProxy entirely.
	// Go's HTTP transport can corrupt WebSocket frames (RSV1 errors),
	// so we hijack both connections and copy raw bytes.
	if websocket.IsWebSocketUpgrade(r) {
		s.handleWebSocket(w, r, upstream, host)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = upstream
			req.Header.Set("X-Forwarded-Host", host)
			if _, ok := req.Header["X-Forwarded-For"]; !ok {
				req.Header.Set("X-Forwarded-For", r.RemoteAddr)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error [%s -> %s]: %v", host, upstream, err)
			http.Error(w, fmt.Sprintf("porter: upstream unreachable (%v)", err), http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
}

// serveNotFound renders a styled HTML page listing all available routes.
func (s *Server) serveNotFound(w http.ResponseWriter, host string) {
	s.mu.RLock()
	routes := make([]Route, len(s.routes))
	copy(routes, s.routes)
	s.mu.RUnlock()

	data := struct {
		Host   string
		Routes []Route
	}{Host: host, Routes: routes}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	if err := notFoundTmpl.Execute(w, data); err != nil {
		log.Printf("warning: failed to render not-found page: %v", err)
	}
}

var notFoundTmpl = template.Must(template.New("notfound").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Porter - not found</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { background: #0d1117; color: #c9d1d9; font-family: 'SF Mono', 'Cascadia Code', 'Fira Code', monospace; display: flex; justify-content: center; padding: 60px 20px; min-height: 100vh; }
  .container { max-width: 600px; width: 100%; }
  h1 { font-size: 1.4rem; color: #f85149; margin-bottom: 6px; }
  .sub { color: #8b949e; font-size: 0.85rem; margin-bottom: 32px; }
  h2 { font-size: 0.9rem; color: #8b949e; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 12px; }
  .routes { border: 1px solid #21262d; border-radius: 6px; overflow: hidden; }
  .route { display: flex; justify-content: space-between; align-items: center; padding: 10px 14px; border-bottom: 1px solid #21262d; }
  .route:last-child { border-bottom: none; }
  .route a { color: #58a6ff; text-decoration: none; }
  .route a:hover { text-decoration: underline; }
  .port { color: #8b949e; font-size: 0.85rem; }
  .tag { font-size: 0.7rem; color: #8b949e; background: #21262d; padding: 2px 6px; border-radius: 3px; margin-left: 8px; }
  .empty { padding: 20px 14px; color: #8b949e; text-align: center; }
</style>
</head>
<body>
<div class="container">
  <h1>not found</h1>
  <p class="sub">no route configured for <strong>{{.Host}}</strong></p>
  <h2>available routes</h2>
  <div class="routes">
  {{if .Routes}}
    {{range .Routes}}
    <div class="route">
      <span>
        {{if eq .Type "tcp"}}
          {{.Domain}}<span class="tag">tcp</span>
        {{else}}
          <a href="http://{{.Domain}}">{{.Domain}}</a>
        {{end}}
      </span>
      <span class="port">
        {{if eq .Type "tcp"}}:{{.ListenPort}} &rarr; :{{.Port}}{{else}}:{{.Port}}{{end}}
      </span>
    </div>
    {{end}}
  {{else}}
    <div class="empty">no routes configured</div>
  {{end}}
  </div>
</div>
</body>
</html>`))

// handleWebSocket proxies a WebSocket connection by terminating the
// protocol on both sides (client ↔ proxy ↔ upstream). Because each
// side is an independent WebSocket connection, compression negotiation
// is fully isolated — the RSV1 frame corruption that occurs when
// httputil.ReverseProxy passes compressed frames through is impossible.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request, upstream, host string) {
	// Accept the client's WebSocket upgrade
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket proxy: client upgrade: %v", err)
		return
	}
	defer clientConn.Close()

	// Dial the upstream as a fresh WebSocket connection (no compression)
	dialer := websocket.Dialer{}
	reqHeader := http.Header{
		"Host":             {host},
		"X-Forwarded-Host": {host},
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		reqHeader.Set("X-Forwarded-For", v)
	} else {
		reqHeader.Set("X-Forwarded-For", r.RemoteAddr)
	}

	upstreamConn, _, err := dialer.Dial("ws://"+upstream+r.URL.RequestURI(), reqHeader)
	if err != nil {
		log.Printf("websocket proxy: upstream dial ws://%s%s: %v", upstream, r.URL.RequestURI(), err)
		return
	}
	defer upstreamConn.Close()

	// Bidirectional message copy
	errc := make(chan error, 2)
	go func() { errc <- copyWS(upstreamConn, clientConn) }() // client → upstream
	go func() { errc <- copyWS(clientConn, upstreamConn) }() // upstream → client
	<-errc
}

// copyWS reads messages from src and writes them to dst until an error occurs.
func copyWS(dst, src *websocket.Conn) error {
	for {
		mt, msg, err := src.ReadMessage()
		if err != nil {
			return err
		}
		if err := dst.WriteMessage(mt, msg); err != nil {
			return err
		}
	}
}

// startTCPListeners starts a TCP listener for each tcp-type route.
func (s *Server) startTCPListeners() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, route := range s.routes {
		if route.Type != "tcp" {
			continue
		}
		s.startTCPListenerLocked(route)
	}
}

// startTCPListenerLocked starts a single TCP listener. Caller must hold s.mu.
func (s *Server) startTCPListenerLocked(route Route) {
	if route.ListenPort == 0 {
		log.Printf("tcp proxy: skipping %s (no listen_port configured)", route.Domain)
		return
	}

	// Listen on ListenPort, forward to Port (the upstream service)
	listenAddr := fmt.Sprintf("127.0.0.1:%d", route.ListenPort)

	// Check if we already have a listener for this domain
	if _, ok := s.tcpListeners[route.Domain]; ok {
		return
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Printf("tcp proxy: failed to listen on %s for %s: %v", listenAddr, route.Domain, err)
		return
	}

	s.tcpListeners[route.Domain] = ln
	log.Printf("tcp proxy: %s (:%d) -> localhost:%d", route.Domain, route.ListenPort, route.Port)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go s.handleTCP(conn, route.Port)
		}
	}()
}

func (s *Server) handleTCP(src net.Conn, targetPort int) {
	defer src.Close()

	dst, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort), tcpDialTimeout)
	if err != nil {
		log.Printf("tcp proxy: dial failed: %v", err)
		return
	}
	defer dst.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(dst, src)
		// Signal dst that no more data is coming from src
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(src, dst)
		// Signal src that no more data is coming from dst
		if tc, ok := src.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()
	wg.Wait()
}

// loadRoutes reads routes from the routes.json file.
func (s *Server) loadRoutes() error {
	data, err := os.ReadFile(s.routesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}

	var routes []Route
	if err := json.Unmarshal(data, &routes); err != nil {
		return err
	}

	// Default type to "http"
	for i := range routes {
		if routes[i].Type == "" {
			routes[i].Type = "http"
		}
	}

	s.mu.Lock()
	s.routes = routes
	s.mu.Unlock()

	return nil
}

// watchRoutes polls the routes file for changes and reloads.
func (s *Server) watchRoutes() {
	var lastMod time.Time

	for {
		time.Sleep(routePollInterval)

		info, err := os.Stat(s.routesFile)
		if err != nil {
			continue
		}

		if info.ModTime().After(lastMod) {
			lastMod = info.ModTime()
			if err := s.loadRoutes(); err != nil {
				log.Printf("warning: failed to reload routes: %v", err)
				continue
			}
			s.reconcileTCPListeners()
		}
	}
}

// reconcileTCPListeners stops listeners for removed TCP routes and starts
// listeners for new ones.
func (s *Server) reconcileTCPListeners() {
	s.mu.RLock()
	activeTCP := make(map[string]bool)
	for _, route := range s.routes {
		if route.Type == "tcp" {
			activeTCP[route.Domain] = true
		}
	}
	s.mu.RUnlock()

	// Stop listeners for routes that no longer exist
	s.mu.Lock()
	for domain, ln := range s.tcpListeners {
		if !activeTCP[domain] {
			ln.Close()
			delete(s.tcpListeners, domain)
			log.Printf("tcp proxy: stopped listener for removed route %s", domain)
		}
	}
	s.mu.Unlock()

	// Start listeners for new routes
	s.startTCPListeners()
}

func (s *Server) buildTLSConfig() (*tls.Config, error) {
	caKeyPath := filepath.Join(s.certsDir, "ca-key.pem")
	caCertPath := filepath.Join(s.certsDir, "ca-cert.pem")
	certPath := filepath.Join(s.certsDir, "server-cert.pem")
	keyPath := filepath.Join(s.certsDir, "server-key.pem")

	// Generate CA + server cert if missing
	if err := os.MkdirAll(s.certsDir, 0755); err != nil {
		return nil, fmt.Errorf("create certs dir: %w", err)
	}
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		if err := GenerateCA(caCertPath, caKeyPath); err != nil {
			return nil, fmt.Errorf("generate CA: %w", err)
		}
		if err := GenerateServerCert(caCertPath, caKeyPath, certPath, keyPath, []string{"*.test", "localhost"}); err != nil {
			return nil, fmt.Errorf("generate server cert: %w", err)
		}
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
	}, nil
}

// PidFile returns the path to the proxy PID file.
func PidFile(configDir string) string {
	return filepath.Join(configDir, "proxy.pid")
}

// WritePidFile writes the current process PID.
func WritePidFile(configDir string) error {
	return os.WriteFile(PidFile(configDir), []byte(fmt.Sprintf("%d", os.Getpid())), 0600)
}

// ReadPid reads the proxy PID from disk. Returns 0 if not found.
func ReadPid(configDir string) int {
	data, err := os.ReadFile(PidFile(configDir))
	if err != nil {
		return 0
	}
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	return pid
}

// RemovePidFile removes the PID file.
func RemovePidFile(configDir string) {
	os.Remove(PidFile(configDir))
}

// IsRunning checks if the proxy is reachable on its HTTP port.
func IsRunning(configDir string) bool {
	pid := ReadPid(configDir)
	if pid == 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists
	return proc.Signal(syscall.Signal(0)) == nil
}
