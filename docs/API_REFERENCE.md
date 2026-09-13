# API Reference

This document describes the public APIs of xray-cli's daemon control interface and exported Go types.

## Daemon Control API

The HTTP control API is available when running in daemon mode with `--daemon-addr` flag. All endpoints require Bearer token authentication via `Authorization: Bearer <token>` header, where `<token>` is read from `/usr/local/etc/xray-cli/control.token`.

### Endpoint Summary

| Method | Path | Input | Description |
|--------|------|-------|-------------|
| GET | `/health` | - | Health check; returns 200 OK if connected, 503 otherwise |
| GET | `/status` | - | Returns JSON with current status: connected, active_profile, uptime_s, bytes_in/out, reconnects |
| GET | `/profiles` | - | Returns available profiles list with emoji country flags; includes currently active profile |
| GET | `/ping` | - | Pings all profiles and returns latency (ms) and GeoIP country for each |
| GET | `/server-info` | - | Public IP, exit country, protocol, DNS server, IP leak status of active server |
| POST | `/connect` | `{"profile": "name"}` | Switch to named profile; requires valid profile name |
| POST | `/disconnect` | - | Terminate current tunnel connection |
| POST | `/refresh` | - | Re-fetch subscription profiles from remote URL |
| GET | `/settings` | - | Returns session-only connection settings, active profile, and lifecycle status |
| POST | `/settings` | `{"auto_connect": false}` | Updates one or both connection settings |
| POST | `/reconnect` | - | Stops and restarts the selected profile through the daemon controller |

### Connection Settings Response

```json
{
    "auto_connect": true,
    "auto_reconnect": true,
    "active_profile": "home",
    "status": "connected"
}
```

`POST /settings` accepts a partial JSON object containing `auto_connect`,
`auto_reconnect`, or both. An empty object and unknown fields are rejected with
`400 Bad Request`. Settings are session-only and revert to both enabled when
the daemon process starts. Enabling `auto_connect` while the daemon is idle
starts the selected profile; changing it while connected does not interrupt the
current tunnel.

The `status` field is one of `disconnected`, `connecting`, `reconnecting`,
`connected`, or `operation_failed`. `POST /reconnect` and `POST /connect` are
serialized with `POST /disconnect`; a new tunnel is not started until the
previous operation has completed.

### Status Response Format

```json
{
    "connected": true,
    "active_profile": "home",
    "status": "connected",
    "uptime_s": 3600,
    "bytes_in": 15728640,
    "bytes_out": 2097152,
    "reconnects": 1
}
```

### Profile Response Format

```json
{
    "profiles": [
        {
            "name": "home",
            "flag": ""
        },
        {
            "name": "work",
            "flag": "🇩🇪"
        }
    ],
    "active": "home"
}
```

## CLI Interface

### Command-line Flags

| Flag | Default | Type | Description |
|------|---------|------|-------------|
| `--link` | — | string | Direct XRay proxy link (vless://, vmess://, trojan://) |
| `--config` | — | string | Config file: `.txt` (single link), `.yaml` (multi-profile) |
| `--subscribe` | — | string | Subscription URL (HTTPS required) |
| `--profile` | — | string | Named profile from config or subscription |
| `--list-profiles` | false | bool | Print available profiles and exit |
| `--daemon-addr` | "" | string | HTTP control API bind address (e.g., 127.0.0.1:19099) |
| `--status` | "" | string | Start status server (health/metrics) at given address |
| `--tray` | true | bool | Enable macOS menu bar integration |
| `--dns` | — | string | DNS override: comma-separated IPs, routes queries through tunnel |
| `--verbose` | false | bool | Log bandwidth statistics every 10 seconds |
| `--log` | info | string | Log level: debug / info / warn / error |
| `--tls-insecure` | false | bool | Request insecure TLS; validation rejects this mode |
| `--max-reconnects` | 0 | int | Maximum reconnect attempts (0 = unlimited) |
| `--help` | — | — | Display help message and exit |

### Example Usage Patterns

```bash
# Single link with DNS override
sudo xray-cli --link "vless://user@host:443" --dns 1.1.1.1,8.8.8.8

# Multi-profile config
sudo xray-cli --config servers.yaml --profile work

# Daemon mode for autostart integration
sudo xray-cli --config servers.yaml --daemon-addr 127.0.0.1:19099

# List available profiles
sudo xray-cli --config servers.yaml --list-profiles

# Remote subscription (fetches from HTTPS URL)
sudo xray-cli --subscribe "https://example.com/subscription.txt"

# Combine daemon + status server
sudo xray-cli --config servers.yaml --daemon-addr 127.0.0.1:19099 --status 127.0.0.1:9999
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `XRAY_LINK` | - | Connection link used when `--link` is omitted |
| `XRAY_DEBUG_PPROF` | - | Set to a non-empty value to enable the loopback-only pprof server |
| `XRAY_DEBUG_PPROF_ADDR` | `127.0.0.1:6060` | Loopback address for the pprof server |

Subscription and endpoint caches are stored below the operating system's user
cache directory in a private `xray-cli-private` directory. The path is not
configurable through an environment variable.

## Error Handling

### Common Error Codes

| Code | Meaning | Resolution |
|------|---------|------------|
| EPERM | Permission denied (e.g., TUN device creation) | Run with `sudo` |
| ENOENT | Config file or link not found | Verify --config path or link syntax |
| EINVAL | Invalid proxy format | Check link URL format (vless://, vmess://, trojan://) |
| EACCES | Cannot acquire session lock | Kill existing xray-cli process |
| ECONNREFUSED | Remote subscription fetch failed | Verify HTTPS URL and network connectivity |

### Error Message Examples

```
error: cannot acquire VPN session: another instance is running
error: invalid link format: vless://user@host - missing protocol scheme
error: subscription requires HTTPS (HTTP allowed only for loopback)
error: route delete 0.0.0.0/1: route not found (previous SIGKILL left routes)
```

---

*For daemon API details, see `docs/architecture.md`.*
