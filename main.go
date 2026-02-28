package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/logscore/roxy/cmd"
	"github.com/logscore/roxy/pkg/config"
)

const usage = `roxy - dev server port multiplexer with subdomain routing

Usage:
  roxy run -a                     Run all services from roxy.yaml
  roxy run <service>             Run a single service from roxy.yaml
  roxy run "<command>" [flags]   Run command with auto port/domain
  roxy list                      List active routes
  roxy stop <id|domain>...       Stop one or more routes
  roxy stop -a [--remove-dns]    Stop all routes and proxy
  roxy logs <id|domain>          Tail logs for a detached process
  roxy proxy <start|stop|restart|status|logs>  Manage the proxy server

Run flags:
  -d, --detach           Run in the background (detached mode)
  -p, --port <n>         Pin to an exact port (default: random)
  -n, --name <name>      Override subdomain name
  --tls                  Enable HTTPS for this process
  --listen-port <n>      TCP mode: proxy listens on this port, forwards to service

Stop flags:
  -a, --all          Stop all routes and the proxy
  --remove-dns       Also remove DNS resolver configuration (with -a)

Proxy flags:
  -d, --detach			 Run proxy in the background (default for proxy start|stop)
  --no-detach            Run proxy in the foreground
  --proxy-port <n>       HTTP proxy port (default: 80)
  --https-port <n>       HTTPS proxy port (default: 443)
  --dns-port <n>         DNS server port (default: 1299)
  --tls                  Enable HTTPS`

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		fmt.Println(usage)
		os.Exit(0)
	}

	var err error

	switch args[0] {
	case "run":
		err = runCommand(args[1:])

	case "list":
		err = cmd.List()

	case "stop":
		err = stopCommand(args[1:])

	case "logs":
		if len(args) < 2 {
			die(logsUsage)
		}
		err = cmd.Logs(args[1])

	case "proxy":
		err = proxyCommand(args[1:])

	case "help", "--help", "-h":
		fmt.Println(usage)
		os.Exit(0)

	default:
		die("unknown command: " + args[0] + "\n\n" + usage)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

const runUsage = `Usage:
  roxy run -a                    Run all services from roxy.yaml
  roxy run <service>             Run a single service from roxy.yaml
  roxy run "<command>" [flags]   Run command with auto port/domain

Flags:
  -a, --all              Run all services from roxy.yaml
  -d, --detach           Run in the background (detached mode)
  -p, --port <n>         Start scanning from this port (default: 3000)
  -n, --name <name>      Override subdomain name
  --tls                  Enable HTTPS for this process
  --listen-port <n>      TCP mode: proxy listens on this port, forwards to service`

const stopUsage = `Usage:
  roxy stop <id|domain>...       Stop one or more routes
  roxy stop -a [--remove-dns]    Stop all routes and proxy

Flags:
  -a, --all          Stop all routes and the proxy
  --remove-dns       Also remove DNS resolver configuration (with -a)`

const logsUsage = `Usage:
  roxy logs <id|domain>          Tail logs for a detached process`

const proxyUsage = `Usage:
  roxy proxy start [flags]       Start the proxy server
  roxy proxy stop                Stop the proxy server
  roxy proxy restart [flags]     Restart the proxy server
  roxy proxy status              Show proxy status
  roxy proxy logs [-a] [-w]      View proxy logs

Flags:
  -d, --detach           Run proxy in the background (default)
  --no-detach            Run proxy in the foreground
  --proxy-port <n>       HTTP proxy port (default: 80)
  --https-port <n>       HTTPS proxy port (default: 443)
  --dns-port <n>         DNS server port (default: 1299)
  --tls                  Enable HTTPS`

func runCommand(args []string) error {
	opts := cmd.RunOptions{}
	runAll := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-a", "--all":
			runAll = true
		case "-p", "--port":
			if i+1 >= len(args) {
				die("--port requires a value")
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil {
				die("invalid port: " + args[i])
			}
			opts.StartPort = p
		case "-n", "--name":
			if i+1 >= len(args) {
				die("--name requires a value")
			}
			i++
			opts.Name = args[i]
		case "--tls":
			opts.TLS = true
		case "-d", "--detach":
			opts.Detach = true
		case "--log-file":
			if i+1 >= len(args) {
				die("--log-file requires a value")
			}
			i++
			opts.LogFile = args[i]
		case "--id":
			if i+1 >= len(args) {
				die("--id requires a value")
			}
			i++
			opts.ID = args[i]
		case "--listen-port":
			if i+1 >= len(args) {
				die("--listen-port requires a value")
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil {
				die("invalid listen port: " + args[i])
			}
			opts.ListenPort = p
		default:
			if opts.Command == "" {
				opts.Command = args[i]
			} else {
				die("unexpected argument: " + args[i])
			}
		}
	}

	// roxy run -a / roxy run --all
	if runAll {
		cfg, _ := config.LoadRoxyYAML(".")
		if cfg == nil {
			die("no roxy.yaml found in current directory")
		}
		return cmd.RunAll(cfg, opts.Detach)
	}

	// roxy run (no args) -> show usage
	if opts.Command == "" && len(args) == 0 {
		die(runUsage)
	}

	// If no command given, show usage.
	if opts.Command == "" {
		die(runUsage)
	}

	// Single word (no spaces) -> must be a service name from roxy.yaml.
	if !strings.Contains(opts.Command, " ") {
		cfg, _ := config.LoadRoxyYAML(".")
		if cfg == nil {
			die(fmt.Sprintf("unknown service %q (no roxy.yaml found)\n\n%s", opts.Command, runUsage))
		}
		if svc, ok := cfg.Services[opts.Command]; ok {
			return cmd.RunService(opts.Command, svc, opts.Detach)
		}
		names := make([]string, 0, len(cfg.Services))
		for name := range cfg.Services {
			names = append(names, name)
		}
		die(fmt.Sprintf("unknown service %q in roxy.yaml (available: %s)", opts.Command, strings.Join(names, ", ")))
	}

	return cmd.Run(opts)
}

func stopCommand(args []string) error {
	opts := cmd.StopOptions{}

	for i := range args {
		switch args[i] {
		case "-a", "--all":
			opts.All = true
		case "--remove-dns":
			opts.RemoveDNS = true
		default:
			opts.Targets = append(opts.Targets, args[i])
		}
	}

	if !opts.All && len(opts.Targets) == 0 {
		die(stopUsage)
	}

	return cmd.Stop(opts)
}

// proxyCommand handles proxy subcommands.
func proxyCommand(args []string) error {
	if len(args) == 0 {
		die(proxyUsage)
	}

	subArgs := args[1:]

	// Subcommands that have their own flags or no flags at all
	switch args[0] {
	case "stop":
		return cmd.ProxyStop()
	case "status":
		return cmd.ProxyStatus()
	case "logs":
		printAll := false
		watch := false
		for _, a := range subArgs {
			switch a {
			case "-a", "--all":
				printAll = true
			case "-w", "--watch":
				watch = true
			}
		}
		return cmd.ProxyLogs(printAll, watch)
	}

	// Parse flags for start/restart
	opts := cmd.ProxyOptions{
		HTTPPort:  80,
		HTTPSPort: 443,
		DNSPort:   1299,
		Detach:    true, // default to detached for start/restart
	}

	for i := 0; i < len(subArgs); i++ {
		switch subArgs[i] {
		case "--tls":
			opts.TLS = true
		case "-d", "--detach":
			opts.Detach = true
		case "--no-detach":
			opts.Detach = false
		case "--proxy-port", "--http-port":
			if i+1 >= len(subArgs) {
				die("--proxy-port requires a value")
			}
			i++
			p, err := strconv.Atoi(subArgs[i])
			if err != nil {
				die("invalid port: " + subArgs[i])
			}
			opts.HTTPPort = p
		case "--https-port":
			if i+1 >= len(subArgs) {
				die("--https-port requires a value")
			}
			i++
			p, err := strconv.Atoi(subArgs[i])
			if err != nil {
				die("invalid port: " + subArgs[i])
			}
			opts.HTTPSPort = p
		case "--dns-port":
			if i+1 >= len(subArgs) {
				die("--dns-port requires a value")
			}
			i++
			p, err := strconv.Atoi(subArgs[i])
			if err != nil {
				die("invalid port: " + subArgs[i])
			}
			opts.DNSPort = p
		default:
			die("unexpected argument: " + subArgs[i])
		}
	}

	switch args[0] {
	case "start":
		if !opts.Detach {
			return cmd.ProxyRun(opts)
		}
		if err := cmd.ProxyStart(opts); err != nil {
			return err
		}
		cmd.PrintNonStandardPortNotice(opts)
		return nil
	case "restart":
		return cmd.ProxyRestart(opts)
	default:
		die(fmt.Sprintf("unknown proxy command: %s\n\n%s", args[0], proxyUsage))
		return nil
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
