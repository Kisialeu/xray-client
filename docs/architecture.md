# Architecture

This document describes xray-cli's architecture, components, data flow, and design decisions.

## Design Goals

1. **Security**: DNS leak protection, secure authentication, private credential storage
2. **Reliability**: Auto-reconnect with exponential backoff, graceful shutdown handling
3. **Usability**: Multi-profile management, intuitive CLI, macOS menu bar integration
4. **Maintainability**: Clear separation of concerns, testable components, and explicit macOS integration boundaries

## Component Overview

### Package Structure

```
src/
├── main.go              # Entry point: flag parsing, profile loading, runtime orchestration
├── client.go            # TUN proxy management (tun2socks), XRay instance, connection logic
├── controller.go        # Internal daemon command types and stop coordination
├── daemon.go            # Headless VPN daemon, HTTP endpoints, and monitoring
├── tray_client_darwin.go# Tray HTTP client and menu actions
├── tray_darwin.go       # macOS menu bar implementation
├── config.go           # YAML parsing for multi-profile configs
├── interfaces.go       # Test seams for pipe and routing table operations
├── geoip.go            # Server location detection via ip-api.com batch API
├── ping.go             # TCP latency measurement for all profiles
├── subscription.go     # Remote subscription fetching, base64 link parsing, caching
├── health.go           # Connectivity verification (golang.org/x/net/proxy)
├── protection_darwin.go # Firewall rules via pfctl (macOS only)
├── dns_darwin.go       # DNS override for macOS SystemConfiguration
├── session_lock.go     # File-based locking to prevent concurrent sessions
├── metrics.go          # Tunnel traffic measurement instrumentation
└── pprof_debug.go      # Per-process pprof server for debugging
```

### Platform-Specific Files

The application is deliberately macOS-only. Darwin build files provide the
native implementations required by the TUN lifecycle, DNS management,
firewall protection, file picker, and menu-bar UI. There are no non-Darwin
fallback implementations.

| File | macOS-only | Purpose |
|------|------------|---------|
| `tray_darwin.go` | Yes | Menu bar integration using systray package |
| `protection_darwin.go` | Yes | PF firewall rule installation via Security Framework |
| `dns_darwin.go` | Yes | DNS override for macOS network services |
| `emoji_darwin.go` | Yes | Native emoji-to-PNG conversion for tray icons |
| `configpicker_darwin.go` | Yes | NSOpenPanel file picker dialog |

## Data Flow Diagrams

### Single Link Flow (CLI mode)

```
1. CLI: --link flag -> loadLink()
2. Profile validated by validateProfiles()
3. Session lock acquired by acquireSessionLock()
4. Client initialized: NewClientWithOpts(Config{...})
5. TUN device created: tun.NewTun("tun0", ...)
6. XRay instance built with a dynamically allocated localhost SOCKS port
7. TUN interface and split-default routes installed
8. Tunnel starts: vpn.Connect(link)
9. Metrics collection: readerMetrics wraps io.ReadWriteCloser
10. Background monitoring: monitorSession() (health check every 20s)
```

### Multi-Profile Flow (YAML config)

```
1. CLI: --config servers.yaml
2. YAML parsed: parseYAMLConfig() -> configFile struct
3. Profiles loaded and validated by validateProfiles()
4. Default profile selected or first entry used
5. Same flow as single link with additional profile switching support
```

### Subscription Flow

