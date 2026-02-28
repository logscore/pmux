package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/logscore/roxy/internal/domain"
	"github.com/logscore/roxy/internal/platform"
	"github.com/logscore/roxy/internal/port"
	"github.com/logscore/roxy/internal/proxy"
	"github.com/logscore/roxy/pkg/config"
)

// ANSI color codes for service prefixes.
var colors = []string{
	"\x1b[36m", // cyan
	"\x1b[33m", // yellow
	"\x1b[32m", // green
	"\x1b[35m", // magenta
	"\x1b[34m", // blue
	"\x1b[31m", // red
}

const colorReset = "\x1b[0m"

// RunAll starts all services from a RoxyConfig concurrently with prefixed output.
func RunAll(cfg *config.RoxyConfig, detach bool) error {
	if len(cfg.Services) == 0 {
		return fmt.Errorf("no services defined in roxy.yaml")
	}

	// Sort service names for deterministic port assignment and color order.
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	// If detach mode, just run each service detached via RunService.
	if detach {
		for _, name := range names {
			svc := cfg.Services[name]
			if err := RunService(name, svc, true); err != nil {
				return fmt.Errorf("service %s: %w", name, err)
			}
		}
		return nil
	}

	// --- Foreground mode: run all concurrently with prefixed output ---

	p := platform.Detect()
	paths := platform.GetPaths(p)

	if err := os.MkdirAll(paths.ConfigDir, 0755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	// DNS resolver setup
	if !platform.ResolverConfigured(p, paths, 1299) {
		if err := platform.ConfigureResolver(p, paths, 1299); err != nil {
			return fmt.Errorf("failed to configure DNS resolver: %w", err)
		}
		fmt.Println("done - DNS configured")
		fmt.Println()
	}

	// Auto-start proxy if not running
	if !proxy.IsRunning(paths.ConfigDir) {
		if err := ProxyStart(ProxyOptions{HTTPPort: 80, TLS: true, HTTPSPort: 443, DNSPort: 5353}); err != nil {
			return fmt.Errorf("failed to start proxy: %w", err)
		}
		for range proxy.ProxyStartRetries {
			time.Sleep(proxy.ProxyStartRetryInterval)
			if proxy.IsRunning(paths.ConfigDir) {
				break
			}
		}
		if !proxy.IsRunning(paths.ConfigDir) {
			return fmt.Errorf("proxy failed to start -- check if port 80 is in use")
		}
	}

	store := config.NewStore(paths.RoutesFile)

	// Prune stale routes
	if pruned, err := store.PruneStaleRoutes(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to prune stale routes: %v\n", err)
	} else if pruned > 0 {
		fmt.Printf("cleaned up %d stale route(s)\n", pruned)
	}

	// Compute max name length for aligned prefixes.
	maxLen := 0
	for _, name := range names {
		if len(name) > maxLen {
			maxLen = len(name)
		}
	}

	// Pre-assign ports and domains, build child commands.
	type serviceInfo struct {
		name   string
		svc    config.ServiceConfig
		port   int
		domain string
		id     string
		color  string
		prefix string
	}

	services := make([]serviceInfo, 0, len(names))
	for i, name := range names {
		svc := cfg.Services[name]

		assignedPort, err := port.Find(svc.Port, paths.RoutesFile)
		if err != nil {
			return fmt.Errorf("service %s: failed to find port: %w", name, err)
		}

		svcName := svc.Name
		if svcName == "" {
			svcName = name
		}
		dom, err := domain.Generate(svcName)
		if err != nil {
			return fmt.Errorf("service %s: failed to generate domain: %w", name, err)
		}

		if existing := store.FindRoute(dom); existing != nil {
			return fmt.Errorf("service %s: domain %s already in use (pid %d)", name, dom, existing.PID)
		}

		id := config.GenerateID(dom)
		color := colors[i%len(colors)]
		prefix := fmt.Sprintf("%s[%-*s]%s ", color, maxLen, name, colorReset)

		routeType := "http"
		if svc.ListenPort > 0 {
			routeType = "tcp"
		}

		// Register route so port.Find won't reassign it to the next service.
		if err := store.AddRoute(config.Route{
			ID:         id,
			Domain:     dom,
			Port:       assignedPort,
			ListenPort: svc.ListenPort,
			Type:       routeType,
			TLS:        svc.TLS,
			Command:    svc.Cmd,
			Created:    time.Now(),
		}); err != nil {
			return fmt.Errorf("service %s: failed to register route: %w", name, err)
		}

		scheme := "http"
		if svc.TLS {
			scheme = "https"
		}
		fmt.Printf("  %s%s%s  %s://%s\n", color, name, colorReset, scheme, dom)

		services = append(services, serviceInfo{
			name:   name,
			svc:    svc,
			port:   assignedPort,
			domain: dom,
			id:     id,
			color:  color,
			prefix: prefix,
		})
	}
	fmt.Println()

	// Signal handling: first Ctrl+C -> SIGTERM all; second -> SIGKILL all.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	var wg sync.WaitGroup
	var mu sync.Mutex
	cmds := make([]*exec.Cmd, 0, len(services))

	// Cleanup function removes all routes on exit.
	cleanup := func() {
		for _, si := range services {
			_ = store.RemoveRoute(si.domain)
		}
	}
	defer cleanup()

	// Start each service.
	for _, si := range services {
		cmd := exec.Command("sh", "-c", si.svc.Cmd)
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("PORT=%d", si.port),
			"HOST=127.0.0.1",
		)
		cmd.Stdout = newPrefixWriter(si.prefix, os.Stdout)
		cmd.Stderr = newPrefixWriter(si.prefix, os.Stderr)

		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "%sfailed to start: %v\n", si.prefix, err)
			continue
		}

		_ = store.UpdateRoute(si.domain, func(r *config.Route) {
			r.PID = cmd.Process.Pid
		})

		mu.Lock()
		cmds = append(cmds, cmd)
		mu.Unlock()

		wg.Add(1)
		go func(si serviceInfo, cmd *exec.Cmd) {
			defer wg.Done()
			if err := cmd.Wait(); err != nil {
				fmt.Fprintf(os.Stderr, "%sexited: %v\n", si.prefix, err)
			}
		}(si, cmd)
	}

	// Wait for signal or all processes to exit.
	allDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allDone)
	}()

	select {
	case <-sigChan:
		fmt.Println("\nStopping all services...")
		mu.Lock()
		for _, c := range cmds {
			if c.Process != nil {
				_ = c.Process.Signal(syscall.SIGTERM)
			}
		}
		mu.Unlock()

		// Second signal: force kill.
		select {
		case <-sigChan:
			fmt.Println("\nForce killing all services...")
			mu.Lock()
			for _, c := range cmds {
				if c.Process != nil {
					_ = c.Process.Kill()
				}
			}
			mu.Unlock()
			<-allDone
		case <-allDone:
		}
	case <-allDone:
	}

	return nil
}

// prefixWriter wraps an io.Writer, prepending a prefix to each line of output.
// It buffers incomplete lines to prevent interleaving from concurrent services.
type prefixWriter struct {
	prefix string
	out    io.Writer
	mu     sync.Mutex
	buf    []byte
}

func newPrefixWriter(prefix string, out io.Writer) *prefixWriter {
	return &prefixWriter{prefix: prefix, out: out}
}

func (pw *prefixWriter) Write(p []byte) (int, error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	total := len(p)
	pw.buf = append(pw.buf, p...)

	for {
		idx := -1
		for i, b := range pw.buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}

		line := pw.buf[:idx+1]
		fmt.Fprintf(pw.out, "%s%s", pw.prefix, line)
		pw.buf = pw.buf[idx+1:]
	}

	return total, nil
}
