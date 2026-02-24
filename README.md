# pmux

A CLI tool that wraps dev servers, assigns ports dynamically via the `PORT` environment variable, and configures subdomain routing via a built-in reverse proxy and DNS server. Zero external dependencies.

```
pmux run "bun dev"
```

Automatically finds an available port, injects it as `PORT`, and routes `feat-auth.my-app.test` to your dev server.

## How it works

```
*.test DNS (built-in)  -->  pmux proxy (:80)  -->  Dev Server (PORT env var)
```

Everything is built into the `pmux` binary:
- **DNS server** resolves all `*.test` domains to `127.0.0.1`
- **HTTP reverse proxy** routes each subdomain to the correct local port
- **TCP proxy** for raw TCP forwarding (databases, Redis, etc.)

No dnsmasq, no Caddy, no Nginx. Just `pmux`.

## Install

Requires Go 1.21+.

```bash
git clone https://github.com/anomalyco/pmux.git
cd pmux
make build
mv pmux /usr/local/bin/
```

### Cross-compile

```bash
make cross
```

## Usage

### Run a dev server

```bash
pmux run "npm run dev"
```

On first run, pmux will ask for your password once to configure DNS resolution for `.test` domains. After that, everything is automatic.

The domain is derived from your directory and git branch:
- **Root**: current directory name (e.g. `my-app`), worktree-aware
- **Subdomain**: current git branch (e.g. `feat-auth`)
- **Result**: `feat-auth.my-app.test`

Every run gets a subdomain, including `main`/`master`: `main.my-app.test`.
For non-git directories, a stable 5-character hash of the working directory is used as the subdomain.

#### Flags

```bash
pmux run "bun dev" --port 4000        # start scanning from port 4000
pmux run "cargo watch -x run" --name api  # override subdomain
pmux run "bun dev" --tls              # enable HTTPS (generates certs if needed)
```

### List active tunnels

```bash
pmux list
```

### Stop a tunnel

```bash
pmux stop feat-auth.my-app.test
```

### Proxy management

The proxy auto-starts when you run `pmux run`. You can also manage it manually:

```bash
pmux proxy start       # start as background daemon
pmux proxy run         # run in foreground (for debugging)
pmux proxy stop        # stop the daemon
```

### Teardown

```bash
pmux teardown              # stop everything, clear routes
pmux teardown --remove-dns # also remove DNS resolver config
```

## Development

```bash
make build    # compile
make run ARGS='proxy run'  # build and run
make dev      # rebuild on file changes (requires fswatch)
make clean    # remove build artifacts
make cross    # cross-compile all targets
```

### Project structure

```
pmux/
├── main.go                    # CLI entry point
├── cmd/                       # Command implementations
│   ├── run.go
│   ├── proxy.go
│   ├── list.go
│   ├── stop.go
│   └── teardown.go
├── internal/
│   ├── proxy/
│   │   ├── server.go          # HTTP reverse proxy + TCP proxy
│   │   └── certs.go           # Self-signed TLS cert generation
│   ├── dns/server.go          # Built-in DNS server (*.test -> 127.0.0.1)
│   ├── domain/generator.go    # Domain name from git branch + dir
│   ├── platform/platform.go   # OS detection + resolver config
│   ├── port/finder.go         # Available port scanner
│   └── process/spawn.go       # Process lifecycle + signal handling
└── pkg/config/config.go       # Routes file persistence
```

## License

MIT
