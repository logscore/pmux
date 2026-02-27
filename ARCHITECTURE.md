# Architecture: `roxy`

A CLI tool that wraps dev servers, assigns ports dynamically via `PORT` environment variable, and configures subdomain routing via a built-in reverse proxy and DNS server. Zero external dependencies beyond a single Go DNS library.

## Overview

```
roxy run "bun dev"
```

Automatically finds an available port, injects it via `PORT` and `HOST=127.0.0.1`, and routes `feat-auth.my-app.test` to `localhost:3001`.

## Architecture

```
┌─────────────────┐     ┌──────────────────┐     ┌──────────────────┐
│   *.test DNS    │────▶│   Built-in       │────▶│  Dev Server      │
│   (miekg/dns)   │     │   Proxy (:80)    │     │  (PORT env var)  │
│   port 53       │     │   + TLS (:443)   │     │  HOST=127.0.0.1  │
└─────────────────┘     └──────────────────┘     └──────────────────┘
        ▲                      ▲
        │                      │
   auto-configured        routes.json
   on first run           (file-watched)
   (one-time sudo)
```

No dnsmasq, no Caddy, no Nginx. Everything is built into the `roxy` binary.

## Commands

| Command | Description |
|---------|-------------|
| `roxy run "<command>"` | Run command with auto port/domain/proxy |
| `roxy list` | List active tunnels |
| `roxy stop <domain>` | Stop a specific tunnel |
| `roxy proxy start` | Start proxy as background daemon |
| `roxy proxy run` | Run proxy in foreground (debugging) |
| `roxy proxy stop` | Stop the proxy daemon |
| `roxy teardown` | Stop all tunnels, clear routes, stop proxy |
| `roxy teardown --remove-dns` | Also remove DNS resolver config |

## Per-Run Flow: `roxy run "<command>"`

1. Detect platform (darwin/linux)
2. Auto-configure DNS resolver if not already done (one-time sudo prompt)
3. Auto-start proxy daemon if not running (always with TLS enabled)
4. If `--tls` flag: auto-prompt to trust CA cert if not already trusted
5. Find first available port >= starting port (default 3000)
6. Generate domain: `<subdomain>.<root>.test`
   - Root: directory name (worktree-aware via `git rev-parse --git-common-dir`)
   - Subdomain: git branch name, or stable 5-char hash of cwd for non-git dirs
   - Every run gets a subdomain, including `main`/`master` branches
7. Register route in `routes.json` with `Created` timestamp
8. Spawn child process with `PORT=<port>` and `HOST=127.0.0.1` in environment
9. Update route with child PID (atomic update, no remove+add gap)
10. Wait for process exit or signal
11. Remove route from `routes.json`
12. If no routes remaining, auto-stop proxy daemon

## Key Design Decisions

### Built-in proxy instead of Caddy
Caddy caused issues with Caddyfile format, admin API path creation, and catch-all blocks intercepting routes. A `net/http/httputil.ReverseProxy` is simpler and has zero dependencies.

### Built-in DNS instead of dnsmasq
Using `github.com/miekg/dns` to resolve `*.test` → `127.0.0.1` and forward everything else upstream. Eliminates platform-specific Homebrew/apt installs.

### HOST=127.0.0.1 injection
Many frameworks (Node/Bun) bind to `[::1]` (IPv6 only) when given just a PORT. Injecting `HOST=127.0.0.1` forces IPv4 binding, matching what the proxy expects.

### Proxy dials `localhost` not `127.0.0.1`
The reverse proxy dials `localhost:<port>` so Go's dialer tries both IPv4 and IPv6, handling frameworks that ignore `HOST` and bind to `[::1]`.

### Per-run TLS
The `--tls` flag is per-run, not global. The proxy always starts with both `:80` and `:443` listeners, so any run can use `--tls` without restarting the proxy. The `TLS` field on each route controls which scheme is displayed to the user.

### Auto-stop proxy
When the last route is removed (process exits), the cleanup function checks if routes are empty and sends SIGTERM to the proxy daemon. This avoids orphaned proxy processes.

### Route file watching
The proxy polls `routes.json` every 500ms for changes (by mtime). This avoids needing an admin API or IPC mechanism — `roxy run` just writes the file and the proxy picks it up.

