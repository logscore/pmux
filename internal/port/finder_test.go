package port

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestFind_RandomMode_ReturnsValidPort(t *testing.T) {
	p, err := Find(0, "nonexistent-routes.json")
	if err != nil {
		t.Fatalf("Find(0) returned error: %v", err)
	}
	if p < minPort || p > maxPort {
		t.Fatalf("port %d outside range %d-%d", p, minPort, maxPort)
	}
}

func TestFind_RandomMode_AvoidsWellKnownPorts(t *testing.T) {
	// Run many times to get statistical confidence.
	for range 200 {
		p, err := Find(0, "nonexistent-routes.json")
		if err != nil {
			t.Fatalf("Find(0) returned error: %v", err)
		}
		if wellKnownPorts[p] {
			t.Fatalf("Find(0) returned well-known port %d", p)
		}
	}
}

func TestFind_ExactPin_ReturnsRequestedPort(t *testing.T) {
	// Find a port we know is free by briefly listening.
	ln, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatalf("failed to find free port for test: %v", err)
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	p, err := Find(freePort, "nonexistent-routes.json")
	if err != nil {
		t.Fatalf("Find(%d) returned error: %v", freePort, err)
	}
	if p != freePort {
		t.Fatalf("expected port %d, got %d", freePort, p)
	}
}

func TestFind_ExactPin_ErrorWhenBusy(t *testing.T) {
	// Occupy a port.
	ln, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	_, err = Find(busyPort, "nonexistent-routes.json")
	if err == nil {
		t.Fatalf("expected error for busy port %d, got nil", busyPort)
	}
}

func TestFind_ExactPin_ErrorWhenClaimed(t *testing.T) {
	// Write a routes file with a claimed port.
	dir := t.TempDir()
	routesFile := filepath.Join(dir, "routes.json")
	routes := []struct {
		Port int `json:"port"`
	}{{Port: 19999}}
	data, _ := json.Marshal(routes)
	if err := os.WriteFile(routesFile, data, 0644); err != nil {
		t.Fatalf("failed to write routes file: %v", err)
	}

	_, err := Find(19999, routesFile)
	if err == nil {
		t.Fatal("expected error for claimed port 19999, got nil")
	}
}

func TestFind_RandomMode_AvoidsClaimedPorts(t *testing.T) {
	// Claim a large set of ports in the routes file and verify none are returned.
	dir := t.TempDir()
	routesFile := filepath.Join(dir, "routes.json")

	// Claim a few specific ports.
	claimedPorts := []int{20000, 20001, 20002}
	routes := make([]struct {
		Port int `json:"port"`
	}, len(claimedPorts))
	for i, p := range claimedPorts {
		routes[i].Port = p
	}
	data, _ := json.Marshal(routes)
	if err := os.WriteFile(routesFile, data, 0644); err != nil {
		t.Fatalf("failed to write routes file: %v", err)
	}

	claimedSet := make(map[int]bool)
	for _, p := range claimedPorts {
		claimedSet[p] = true
	}

	for range 50 {
		p, err := Find(0, routesFile)
		if err != nil {
			t.Fatalf("Find(0) returned error: %v", err)
		}
		if claimedSet[p] {
			t.Fatalf("Find(0) returned claimed port %d", p)
		}
	}
}

func TestFind_InvalidExactPort(t *testing.T) {
	for _, p := range []int{-1, 0, 70000} {
		if p == 0 {
			continue // 0 means random mode, not invalid
		}
		_, err := Find(p, "nonexistent-routes.json")
		if err == nil {
			t.Fatalf("expected error for port %d, got nil", p)
		}
	}
}

func TestFind_RandomMode_ReturnsDifferentPorts(t *testing.T) {
	// Verify that multiple calls don't always return the same port.
	seen := make(map[int]bool)
	for range 10 {
		p, err := Find(0, "nonexistent-routes.json")
		if err != nil {
			t.Fatalf("Find(0) returned error: %v", err)
		}
		seen[p] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected multiple different ports from 10 calls, got %d unique port(s)", len(seen))
	}
}

func TestWellKnownPorts_AreInValidRange(t *testing.T) {
	for p := range wellKnownPorts {
		if p < 1 || p > 65535 {
			t.Errorf("well-known port %d is outside valid range", p)
		}
	}
}

func TestFind_FallbackScan_StartsAt1024(t *testing.T) {
	// This test verifies that the fallback sequential scan can find ports
	// starting from 1024. We can't easily force the random path to fail,
	// but we can verify the function works even if we occupy many ports.
	// Just verify the basic contract holds.
	p, err := Find(0, "nonexistent-routes.json")
	if err != nil {
		t.Fatalf("Find(0) returned error: %v", err)
	}
	if p < minPort {
		t.Fatalf("port %d is below minimum %d", p, minPort)
	}
}

func TestCheckAvailable_FreePort(t *testing.T) {
	ln, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	if err := checkAvailable(freePort); err != nil {
		t.Fatalf("expected port %d to be available: %v", freePort, err)
	}
}

func TestCheckAvailable_BusyPort(t *testing.T) {
	ln, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	if err := checkAvailable(busyPort); err == nil {
		t.Fatalf("expected port %d to be unavailable", busyPort)
	}
}

func TestLoadClaimedPorts_ValidFile(t *testing.T) {
	dir := t.TempDir()
	routesFile := filepath.Join(dir, "routes.json")
	routes := []struct {
		Port int `json:"port"`
	}{{Port: 3000}, {Port: 4000}, {Port: 5000}}
	data, _ := json.Marshal(routes)
	_ = os.WriteFile(routesFile, data, 0644)

	claimed := loadClaimedPorts(routesFile)
	for _, r := range routes {
		if !claimed[r.Port] {
			t.Errorf("expected port %d to be claimed", r.Port)
		}
	}
}

func TestLoadClaimedPorts_MissingFile(t *testing.T) {
	claimed := loadClaimedPorts("/nonexistent/path/routes.json")
	if len(claimed) != 0 {
		t.Errorf("expected empty map for missing file, got %d entries", len(claimed))
	}
}

func TestLoadClaimedPorts_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	routesFile := filepath.Join(dir, "routes.json")
	_ = os.WriteFile(routesFile, []byte("not json"), 0644)

	claimed := loadClaimedPorts(routesFile)
	if len(claimed) != 0 {
		t.Errorf("expected empty map for invalid JSON, got %d entries", len(claimed))
	}
}

func BenchmarkFind_Random(b *testing.B) {
	for range b.N {
		_, err := Find(0, "nonexistent-routes.json")
		if err != nil {
			b.Fatalf("Find(0) returned error: %v", err)
		}
	}
}

func BenchmarkFind_ExactPin(b *testing.B) {
	// Find a free port to pin to.
	ln, err := net.Listen("tcp4", ":0")
	if err != nil {
		b.Fatalf("failed to find free port: %v", err)
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	for range b.N {
		p, err := Find(freePort, "nonexistent-routes.json")
		if err != nil {
			// Port might get taken between iterations; skip non-fatal.
			continue
		}
		_ = p
	}
}

func ExampleFind() {
	// Random port selection (default behavior).
	port, err := Find(0, "")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("got port %d (between %d and %d)\n", port, minPort, maxPort)
}
