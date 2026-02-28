package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/logscore/roxy/internal/platform"
	"github.com/logscore/roxy/internal/proxy"
	"github.com/logscore/roxy/pkg/config"
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

	if err := proxy.WriteState(paths.ConfigDir, proxy.ProxyState{
		PID:       os.Getpid(),
		HTTPPort:  opts.HTTPPort,
		HTTPSPort: opts.HTTPSPort,
		DNSPort:   opts.DNSPort,
		TLS:       opts.TLS,
	}); err != nil {
		return fmt.Errorf("failed to write proxy state: %w", err)
	}

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

// ProxyStatus prints info about the proxy, DNS server, and TLS.
func ProxyStatus() error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	running := proxy.IsRunning(paths.ConfigDir)
	state := proxy.ReadState(paths.ConfigDir)

	fmt.Println()
	if running && state != nil {
		fmt.Printf("  proxy       running (pid %d)\n", state.PID)
		fmt.Printf("  http port   %d\n", state.HTTPPort)
		fmt.Printf("  https port  %d\n", state.HTTPSPort)
		fmt.Printf("  dns port    %d\n", state.DNSPort)
	} else if running {
		pid := proxy.ReadPid(paths.ConfigDir)
		fmt.Printf("  proxy       running (pid %d)\n", pid)
	} else {
		fmt.Printf("  proxy       not running\n")
	}

	// Log file
	logPath := filepath.Join(LogsDir(paths.ConfigDir), "proxy.log")
	if _, err := os.Stat(logPath); err == nil {
		fmt.Printf("  logs        %s\n", logPath)
	}

	// DNS resolver
	if _, err := os.ReadFile(paths.ResolverPath); err == nil {
		fmt.Printf("  resolver    %s\n", paths.ResolverPath)
	} else {
		fmt.Printf("  resolver    not configured\n")
	}

	// TLS
	caCertPath := filepath.Join(paths.CertsDir, "ca-cert.pem")
	if state != nil && state.TLS {
		if platform.CATrusted(p, caCertPath) {
			fmt.Printf("  tls         CA trusted\n")
		} else if _, err := os.Stat(caCertPath); err == nil {
			fmt.Printf("  tls         CA generated (not trusted)\n")
		}
	} else {
		fmt.Printf("  tls         disabled\n")
	}

	// Routes
	store := config.NewStore(paths.RoutesFile)
	routes, err := store.LoadRoutes()
	if err == nil {
		fmt.Printf("  routes      %d active\n", len(routes))
	}

	fmt.Println()
	return nil
}

// ProxyLogs prints the last 20 lines of the proxy log file, all lines with printAll,
// or tails the log live with watch.
func ProxyLogs(printAll bool, watch bool) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	logPath := filepath.Join(LogsDir(paths.ConfigDir), "proxy.log")

	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Errorf("no proxy log file found (is the proxy running?)")
	}

	content := string(data)
	if printAll {
		fmt.Print(content)
		if !watch {
			return nil
		}
	}

	if !printAll {
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
		if len(lines) > 20 {
			lines = lines[len(lines)-20:]
		}
		for _, line := range lines {
			fmt.Println(line)
		}
	}

	if !watch {
		return nil
	}

	// Tail: open file and seek to end, then poll for new data
	f, err := os.Open(logPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	for {
		n, err := io.Copy(os.Stdout, f)
		if err != nil {
			return err
		}
		if n == 0 {
			time.Sleep(200 * time.Millisecond)
		}
	}
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
