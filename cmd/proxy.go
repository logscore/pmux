package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/logscore/roxy/internal/platform"
	"github.com/logscore/roxy/internal/proxy"
)

type ProxyOptions struct {
	TLS       bool
	HTTPPort  int
	HTTPSPort int
	DNSPort   int
	Detach    bool
}

// ProxyStart launches the proxy as a background daemon.
func ProxyStart(opts ProxyOptions) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	if proxy.IsRunning(paths.ConfigDir) {
		fmt.Println("proxy is already running")
		return nil
	}

	return proxyStartDaemon(opts, paths)
}

// proxyStartDaemon re-execs the binary as a detached "proxy start" process.
func proxyStartDaemon(opts ProxyOptions, paths platform.Paths) error {
	// Ensure config and logs dirs exist
	if err := os.MkdirAll(paths.ConfigDir, 0755); err != nil {
		return err
	}
	logsDir := LogsDir(paths.ConfigDir)
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return err
	}

	// Open log file for the proxy daemon
	logPath := filepath.Join(logsDir, "proxy.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to create proxy log file: %w", err)
	}
	defer logFile.Close()

	// Re-exec ourselves with the "proxy start" subcommand in a detached process.
	// Pass --no-detach so the child runs in foreground (it IS the daemon).
	args := []string{"proxy", "start", "--no-detach"}
	if opts.TLS {
		args = append(args, "--tls")
	}
	if opts.HTTPPort != 0 {
		args = append(args, "--http-port", fmt.Sprintf("%d", opts.HTTPPort))
	}
	if opts.HTTPSPort != 0 {
		args = append(args, "--https-port", fmt.Sprintf("%d", opts.HTTPSPort))
	}
	if opts.DNSPort != 0 {
		args = append(args, "--dns-port", fmt.Sprintf("%d", opts.DNSPort))
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	cmd := exec.Command(exePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	return nil
}

// ProxyRun runs the proxy in the foreground (used by the daemon, or when --detach is not set).
func ProxyRun(opts ProxyOptions) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	if err := os.MkdirAll(paths.ConfigDir, 0755); err != nil {
		return err
	}

	dnsPort := opts.DNSPort
	if dnsPort == 0 {
		dnsPort = 53
	}

	// Ensure resolver is configured for the correct DNS port
	if !platform.ResolverConfigured(p, paths, dnsPort) {
		if err := platform.ConfigureResolver(p, paths, dnsPort); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to configure DNS resolver: %v\n", err)
		}
	}

	srv := proxy.New(proxy.Options{
		HTTPPort:   opts.HTTPPort,
		HTTPSPort:  opts.HTTPSPort,
		DNSPort:    opts.DNSPort,
		TLS:        opts.TLS,
		CertsDir:   paths.CertsDir,
		RoutesFile: paths.RoutesFile,
	})

	if err := proxy.WritePidFile(paths.ConfigDir); err != nil {
		return fmt.Errorf("failed to write pid file: %w", err)
	}
	defer proxy.RemovePidFile(paths.ConfigDir)

	printProxyStatus(opts)

	return srv.Run()
}

// ProxyStop stops the proxy daemon.
func ProxyStop() error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	pid := proxy.ReadPid(paths.ConfigDir)
	if pid == 0 {
		fmt.Println("proxy is not running")
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		proxy.RemovePidFile(paths.ConfigDir)
		return fmt.Errorf("process not found: %w", err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		proxy.RemovePidFile(paths.ConfigDir)
		return fmt.Errorf("failed to stop proxy: %w", err)
	}

	proxy.RemovePidFile(paths.ConfigDir)
	fmt.Println("proxy stopped")
	return nil
}

// ProxyRestart stops the running proxy (if any) and starts a new one.
func ProxyRestart(opts ProxyOptions) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	if proxy.IsRunning(paths.ConfigDir) {
		if err := ProxyStop(); err != nil {
			return fmt.Errorf("failed to stop proxy: %w", err)
		}
		// Wait for the old process to fully exit
		for range proxy.ProxyStartRetries {
			time.Sleep(proxy.ProxyStartRetryInterval)
			if !proxy.IsRunning(paths.ConfigDir) {
				break
			}
		}
	}

	if opts.Detach {
		if err := proxyStartDaemon(opts, paths); err != nil {
			return err
		}
		// Wait for it to come up
		for range proxy.ProxyStartRetries {
			time.Sleep(proxy.ProxyStartRetryInterval)
			if proxy.IsRunning(paths.ConfigDir) {
				break
			}
		}
		if !proxy.IsRunning(paths.ConfigDir) {
			return fmt.Errorf("proxy failed to start after restart")
		}
		fmt.Println("proxy restarted")
		PrintNonStandardPortNotice(opts)
		return nil
	}

	// Foreground mode
	return ProxyRun(opts)
}

// printProxyStatus prints the proxy configuration on foreground start.
func printProxyStatus(opts ProxyOptions) {
	PrintNonStandardPortNotice(opts)
}

// PrintNonStandardPortNotice warns users when the proxy port is non-standard,
// since they will need to include the port when accessing dev servers.
func PrintNonStandardPortNotice(opts ProxyOptions) {
	httpPort := opts.HTTPPort
	if httpPort == 0 {
		httpPort = 80
	}

	if httpPort != 80 {
		fmt.Println()
		fmt.Printf("  note: proxy is running on port %d (non-standard)\n", httpPort)
		fmt.Printf("  access your dev servers at: http://<name>.test:%d\n", httpPort)
		fmt.Println()
	}
}
