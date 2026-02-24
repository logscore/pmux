package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
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

	pmuxdns "github.com/logscore/pmux/internal/dns"
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
	dnsServer, err := pmuxdns.Start()
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
		http.Error(w, fmt.Sprintf("pmux: no route for host %q", host), http.StatusNotFound)
		return
	}

	upstream := fmt.Sprintf("localhost:%d", matched.Port)

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
			http.Error(w, fmt.Sprintf("pmux: upstream unreachable (%v)", err), http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
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
