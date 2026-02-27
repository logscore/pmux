package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/logscore/roxy/internal/platform"
	"github.com/logscore/roxy/internal/proxy"
)

type ProxyOptions struct {
	TLS       bool
	HTTPPort  int
	HTTPSPort int
}

// ProxyStart launches the proxy as a background daemon.
func ProxyStart(opts ProxyOptions) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	if proxy.IsRunning(paths.ConfigDir) {
		fmt.Println("proxy is already running")
		return nil
	}

	// Ensure config dir exists
	if err := os.MkdirAll(paths.ConfigDir, 0755); err != nil {
		return err
	}

	// Re-exec ourselves with the "proxy run" subcommand in a detached process
	args := []string{"proxy", "run"}
	if opts.TLS {
		args = append(args, "--tls")
	}
	if opts.HTTPPort != 0 {
		args = append(args, "--http-port", fmt.Sprintf("%d", opts.HTTPPort))
	}
	if opts.HTTPSPort != 0 {
		args = append(args, "--https-port", fmt.Sprintf("%d", opts.HTTPSPort))
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	cmd := exec.Command(exePath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	// fmt.Printf("proxy started (pid %d)\n", cmd.Process.Pid)
	return nil
}

// ProxyRun runs the proxy in the foreground (used by the daemon).
func ProxyRun(opts ProxyOptions) error {
	p := platform.Detect()
	paths := platform.GetPaths(p)

	if err := os.MkdirAll(paths.ConfigDir, 0755); err != nil {
		return err
	}

	srv := proxy.New(proxy.Options{
		HTTPPort:   opts.HTTPPort,
		HTTPSPort:  opts.HTTPSPort,
		TLS:        opts.TLS,
		CertsDir:   paths.CertsDir,
		RoutesFile: paths.RoutesFile,
	})

	if err := proxy.WritePidFile(paths.ConfigDir); err != nil {
		return fmt.Errorf("failed to write pid file: %w", err)
	}
	defer proxy.RemovePidFile(paths.ConfigDir)

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
