# pmux

A CLI tool that lets you run multiple dev servers with a human readible domain name. No port conflicts, no cookie bleed across dev servers, just one simple tool.

```
pmux run "<dev command>"
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

## Install

### Quick install (recommended)

Downloads the latest pre-built binary for your platform:

```bash
curl -fsSL https://raw.githubusercontent.com/logscore/pmux/master/install.sh | bash
```

Or install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/logscore/pmux/master/install.sh | bash -s v1.0.0
```

### Build from source

Requires Go 1.21+.

```bash
git clone https://github.com/logscore/pmux.git
cd pmux
make build
sudo mv dist/pmux /usr/local/bin/
```

## Usage

### Run a dev server

```bash
pmux run "npm run dev"
# OR
pmux run -d "npm run dev"
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
pmux run "bun dev" --port 4000            # start scanning from port 4000
pmux run "cargo watch -x run" --name api  # override subdomain
pmux run "bun dev" --tls                  # enable HTTPS (generates certs if needed)
pmux run -d "bun dev"                     # runs in detatched mode like docker
```

### List active servers

```bash
pmux list
```

### Stop a server

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

### DNS management

We don't have commands for DNS manangement yet since the DNS server and proxy are ephemeral. Meaning when the last pmux session closes, both the proxy and the DNS server are cleaned up and removed.

### Teardown

```bash
pmux teardown       # stop everything, clear routes. Like docker compose down, but for all the servers.
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

## License

MIT
