package process

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/logscore/pmux/internal/proxy"
	"github.com/logscore/pmux/pkg/config"
)

// Run spawns the command with PORT set, tracks the route, and
// handles cleanup on exit or signal.
func Run(cmdStr string, port int, domain string, tlsEnabled bool, store *config.Store, configDir string) error {
	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Track route (the proxy watches routes.json for changes)
	if err := store.AddRoute(config.Route{
		Domain:  domain,
		Port:    port,
		Type:    "http",
		TLS:     tlsEnabled,
		Command: cmdStr,
		Created: time.Now(),
	}); err != nil {
		return fmt.Errorf("failed to register route: %w", err)
	}

	// Ensure cleanup on any exit path
	cleanup := func() {
		if err := store.RemoveRoute(domain); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to remove route: %v\n", err)
			return
		}
		fmt.Println("done - route removed")

		// Auto-stop proxy when last route exits
		routes, err := store.LoadRoutes()
		if err == nil && len(routes) == 0 {
			pid := proxy.ReadPid(configDir)
			if pid != 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					_ = proc.Signal(syscall.SIGTERM)
					proxy.RemovePidFile(configDir)
					fmt.Println("done - proxy stopped (no routes remaining)")
				}
			}
		}
	}
	defer cleanup()

	// Spawn child process
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"HOST=127.0.0.1",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	// Update route with PID (atomic — no gap where proxy sees no route)
	_ = store.UpdateRoute(domain, func(r *config.Route) {
		r.PID = cmd.Process.Pid
	})

	// Wait for either signal or process exit
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case sig := <-sigChan:
		fmt.Printf("\nReceived %v, cleaning up...\n", sig)
		_ = cmd.Process.Signal(sig)

		// If a second signal arrives during cleanup, force-kill the process
		select {
		case <-sigChan:
			fmt.Println("\nForce killing process...")
			_ = cmd.Process.Kill()
			<-done
		case <-done:
		}
		return nil

	case err := <-done:
		if err != nil {
			return fmt.Errorf("command exited with error: %w", err)
		}
		return nil
	}
}
