package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/logscore/porter/cmd"
)

const usage = `porter - dev server port multiplexer with subdomain routing

Usage:
  porter run "<command>" [flags]   Run command with auto port/domain
  porter list                      List active routes
  porter stop <id|domain>...       Stop one or more routes
  porter stop -a [--remove-dns]    Stop all routes and proxy
  porter logs <id|domain>          Tail logs for a detached process

Run flags:
  -d, --detach     Run in the background (detached mode)
  --port <n>       Start scanning from this port (default: 3000)
  --name <name>    Override subdomain name
  --tls            Enable HTTPS for this server

Stop flags:
  -a, --all        Stop all routes and the proxy
  --remove-dns     Also remove DNS resolver configuration (with -a)`

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
			die("usage: porter logs <id|domain>")
		}
		err = cmd.Logs(args[1])

	// Internal commands (not shown in help)
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
		case "--port":
			if i+1 >= len(args) {
				die("--port requires a value")
			}
			i++
			p, err := strconv.Atoi(args[i])
			if err != nil {
				die("invalid port: " + args[i])
			}
			opts.StartPort = p
		case "--name":
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
		die("usage: porter run \"<command>\" [--port <n>] [--name <name>] [--tls]")
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
		die("usage: porter stop <id|domain>... or porter stop -a")
	}

	return cmd.Stop(opts)
}

// proxyCommand handles internal proxy subcommands (not user-facing).
func proxyCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: porter proxy <run|stop>")
	}

	opts := cmd.ProxyOptions{
		HTTPPort:  80,
		HTTPSPort: 443,
	}

	subArgs := args[1:]
	opts.TLS = hasFlag(subArgs, "--tls")

	for i := 0; i < len(subArgs); i++ {
		switch subArgs[i] {
		case "--http-port":
			if i+1 >= len(subArgs) {
				die("--http-port requires a value")
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
		}
	}

	switch args[0] {
	case "run":
		return cmd.ProxyRun(opts)
	case "stop":
		return cmd.ProxyStop()
	default:
		return fmt.Errorf("unknown proxy command: %s", args[0])
	}
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
