package cmd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/logscore/porter/internal/platform"
	"github.com/logscore/porter/pkg/config"
)

func Stop(domain string) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)
	store := config.NewStore(paths.RoutesFile)

	routes, err := store.LoadRoutes()
	if err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	// Find route
	var target *config.Route
	for _, r := range routes {
		if r.Domain == domain {
			target = &r
			break
		}
	}

	if target == nil {
		return fmt.Errorf("no active tunnel for domain: %s", domain)
	}

	// Kill process if still running
	if target.PID > 0 {
		proc, err := os.FindProcess(target.PID)
		if err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	// Remove from store (proxy watches the file and updates automatically)
	if err := store.RemoveRoute(domain); err != nil {
		return fmt.Errorf("failed to remove route: %w", err)
	}

	fmt.Printf("Stopped: %s\n", domain)
	return nil
}
