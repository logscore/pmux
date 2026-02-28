package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/logscore/roxy/cmd"
)

const usage = `roxy - dev server port multiplexer with subdomain routing

Usage:
  roxy run "<command>" [flags]   Run command with auto port/domain
  roxy list                      List active routes
  roxy stop <id|domain>...       Stop one or more routes
  roxy stop -a [--remove-dns]    Stop all routes and proxy
  roxy logs <id|domain>          Tail logs for a detached process
  roxy proxy <start|stop|restart> [flags]  Manage the proxy server

Run flags:
  -d, --detach       Run in the background (detached mode)
  -p, --port <n>     Start scanning from this port (default: 3000)
  -n, --name <name>  Override subdomain name
  --tls              Enable HTTPS for this process

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
			die("usage: roxy logs <id|domain>")
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

func runCommand(args []string) error {
	opts := cmd.RunOptions{
		StartPort: 3000,
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
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
		default:
			if opts.Command == "" {
				opts.Command = args[i]
			} else {
				die("unexpected argument: " + args[i])
			}
		}
	}

	if opts.Command == "" {
		die("usage: roxy run \"<command>\" [-p <number>] [-n <name>] [--tls] [-d]")
	}

	return cmd.Run(opts)
}

func stopCommand(args []string) error {
	opts := cmd.StopOptions{}

	for i := 0; i < len(args); i++ {
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
		die("usage: roxy stop <id|domain>... or roxy stop -a [--remove-dns]")
	}

	return cmd.Stop(opts)
}

// proxyCommand handles proxy subcommands.
func proxyCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: roxy proxy <start|stop|restart>")
	}

	opts := cmd.ProxyOptions{
		HTTPPort:  80,
		HTTPSPort: 443,
		DNSPort:   1299,
		Detach:    true, // default to detached for start/restart
	}

	subArgs := args[1:]

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
	case "stop":
		return cmd.ProxyStop()
	case "restart":
		return cmd.ProxyRestart(opts)
	default:
		return fmt.Errorf("unknown proxy command: %s (expected start, stop, or restart)", args[0])
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
