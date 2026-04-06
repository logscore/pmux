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

Homebrew:

```bash
brew install logscore/tap/roxy
```

Linux/MacOS:

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
| `-p` | `--port <n>` | Sets the port for the process. Increments from that value if that port is taken |
| `-n` | `--name <name>` | Override subdomain name |
|      | `--tls` | Enable HTTPS for this server |
|      | `--public` | Expose via tunnel (requires configured provider) |

### Service config (`roxy.json`)

`roxy run -a` and `roxy run <service>` read `roxy.json` in the current directory. A JSON Schema is included at `roxy.schema.json`.

```json
{
  "$schema": "./roxy.schema.json",
  "services": {
    "web": {
      "cmd": "npm run dev",
      "tls": true,
      "port": 4000
    },
    "redis": {
      "cmd": "docker run --rm -p $PORT:6379 redis:7",
      "listen-port": 6379
    }
  }
}
```

`port` works like the CLI `--port` flag (starting port to scan).

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

The proxy auto-starts when you run `roxy run`. You can also manage it directly:

```bash
roxy proxy start              # start the proxy (detached by default)
roxy proxy stop               # stop the proxy
roxy proxy restart            # restart the proxy
roxy proxy start --no-detach  # run in the foreground (useful for debugging)
```

| Long | Description |
|------|-------------|
| `-d`, `--detach` | Run proxy in the background (default) |
| `--no-detach` | Run proxy in the foreground |
| `--proxy-port <n>` | HTTP proxy port (default: 80) |
| `--https-port <n>` | HTTPS proxy port (default: 443) |
| `--dns-port <n>` | DNS server port (default: 1299) |
| `--tls` | Enable HTTPS |

#### Privileged ports

On macOS, unprivileged processes can bind to any port including 80 and 443 if bound on 0.0.0.0, which we do so you can access your domain on the network (excluding the DNS server which is bound to :1299).

On Linux, ports below 1024 are restricted. If the proxy fails to bind to port 80, you have two options:

**Option 1**: Grant the binary permission to bind to low ports (recommended):

```bash
sudo setcap cap_net_bind_service=+ep $(which roxy)
```

**Option 2**: Use a non-standard proxy port:

```bash
roxy proxy start --proxy-port 8080
```

When using a non-standard proxy port, you'll need to include the port when accessing dev servers in your browser:

```
http://feat-auth.my-app.test:8080
```

The DNS server defaults to port 1299 to avoid conflicts with system DNS services (macOS `mDNSResponder` occupies both port 53 and 5353). To change this, you will need to run `roxy proxy start --dns-port <privileged port>`

### DNS management

The DNS server starts and stops with the proxy. The resolver is configured automatically on first run (requires sudo once to write to `/etc/resolver/test` on macOS or systemd-resolved config on Linux).

### Nuke everything

```bash
roxy stop -a              # stop everything, clear routes. Like docker compose down, but for all the servers.
roxy stop -a --remove-dns # also remove DNS resolver config
```

| Short | Long | Description |
|-------|------|-------------|
| `-a` | `--all` | Stop all routes and the proxy |
|      | `--remove-dns` | Also remove DNS resolver configuration (with `-a`) |

### Tunnels (public URLs)

Expose a local dev server to the internet with `--public`:

```bash
roxy run "npm run dev" --public
```

This requires a tunnel provider to be installed and configured. Run `roxy tunnel set` to choose one, then `roxy tunnel status` to confirm.

#### Supported providers

| Provider | Install |
|----------|---------|
| **ngrok** | [ngrok.com/download](https://ngrok.com/download) |
| **cloudflared** | [developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/) |
| **bore** | [github.com/ekzhang/bore](https://github.com/ekzhang/bore) — `cargo install bore-cli` |

#### Tunnel commands

```bash
roxy tunnel set     # choose a tunnel provider (interactive)
roxy tunnel status  # show configured provider and binary path
```

Once configured, use `--public` with a single service:

```bash
roxy run "bun dev" --public           # foreground with tunnel
roxy run -d "bun dev" --public        # detached; prints local + remote URLs
roxy run <service> --public           # named service from roxy.json
```

> `--public` cannot be used with `-a`/`--all`. Tunnel providers like ngrok limit you to one active tunnel, and subdomain routing breaks when traffic arrives under a different hostname. Run services individually if you need a public URL.

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
