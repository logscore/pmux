package cmd

import (
	"fmt"
	"os"
	"syscall"

	"github.com/logscore/porter/internal/platform"
	"github.com/logscore/porter/internal/proxy"
	"github.com/logscore/porter/pkg/config"
)

type StopOptions struct {
	Targets   []string // ID prefixes or exact domains
	All       bool
	RemoveDNS bool
}

func Stop(opts StopOptions) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)
	store := config.NewStore(paths.RoutesFile)

	if opts.All {
		return stopAll(store, paths, p, opts.RemoveDNS)
	}

	if len(opts.Targets) == 0 {
		return fmt.Errorf("usage: porter stop <id|domain>... or porter stop -a")
	}

	var failed bool
	for _, target := range opts.Targets {
		route, err := store.ResolveRoute(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			failed = true
			continue
		}

		if route.PID > 0 {
			if proc, err := os.FindProcess(route.PID); err == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}

		if err := store.RemoveRoute(route.Domain); err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to remove route %s: %v\n", route.Domain, err)
			failed = true
			continue
		}

		fmt.Printf("%s (%s)\n", route.ID, route.Domain)
	}

	if failed {
		return fmt.Errorf("some routes could not be stopped")
	}
	return nil
}

func stopAll(store *config.Store, paths platform.Paths, p platform.Platform, removeDNS bool) error {
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

	if err := store.ClearRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to clear routes: %v\n", err)
	} else if len(routes) > 0 {
		fmt.Printf("Stopped %d route(s)\n", len(routes))
	}

	// Stop proxy
	if err := ProxyStop(); err != nil {
		// Only warn if the proxy was supposed to be running
		if proxy.IsRunning(paths.ConfigDir) {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}

	if removeDNS {
		if err := platform.RemoveResolver(p, paths); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove DNS config: %v\n", err)
		} else {
			fmt.Println("dns config removed")
		}
	}

	return nil
}
