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
  porter list                      List active tunnels
  porter stop <domain>             Stop a specific tunnel
  porter logs <domain>             Tail logs for a detached process
  porter proxy start [flags]       Start the proxy daemon
  porter proxy run [flags]         Run the proxy in the foreground
  porter proxy stop                Stop the proxy daemon
  porter teardown [flags]          Stop everything and clean up

Run flags:
  -d, --detach     Run in the background (detached mode)
  --port <n>       Start scanning from this port (default: 3000)
  --name <name>    Override subdomain name
  --tls            Enable HTTPS for this server

Proxy flags:
  --tls            Enable HTTPS listener
  --http-port <n>  HTTP listen port (default: 80)
  --https-port <n> HTTPS listen port (default: 443)

Teardown flags:
  --remove-dns     Also remove DNS resolver configuration`

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
		if len(args) < 2 {
			die("usage: porter stop <domain>")
		}
		err = cmd.Stop(args[1])

	case "proxy":
		err = proxyCommand(args[1:])

	case "teardown":
		removeDNS := hasFlag(args[1:], "--remove-dns")
		err = cmd.Teardown(removeDNS)

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

func proxyCommand(args []string) error {
	if len(args) == 0 {
		die("usage: porter proxy <start|run|stop>")
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
	case "start":
		return cmd.ProxyStart(opts)
	case "run":
		return cmd.ProxyRun(opts)
	case "stop":
		return cmd.ProxyStop()
	default:
		die("unknown proxy command: " + args[0])
	}
	return nil
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