```
1. CLI: --subscribe URL OR subscription config field
2. Validate HTTPS requirement: subscriptionURL()
3. Remote fetch: fetchSubscriptionRemote(client, url) -> base64 content
4. Decoded links parsed to Profile list
5. DNS servers extracted from HTTP headers (X-DNS) or `#dns:` lines in raw body
6. Cached by SHA256 of URL for 7 days
7. If no new subscription, fallback to cached profiles
8. Profiles merged inline + subscription profiles with same names taking precedence
```

### Daemon Mode Flow

```
1. CLI starts daemon: --daemon-addr 127.0.0.1:19099
2. Control token created/loaded from controlTokenPath (mode 0600)
3. HTTP server started with controlHandler() wrapping mux.Handler
4. Tray client connects via http.Client with Token-based auth
5. Endpoints respond to commands; errors sent back via done channel
6. Daemon refreshes subscriptions in background if configured
7. On disconnect, cleanup runs (routes removed, DNS restored)
```

### Connection Settings and Lifecycle Control

`ConnectionSettings` is a typed, session-only model with both
`AutoConnect` and `AutoReconnect` enabled by default. The embedded tray and
daemon-backed tray render these values as flat checkable menu items because
the systray dependency does not expose submenus.

All lifecycle actions are submitted to the existing unbuffered controller
command channel. The controller is the only owner of the session cancel
function and waits for the current reconnect loop to finish before starting a
replacement. `Disconnect` therefore cancels the reconnect context as well as
the active Client.

The reconnect loop observes a controller-owned settings state. When
`AutoReconnect` is disabled, the loop exits after its current tunnel teardown
and any pending backoff wait is interrupted. Re-enabling it changes future
failures only; it does not create a second loop. A failed operation leaves the
shared status as `operation_failed` until a subsequent lifecycle action changes
it. Enabling `AutoConnect` while the controller is idle starts the selected
profile; changing it during an active tunnel does not interrupt that tunnel.

## Component Descriptions

### `client.go` - TUN Proxy Manager

Manages the tun2socks pipe and XRay core instance. Key responsibilities:

- Creates TUN device at 192.18.0.1/32 with split-default IPv4 routes to 0.0.0.0/1 and 128.0.0.0/1
- Builds XRay inbound (SOCKS) and outbound (proxy link) configurations
- Routes traffic through tunnel via pipe2socks abstraction
- Provides read-only bandwidth metrics (atomic counters)
- Exposes Connect() for connection state changes

**Security considerations:**
- `tls_insecure` is rejected during profile validation; trusted TLS is required
- DNS servers config overrides macOS resolver to prevent DNS leaks

### `daemon.go` - HTTP API Gateway and Background Operations

The daemon owns the authenticated HTTP handlers and routes mutating requests
to a serialized VPN command channel. It also runs the reconnecting VPN session,
periodic connectivity checks, subscription refreshes, and optional bandwidth
logging.

| Command | Method | Path | Auth | Description |
|---------|--------|------|-------|-------------|
| GET /health | GET | /health | Required | Health check (200 OK or 503) |
| GET /status | GET | /status | Required | Current status JSON |
| GET /profiles | GET | /profiles | Required | Available profiles list |
| GET /ping | GET | /ping | Required | Profile latencies and GeoIP |
| GET /server-info | GET | /server-info | Required | Active server metadata |
| POST /connect | POST | /connect | Required | Switch profile by name |
| POST /disconnect | POST | /disconnect | Required | Terminate session |
| POST /refresh | POST | /refresh | Required | Re-fetch subscription; returns 409 while protected mode is active |
| GET /settings | GET | /settings | Required | Read session-only connection settings |
| POST /settings | POST | /settings | Required | Update connection settings |
| POST /reconnect | POST | /reconnect | Required | Restart selected profile |

**Authentication:** Bearer token from control.token file (hex-encoded 32-byte random)
Protected against: CORS preflight, origin header leaks, oversized requests (4096 byte limit)

### `config.go` - YAML Configuration Parser

Supports two config formats:

**Format 1: Single link** (`.txt`)
- First non-comment, non-empty line is the proxy link

**Format 2: Multi-profile** (`.yaml`)
- Optional `subscription`: remote URL for additional profiles
- Optional `default`: default profile name if not specified in command
- Optional `dns`: override DNS servers on connect
- `profiles` list: named proxy configurations with TLS settings

### `geoip.go` - Country Detection via ip-api.com

Batch API approach:

1. Parse all host:port from profiles using extractHostPort()
2. Resolve to IPs (DNS lookup, cache-first)
3. Batch request to `https://ip-api.com/batch?fields=countryCode`
4. Cache results by hostname in map[string]string for 7 days
5. Map country codes to Unicode regional indicator symbols (U+1F1E6 base)

**Note:** IPv6 proxy endpoints not supported by ip-api.com; uses country from profile name fallback (BAVARIA->DE, SCHWEIZ->CH, ASTANA->KZ).

### `ping.go` - Latency Measurement

Parallel TCP handshake per profile:

1. Extract host:port for each profile
2. Concurrency-limited goroutines (max 8 concurrent pings)
3. Single TCP connect to port (5s timeout); measures round-trip
4. Results include latency, cached GeoIP country if available
5. No payload sent (pure handshake)

### `subscription.go` - Remote Profile Fetching

Fetches base64-encoded proxy link lists:

1. Validates HTTPS scheme; rejects HTTP except localhost
2. Checks for redirect loops (>5 redirects rejected)
3. Max body size: 1 MiB + header parsing overhead
4. Caches by SHA256 of URL in private temp directory (7-day expiry)
5. Parses links from base64 content using known schemes
6. Extracts DNS from HTTP header X-DNS or `#dns:` comment lines in raw body

### `session_lock.go` - Concurrent Session Prevention

Prevents multiple VPN instances:

1. Acquires advisory file lock on `/usr/local/etc/xray-cli/session.lock`
2. Fails with error if another process holds the lock
3. Released explicitly on graceful shutdown (SIGTERM/SIGINT)
4. Note: SIGKILL bypasses signal handler; routes may persist

### `interfaces.go` - Test Seams

