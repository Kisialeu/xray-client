# xray-cli

XRay VPN client in Go.

**Features:**
- Auto-reconnect with exponential backoff (2 s → 60 s, capped, configurable attempt limit) — triggers on both initial connect failures **and** mid-session tunnel drops
- Multi-profile config support (`.yaml` with named profiles, or a plain `.txt` link file)
- macOS menu bar app (`--tray`) — icon changes on connect/disconnect, live ↓RX/↑TX in menu, profile switching from the menu
- HTTP status server — `/health` (Docker-friendly) and `/status` (JSON metrics)
- Bandwidth display — ↓RX/↑TX rate + totals every 10 s (`--verbose`)
- Thread-safe metrics via `sync/atomic`
- Native macOS file picker fallback when no `--link`/`--config`/`--list-profiles` is given (no-op on other platforms)

**Dependencies:** built on top of `github.com/goxray/core` (`route`, `tun`, `pipe2socks`) and `github.com/lilendian0x00/xray-knife` for the underlying XRay core integration.

---

## Build

Go source lives in `./src`. Run `build.sh` from the repo root (the directory containing `src/`).

```bash
chmod +x build.sh
./build.sh                  # native: current OS + arch, CGO enabled
./build.sh --arch arm64     # native OS, arm64
./build.sh --arch amd64     # native OS, amd64
```

Output: `./dist/<os>/xray-cli-<arch>`

- Native builds use your local Go toolchain with `CGO_ENABLED=1`, so `--tray` works when built on macOS.
- Cross-arch on the same OS is supported (e.g. macOS arm64 host → macOS amd64 binary), but cgo-dependent code (`--tray`) may fail to build without a matching cgo cross-toolchain for the target arch installed locally.
- There is no supported cross-OS path (e.g. building a darwin binary from Linux, or vice versa) through this script — `github.com/getlantern/systray` requires cgo/Cocoa on macOS, and that toolchain isn't available cross-OS.

Requires Go 1.25+ installed locally (`go env GOOS`/`GOARCH` determine the native target).

### Manual cross-compilation (single target, no `--tray`)

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o xray-cli-linux-amd64 ./src

# Linux arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o xray-cli-linux-arm64 ./src

# macOS amd64 (no --tray; CGO disabled)
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o xray-cli-darwin-amd64 ./src