## DNS Resolution Setup

### macOS
Creates `/etc/resolver/test` containing `nameserver 127.0.0.1`. This tells macOS to send all `.test` queries to the built-in DNS server.

### Linux
Creates `/etc/systemd/resolved.conf.d/roxy.conf` with `DNS=127.0.0.1` and `Domains=~test`, then restarts `systemd-resolved`.

### Upstream discovery
On macOS: `scutil --dns`. On Linux: parse `/etc/resolv.conf`. Falls back to `8.8.8.8:53`.

## TLS Certificate Chain

Self-signed using `crypto/x509` (ECDSA P-256):
1. **CA cert**: `~/.config/roxy/certs/ca-cert.pem` — 10 year validity
2. **Server cert**: `~/.config/roxy/certs/server-cert.pem` — 1 year validity, wildcard `*.test` + `localhost` SANs
3. **CA trust**: On first `--tls` use, auto-prompts to add CA to OS trust store:
   - macOS: `sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain`
   - Linux: Copy to `/usr/local/share/ca-certificates/` + `update-ca-certificates`

## TCP Proxy

For raw TCP forwarding (databases, Redis, etc.):
- Route type `"tcp"` in `routes.json`
- Requires `listen_port` (separate from service `port`) to avoid bind conflicts
- Proxy listens on `127.0.0.1:<listen_port>`, forwards via `io.Copy` bidirectional to `localhost:<port>`

## Route Configuration

Routes are stored in `~/.config/roxy/routes.json`:

```json
[
  {
    "domain": "feat-auth.my-app.test",
    "port": 3001,
    "type": "http",
    "tls": false,
    "command": "bun dev",
    "pid": 12345,
    "created": "2026-02-24T10:30:00Z"
  }
]
```

## Cleanup and Lifecycle

| Signal/Event | Action |
|--------------|--------|
| SIGINT (Ctrl+C) | Forward to child, remove route, maybe stop proxy |
| SIGTERM | Forward to child, remove route, maybe stop proxy |
| Child exits normally | Remove route, maybe stop proxy |
| Child crashes | Remove route, report error, maybe stop proxy |

## File Structure

```
~/.config/roxy/
├── routes.json        # Active routes (file-watched by proxy)
├── proxy.pid          # Proxy daemon PID
└── certs/
    ├── ca-cert.pem    # Self-signed CA certificate
    ├── ca-key.pem     # CA private key
    ├── server-cert.pem # Wildcard *.test server certificate
    └── server-key.pem  # Server private key
```

## Project Structure

```
roxy/
├── main.go                    # CLI entry point (hand-rolled arg parser)
├── cmd/
│   ├── run.go                 # roxy run — auto-setup, spawn, cleanup
│   ├── proxy.go               # roxy proxy start/run/stop
│   ├── list.go                # roxy list
│   ├── stop.go                # roxy stop <domain>
│   └── teardown.go            # roxy teardown [--remove-dns]
├── internal/
│   ├── proxy/
│   │   ├── server.go          # HTTP/HTTPS reverse proxy + TCP proxy + route watching
│   │   └── certs.go           # CA + server cert generation (crypto/x509)
│   ├── dns/server.go          # Built-in DNS server (miekg/dns)
│   ├── domain/generator.go    # Domain from git branch + dir (worktree-aware)
│   ├── platform/platform.go   # OS detection, resolver config, CA trust
│   ├── port/finder.go         # Sequential port scanner
│   └── process/spawn.go       # Process lifecycle, signal forwarding, auto-stop
├── pkg/config/config.go       # Route struct + JSON file store
├── go.mod                     # github.com/logscore/roxy
└── Makefile                   # build, run, dev, clean, cross targets
```

## Dependencies

### Runtime
- Go 1.21+ (single binary, no runtime deps)

### Go modules
- `github.com/miekg/dns` — DNS server library (only external dependency)

## Known Limitations

- **Vite 6+**: Blocks unknown hosts. Users must add `.test` to `server.allowedHosts` in `vite.config.js`.
- **Framework flag injection**: Not yet implemented. Like portless, could auto-inject `--host 127.0.0.1 --port $PORT` for known frameworks (Vite, Astro, etc.) that ignore env vars.
- **No tests**: Zero test coverage currently.
