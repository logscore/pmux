package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// Route represents an active tunnel route.
type Route struct {
	Domain     string    `json:"domain"`
	Port       int       `json:"port"`                  // upstream service port
	ListenPort int       `json:"listen_port,omitempty"` // proxy listen port (TCP routes only)
	Type       string    `json:"type"`                  // "http" (default) or "tcp"
	TLS        bool      `json:"tls"`                   // serve this route over HTTPS
	Command    string    `json:"command"`
	PID        int       `json:"pid"`
	LogFile    string    `json:"log_file,omitempty"` // stdout/stderr log for detached processes
	Created    time.Time `json:"created"`
}

// Store manages the routes.json file.
type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(routesFile string) *Store {
	return &Store{path: routesFile}
}

// LoadRoutes reads all routes from disk.
func (s *Store) LoadRoutes() ([]Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	if len(data) == 0 {
		return nil, nil
	}

	var routes []Route
	if err := json.Unmarshal(data, &routes); err != nil {
		return nil, err
	}
	return routes, nil
}

// AddRoute appends a route and persists to disk.
func (s *Store) AddRoute(route Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if route.Type == "" {
		route.Type = "http"
	}

	routes, err := s.loadUnsafe()
	if err != nil {
		return err
	}

	routes = append(routes, route)
	return s.saveUnsafe(routes)
}

// UpdateRoute atomically updates a route by domain, applying the given function.
func (s *Store) UpdateRoute(domain string, fn func(*Route)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	routes, err := s.loadUnsafe()
	if err != nil {
		return err
	}

	for i := range routes {
		if routes[i].Domain == domain {
			fn(&routes[i])
			return s.saveUnsafe(routes)
		}
	}

	return fmt.Errorf("route %q not found", domain)
}

// RemoveRoute removes a route by domain and persists to disk.
func (s *Store) RemoveRoute(domain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	routes, err := s.loadUnsafe()
	if err != nil {
		return err
	}

	var filtered []Route
	for _, r := range routes {
		if r.Domain != domain {
			filtered = append(filtered, r)
		}
	}

	return s.saveUnsafe(filtered)
}

// PruneStaleRoutes removes routes whose PID is no longer alive.
// Returns the number of routes pruned.
func (s *Store) PruneStaleRoutes() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	routes, err := s.loadUnsafe()
	if err != nil {
		return 0, err
	}
	if len(routes) == 0 {
		return 0, nil
	}

	var alive []Route
	for _, r := range routes {
		if r.PID > 0 && !processAlive(r.PID) {
			continue // stale
		}
		alive = append(alive, r)
	}

	pruned := len(routes) - len(alive)
	if pruned > 0 {
		if err := s.saveUnsafe(alive); err != nil {
			return 0, err
		}
	}
	return pruned, nil
}

// FindRoute returns the first route matching the given domain, or nil.
func (s *Store) FindRoute(domain string) *Route {
	s.mu.Lock()
	defer s.mu.Unlock()

	routes, err := s.loadUnsafe()
	if err != nil {
		return nil
	}
	for i := range routes {
		if routes[i].Domain == domain {
			return &routes[i]
		}
	}
	return nil
}

// processAlive checks if a process with the given PID is still running.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// ClearRoutes removes all routes.
func (s *Store) ClearRoutes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveUnsafe(nil)
}

func (s *Store) loadUnsafe() ([]Route, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var routes []Route
	if err := json.Unmarshal(data, &routes); err != nil {
		return nil, err
	}
	return routes, nil
}

func (s *Store) saveUnsafe(routes []Route) error {
	if routes == nil {
		routes = []Route{}
	}
	data, err := json.MarshalIndent(routes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}
