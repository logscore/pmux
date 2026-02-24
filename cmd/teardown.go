package cmd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/logscore/pmux/internal/platform"
	"github.com/logscore/pmux/pkg/config"
)

func Teardown(removeDNS bool) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)
	store := config.NewStore(paths.RoutesFile)

	// Kill all tracked processes
	routes, err := store.LoadRoutes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to load routes: %v\n", err)
	}

	for _, r := range routes {
		if r.PID > 0 {
			if proc, err := os.FindProcess(r.PID); err == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}
	}

	// Clear routes
	if err := store.ClearRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to clear routes: %v\n", err)
	} else {
		fmt.Printf("done - removed %d routes\n", len(routes))
	}

	// Stop proxy (which also stops the DNS server)
	if err := ProxyStop(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	// Optionally remove DNS resolver configuration
	if removeDNS {
		fmt.Println("removing DNS resolver config (requires sudo)...")
		if err := platform.RemoveResolver(p, paths); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove DNS config: %v\n", err)
		} else {
			fmt.Println("done - DNS configuration removed")
		}
	}

	fmt.Println("done - teardown complete")
	return nil
}