Provides small interfaces for replacing the packet copy and route operations
in unit tests. They are test seams, not cross-platform implementations:

```go
type pipeIface interface {
    Copy(ctx context.Context, pipe io.ReadWriteCloser, socks5 string) error
}

type ipTable interface {
    Add(options route.Opts) error
    Delete(options route.Opts) error
}
```

Mock implementations in tests: `fakePipe`, `fakeRoutes` from helpers_test.go.

### `metrics.go` - Traffic Instrumentation

Non-blocking bandwidth measurement:

- Wraps io.ReadWriteCloser with atomic Int64 counters
- Uses lock-free read for BytesRead()/BytesWritten()
- Close delegates to wrapped ReadWriteCloser

### `health.go` - Connectivity Verification

Verifies proxy tunnel health post-connect:

```go
func (c *Client) checkConnectivity(ctx context.Context) error
```

Checks against golang.org/x/net/proxy SOCKS5 dialer, probes Google/Cloudflare endpoints. Returns error if tunnel is unusable.

### `control.go` - Token Authentication

Secure daemon communication:

- Control token: hex-encoded 32-byte random value (cryptographically secure rand)
- Stored at `/usr/local/etc/xray-cli/control.token` (mode 0600)
- Authenticated via Bearer token in Authorization header
- Protects against CORS, origin leaks, oversized payloads
- Uses subtle.ConstantTimeCompare to prevent timing attacks

### `main.go` - Entry Point and Orchestration

Coordinates all components:

1. Flag parsing (link/config/subscribe/profile/dns/status/tray/daemon address)
2. Profile loading (inline link, YAML, or subscription)
3. TLS settings (insecure TLS requests are rejected during validation)
4. DNS override priority: CLI flag > config file > subscription X-DNS / #dns:
5. Session lock acquisition
6. Last profile restoration from `/usr/local/etc/xray-cli/last-profile`
7. If daemon mode, create control token and call runDaemon()
8. Optionally start HTTP status server or bandwidth printer
9. Final path: runWithReconnect() with exponential backoff

## Security Considerations

### Authentication & Authorization

- Control API protected by per-session random token (hex 32 bytes)
- Bearer authentication via `Authorization: Bearer <token>` header
- Token file mode enforced at 0600; rejected if world-readable
- Origin header check prevents browser-based attacks
- Request body size limited to 4096 bytes

### DNS Protection

Two-layer approach:

1. macOS SystemConfiguration override on connect (restored on disconnect)
2. Flushes OS DNS cache at both transitions

Note: SIGKILL bypasses signal handler; DNS leak may persist until manual intervention.

### Credential Security

- Control token: never transmitted over unencrypted channels
- Subscription cache: private temp directory (0700), 7-day TTL, SHA256 key
- Runtime state is stored under `/usr/local/etc/xray-cli/`; subscription and
  endpoint caches use a private temporary directory selected by the process

### Error Handling & Recovery

- Auto-reconnect with exponential backoff (2s -> max 15s)
- Attempt limit configurable (--max-reconnects); unlimited by default
- Graceful shutdown on SIGTERM/SIGINT; cleanup on connect/disconnect
- SIGKILL limitation: route cleanup never runs; manual recovery needed

## Design Trade-offs

### TUN vs Redirect Mode

**TUN mode selected** over redirect (port forwarding) because:

1. All IP routing controlled by xray-cli (no DNS leak surface area)
2. Simpler for macOS firewall rule configuration
3. Better suited for single-tenant private networks

### Exponential Backoff Algorithm

```go
backoffDuration(attempt, initial=2s, maxDur=15s) {
    exp := 2^attempt
    return min(maxDur, exp * initial)
}
```

Avoids thundering herd but allows faster recovery for persistent issues.

### GeoIP via External API

Using `ip-api.com` instead of built-in geo database:

**Pros:**
- No local data storage (privacy-friendly)
- Single API call; simple batch implementation
- Regular updates handled by provider

**Cons:**
- External dependency (network failure = no country flag)
- Rate-limited by provider (acceptable for typical use)
- IPv6 limitation (batch API doesn't support /v4 requirement)

### macOS-only Tray and Protection

The tray, Cocoa file picker, emoji rendering, DNS override, firewall
protection, and privilege warning are implemented in `*_darwin.go` files. The
build script rejects non-Darwin hosts, so these integrations are required
parts of the supported binary rather than optional runtime fallbacks.

## Future Considerations

1. **Persistent GeoIP cache**: Reduce external API calls by caching longer
2. **Local DNS resolution**: For improved privacy and offline fallback
3. **Web UI**: REST-based admin panel for advanced users
4. **Plugin architecture**: Allow custom protocol handlers without recompilation
5. **TLS certificate verification**: Keep trusted certificate validation mandatory

---

*This documentation is auto-generated from code comments and best practices.*