# macOS arm64 (no --tray; CGO disabled)
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o xray-cli-darwin-arm64 ./src
```

`--tray` is macOS-only and requires cgo (`CGO_ENABLED=1`, built natively on macOS — cross-compiling cgo from Linux is not supported). On any non-darwin target, passing `--tray` causes the binary to panic immediately on startup (the non-darwin tray stub panics rather than silently ignoring the flag) — treat it as a hard error, not a no-op.

> `sudo` / `CAP_NET_ADMIN` is required at **runtime** (regardless of how the binary was built) to create a TUN device.

---

## Usage

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--link` | — | XRay connection link (`vless://…`, `vmess://…`, `trojan://…`, …) |
| `--config` | — | Config file: `.txt` (first non-comment line is the link) or `.yaml` (multi-profile) |
| `--profile` | — | Named profile to use from a `.yaml` config (default: the file's `default:` key, else first entry) |
| `--list-profiles` | `false` | Print available profiles from `--config` and exit |
| `--status` | `""` (disabled) | HTTP status server bind address, e.g. `127.0.0.1:9999` |
| `--verbose` | `false` | Log bandwidth stats every 10 s |
| `--log` | `info` | Log level: `debug` / `info` / `warn` / `error` |
| `--tray` | on for darwin, off elsewhere | Run as macOS menu bar app — panics on non-darwin |
| `--tls-insecure` | `false` | Allow self-signed TLS certificates |
| `--max-reconnects` | `0` | Max reconnect attempts (0 = unlimited) |
| `--help` | — | Show usage and exit |

`--link` takes precedence over the `XRAY_LINK` environment variable; the env var is only read if `--link` is not passed. `--status` is disabled by default — pass an explicit address to enable it; there is no default bind address.

If none of `--link`, `--config`, or `--list-profiles` is given, the binary opens a native macOS file picker to select a config file (no-op on non-macOS — falls straight through to the usage error below).

### Connecting with a direct link

```bash
sudo ./xray-cli --link "vless://..."
```

### Connecting with a config file

`.txt` config — first non-comment line is the link:
```
# my server
vless://user@host:443?...
```
```bash
sudo ./xray-cli --config server.txt
```

`.yaml` config — multiple named profiles:
```yaml
default: home   # optional; falls back to the first entry if omitted

profiles:
  - name: home
    link: "vless://..."
  - name: work
    link: "vmess://..."
    tls_insecure: true
```
```bash
sudo ./xray-cli --config servers.yaml --profile work
sudo ./xray-cli --config servers.yaml --list-profiles   # see what's available
```

### macOS menu bar

```bash
sudo ./xray-cli --config servers.yaml --tray
```

The icon in the menu bar switches between an outline circle (disconnected) and a filled circle (connected). The menu shows:
- Connection status + session duration
- Live RX / TX bandwidth and running totals
- All profiles from `--config`, with a checkmark on the active one — click any to switch
- Connect / Disconnect
- About… / Quit

Switching profiles, connecting, and disconnecting from the menu all go through a single serializing controller goroutine, so only one tunnel is ever active at a time even if menu items are clicked in quick succession.

Requires a binary built natively on macOS with cgo enabled (see [Build](#build)) — `--tray` on a non-darwin or CGO-disabled binary panics at startup rather than failing gracefully, so don't rely on it as a runtime feature check.

### Run in the background (macOS launchd)

Create `~/Library/LaunchAgents/com.xray-cli.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.xray-cli</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/xray-cli</string>
    <string>--tray</string>
    <string>--config</string>
    <string>/usr/local/etc/xray-cli/servers.yaml</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
```

```bash
launchctl load ~/Library/LaunchAgents/com.xray-cli.plist
```

> `launchd` runs this as your user, not root — TUN creation still needs elevated privileges, so in practice this pattern typically requires either a privileged helper or running the agent as a `LaunchDaemon` under root instead. Adjust to your environment; this plist is a starting point, not a drop-in privilege-safe config.

---

## HTTP endpoints

Disabled by default. Enable with `--status <addr>`, e.g. `--status 127.0.0.1:9999`.

### `GET /health`

Returns `200 OK` when the tunnel is up, `503` otherwise. Suitable for a Docker `HEALTHCHECK` or any external liveness probe.

```
$ curl http://localhost:9999/health
ok
```

### `GET /status`

```json
{
  "connected":  true,
  "uptime_s":   42,
  "bytes_in":   1048576,
  "bytes_out":  204800,
  "reconnects": 0
}
```

---

## How it works

1. Parses the XRay link (direct or from a `.txt`/`.yaml` config) and starts an XRay core SOCKS inbound on a free local port.
2. Waits for the inbound to accept TCP connections before proceeding (bounded poll, 5 s timeout) rather than assuming it's ready immediately after start.
3. Creates a TUN device and routes all system traffic through it (split-default routes: `0.0.0.0/1` + `128.0.0.0/1`), with an explicit route exception back to the XRay server itself through the original gateway.
4. Bridges TUN ↔ XRay SOCKS via `tun2socks`.
5. On disconnect or error, rolls back routes/TUN/XRay instance in reverse order and reconnects with exponential backoff (up to `--max-reconnects` attempts, 0 = unlimited).
6. In `--tray` mode, `systray` owns the main goroutine (a macOS Cocoa requirement); the reconnect loop and a dedicated controller goroutine run in the background, serializing connect/disconnect/switch so only one tunnel is ever active at a time.

### Known limitation: unclean shutdown leaves routes behind

If the process is killed with `SIGKILL` (`kill -9`) rather than `SIGTERM`/`SIGINT`, the rollback and route-cleanup code never runs — this is a fundamental limitation of `-9`, not a bug in this client. If you see `add route: ... file exists` on the next start, a previous instance likely didn't shut down cleanly:

```bash
ps aux | grep xray-cli            # find and kill any stale instances
sudo route delete 0.0.0.0/1
sudo route delete 128.0.0.0/1
```
