# roxy

A CLI tool that lets you run multiple dev servers with a human readible domain name. No port conflicts, no cookie bleed across dev servers, just one simple tool.

```
roxy run "<dev command>"
```

Automatically finds an available port, injects it as `PORT`, and routes `feat-auth.my-app.test` to your dev server.

## How it works

```
*.test DNS (built-in)  -->  roxy proxy (:80)  -->  Dev Server (PORT env var)
```

Everything is built into the `roxy` binary:
- **DNS server** resolves all `*.test` domains to `127.0.0.1`
- **HTTP reverse proxy** routes each subdomain to the correct local port
- **TCP proxy** for raw TCP forwarding (databases, Redis, etc.)

## Install

### Quick install (recommended)

Downloads the latest pre-built binary for your platform:

```bash
curl -fsSL https://raw.githubusercontent.com/logscore/roxy/master/install.sh | bash
```

Or install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/logscore/roxy/master/install.sh | bash -s v1.0.0
```

### Build from source

Requires Go 1.21+.

```bash
git clone https://github.com/logscore/roxy.git
cd roxy
make build
sudo mv dist/roxy /usr/local/bin/
```

## Usage

### Run a dev server

```bash
roxy run "npm run dev"
# OR
roxy run -d "npm run dev"
```

On first run, roxy will ask for your password once to configure DNS resolution for `.test` domains. After that, everything is automatic.

The domain is derived from your directory and git branch:
- **Root**: current directory name (e.g. `my-app`), worktree-aware
- **Subdomain**: current git branch (e.g. `feat-auth`)
- **Result**: `feat-auth.my-app.test`

Every run gets a subdomain, including `main`/`master`: `main.my-app.test`.
For non-git directories, a stable 5-character hash of the working directory is used as the subdomain.

#### Flags

```bash
roxy run "bun dev" -p 4000            # start scanning from port 4000
roxy run "cargo watch -x run" -n api  # override subdomain
roxy run "bun dev" --tls              # enable HTTPS (generates certs if needed)
roxy run -d "bun dev"                 # runs in detached mode like docker
```

| Short | Long | Description |
|-------|------|-------------|
| `-d` | `--detach` | Run in the background (detached mode) |
| `-p` | `--port <n>` | Start scanning from this port (default: 3000) |
| `-n` | `--name <name>` | Override subdomain name |
|      | `--tls` | Enable HTTPS for this server |

### List active servers

```bash
roxy list
```

### Stop a server

```bash
# By url
roxy stop feat-auth.my-app.test

# By ID
roxy stop a2m4l
```

### Proxy management

The proxy auto-starts when you run `roxy run`, and spins down when the last roxy process stops.

### DNS management

We don't have commands for DNS management yet since the DNS server and proxy are ephemeral. Meaning when the last roxy session closes, both the proxy and the DNS server are cleaned up and removed.

### Nuke everything

```bash
roxy stop -a        # stop everything, clear routes. Like docker compose down, but for all the servers.
roxy stop -a -r     # also remove DNS resolver config
```

| Short | Long | Description |
|-------|------|-------------|
| `-a` | `--all` | Stop all routes and the proxy |
|      | `--remove-dns` | Also remove DNS resolver configuration (with `-a`) |

## Development

```bash
make build    # compile
make run ARGS='proxy run'  # build and run
make dev      # rebuild on file changes (requires fswatch)
make clean    # remove build artifacts
make cross    # cross-compile all targets
```

## License

MIT
