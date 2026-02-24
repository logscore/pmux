package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/logscore/pmux/internal/domain"
	"github.com/logscore/pmux/internal/platform"
	"github.com/logscore/pmux/internal/port"
	"github.com/logscore/pmux/internal/process"
	"github.com/logscore/pmux/internal/proxy"
	"github.com/logscore/pmux/pkg/config"
)

type RunOptions struct {
	Command   string
	StartPort int
	Name      string
	TLS       bool
	Detach    bool
}

// LogsDir returns the path to the logs directory.
func LogsDir(configDir string) string {
	return filepath.Join(configDir, "logs")
}

func Run(opts RunOptions) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	// Ensure config directory exists
	if err := os.MkdirAll(paths.ConfigDir, 0755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}

	// Auto-configure DNS resolver on first run
	if !platform.ResolverConfigured(p, paths) {
		if err := platform.ConfigureResolver(p, paths); err != nil {
			return fmt.Errorf("failed to configure DNS resolver: %w", err)
		}
		fmt.Println("done - DNS configured")
		fmt.Println()
	}

	// Auto-start proxy if not running
	if !proxy.IsRunning(paths.ConfigDir) {
		fmt.Println("starting proxy...")
		if err := ProxyStart(ProxyOptions{HTTPPort: 80, TLS: true, HTTPSPort: 443}); err != nil {
			return fmt.Errorf("failed to start proxy: %w", err)
		}
		for i := 0; i < proxy.ProxyStartRetries; i++ {
			time.Sleep(proxy.ProxyStartRetryInterval)
			if proxy.IsRunning(paths.ConfigDir) {
				break
			}
		}
		if !proxy.IsRunning(paths.ConfigDir) {
			return fmt.Errorf("proxy failed to start -- check if port 80 is in use")
		}
	}

	// Auto-trust CA cert on first --tls use
	if opts.TLS {
		caCertPath := paths.CertsDir + "/ca-cert.pem"
		if !platform.CATrusted(p, caCertPath) {
			// The proxy generates certs on startup, wait for the CA cert to appear
			for i := 0; i < 30; i++ {
				if _, err := os.Stat(caCertPath); err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if _, err := os.Stat(caCertPath); err == nil {
				if err := platform.TrustCA(p, caCertPath); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to trust CA cert: %v\n", err)
					fmt.Fprintf(os.Stderr, "HTTPS may show certificate warnings in browsers.\n\n")
				} else {
					fmt.Println("done - CA certificate trusted")
					fmt.Println()
				}
			}
		}
	}

	// Find available port (checks both OS and routes.json)
	assignedPort, err := port.Find(opts.StartPort, paths.RoutesFile)
	if err != nil {
		return fmt.Errorf("failed to find available port: %w", err)
	}

	// Generate domain
	dom, err := domain.Generate(opts.Name)
	if err != nil {
		return fmt.Errorf("failed to generate domain: %w", err)
	}

	store := config.NewStore(paths.RoutesFile)

	scheme := "http"
	if opts.TLS {
		scheme = "https"
	}

	// Detached mode: re-exec ourselves without -d, in a new session with log output
	if opts.Detach {
		return runDetached(opts, paths, dom, assignedPort, scheme)
	}

	fmt.Printf("port assigned: %d\n", assignedPort)
	fmt.Printf("domain: %s\n", dom)
	fmt.Printf("proxy configured\n")
	fmt.Fprintf(os.Stdout, "\nRunning at: %s://%s\n\n", scheme, dom)

	return process.Run(opts.Command, assignedPort, dom, opts.TLS, store, paths.ConfigDir, "")
}

func runDetached(opts RunOptions, paths platform.Paths, dom string, assignedPort int, scheme string) error {
	logsDir := LogsDir(paths.ConfigDir)
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return fmt.Errorf("failed to create logs dir: %w", err)
	}

	logPath := filepath.Join(logsDir, dom+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}
	defer logFile.Close()

	// Re-exec: pmux run "<command>" [flags] (without --detach/-d)
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find executable: %w", err)
	}

	args := []string{"run", opts.Command}
	args = append(args, "--port", fmt.Sprintf("%d", assignedPort))
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.TLS {
		args = append(args, "--tls")
	}
	// Pass the log file path so the child can record it in the route
	args = append(args, "--log-file", logPath)

	cmd := exec.Command(exePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start detached process: %w", err)
	}

	fmt.Printf("port assigned: %d\n", assignedPort)
	fmt.Printf("domain: %s\n", dom)
	fmt.Printf("logs: %s\n", logPath)
	fmt.Fprintf(os.Stdout, "\nRunning (detached) at: %s://%s  [pid %d]\n", scheme, dom, cmd.Process.Pid)

	return nil
}
