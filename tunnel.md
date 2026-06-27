# How the xray-cli tunnel works

This document explains, end to end, how a packet leaves your machine, gets
encrypted, and comes back — and which file, type, and function is
responsible for each step.

---

## 1. The three libraries involved

"XRay" is used casually as if it's one thing. This client is actually built
from three separate, independently-maintained projects:

| Library | Package(s) used | Responsibility |
|---|---|---|
| `github.com/goxray/core` | `network/tun`, `network/route`, `pipe2socks` | TUN device creation, OS routing table manipulation, and the packet-level bridge between the TUN device and a local SOCKS5 proxy (a `tun2socks` equivalent) |
| `github.com/lilendian0x00/xray-knife/v3` | `pkg/protocol`, `pkg/xray` | Parses connection links (`vless://`, `vmess://`, ...) into config structs and manages an xray-core instance's lifecycle (start, stop, SOCKS inbound configuration) |
| `github.com/xtls/xray-core` | `app/log`, `common/log` (used directly); the protocol engine itself (used transitively, via xray-knife) | The actual protocol implementations — VMess, VLESS, Trojan, Shadowsocks, Reality/TLS. This is the component doing real encryption and obfuscation. |

Only `xtls/xray-core`'s logging types (`xapplog.LogType`, `xcommlog.Severity`)
are imported directly by this client, in `client.go`'s `xRayLogLevel`
function, to map the client's own `slog` log level onto xray-core's internal
logger. Everything else from `xtls/xray-core` — the protocol engine — is
used transitively through `xray-knife`.

---

## 2. What happens to a packet

```
 Your apps (browser, etc.)
          |
          |  normal IP traffic, unaware anything is intercepted
          v
 +--------------------+
 |    TUN device       |  created by goxray/core/network/tun
 |  (utunN on macOS)   |  OS routes 0.0.0.0/1 + 128.0.0.0/1 here
 +---------+----------+
           |  raw IP packets
           v
 +--------------------+
 |    pipe2socks       |  reassembles packets into TCP/UDP streams
 |  (goxray/core)       |  goxray/core/pipe2socks
 +---------+----------+
           |  TCP stream over loopback (127.0.0.1:<free port>)
           v
 +--------------------+
 |   SOCKS5 inbound     |  started by xray-core, configured via
 |   (xray-core)         |  xray-knife's xray.Socks{} struct
 +---------+----------+
           |  in-process handoff inside xray-core, no extra socket
           v
 +--------------------+
 |   XRay outbound       |  VMess / VLESS / Trojan / Reality — whichever
 |   (xray-core)         |  protocol your link specifies; encrypts and sends
 +---------+----------+
           |  TLS / obfuscated traffic
           v
       Your VPN server
           |
           v
        Internet
```

The hop through SOCKS is local-loopback only — a few syscalls and memory
copies, not a network round-trip. It exists because xray-core exposes SOCKS
as its stable inbound API rather than a Go-native packet interface. This is
the same approach used by Clash, v2rayN, and sing-box's tun2socks mode.

---

## 3. Routing: split-default route + loop-prevention exception

Two separate `route.Opts` calls happen at connect time, and they solve
different problems.

**1. Split-default route**, added in `setupTunnel()`:

```go
route.Opts{IfName: ifc.Name(), Routes: c.cfg.RoutesToTUN}
```

where `RoutesToTUN` defaults to:

```go
DefaultRoutesToTUN = []*route.Addr{
    route.MustParseAddr("0.0.0.0/1"),
    route.MustParseAddr("128.0.0.0/1"),
}
```

These two `/1` routes together cover the entire IPv4 address space, and each
is more specific than the system's existing `0.0.0.0/0` default route. The OS
routing table prefers more specific matches, so this redirects all traffic
into the TUN device without deleting or replacing the original default
route — which makes rollback on disconnect a plain route deletion rather
than having to restore a previous default route value.

**2. Loop-prevention exception**, added immediately after, via
`xrayToGatewayRoute()`:

```go
func (c *Client) xrayToGatewayRoute() route.Opts {
    return route.Opts{
        Gateway: *c.cfg.GatewayIP,
        Routes:  []*route.Addr{route.MustParseAddr(c.xSrvIP.String() + "/32")},
    }
}
```

Without this, the encrypted packets xray-core sends to your VPN server's own
IP address would themselves match the split-default route above and get
captured back into the TUN device — which would route them to xray-core
again — which would send them to the VPN server again — an infinite loop.
This `/32` route sends traffic specifically to `c.xSrvIP` (resolved in
`createXrayProxy` via `net.ResolveIPAddr`) through the original physical
gateway (`c.cfg.GatewayIP`, discovered once at startup via
`gateway.DiscoverGateway()`), bypassing the TUN device for that one address.

---

## 4. Connection setup — `client.go`, `Client.Connect`

In execution order:

1. **`createXrayProxy(link)`** — builds `xray.Socks{Address: "127.0.0.1", Port: <free port>}`, calls `svc.CreateProtocol(link)` (xray-knife's link parser), `protocol.Parse()`, `protocol.ConvertToGeneralConfig()`, then `svc.MakeInstance(protocol)` to construct (not yet start) an xray-core instance. Also resolves the VPN server's IP via `net.ResolveIPAddr("ip", cfg.Address)` and stores it as `c.xSrvIP`, needed for the loop-prevention route above.
2. **`c.xInst.Start()`** — actually starts xray-core; the SOCKS inbound begins listening.
3. **`waitForInboundReady(addr, inboundReadyTimeout, inboundDialInterval)`** — polls `net.DialTimeout("tcp", addr, interval)` against the SOCKS address until a connection succeeds or `inboundReadyTimeout` (5s) elapses. This replaced an earlier fixed `time.Sleep(100ms)`, which was a race condition under load.
4. **`setupTunnel()`** — calls `tun.New("", 1500)` (empty name lets the OS assign `utunN` on macOS), `ifc.Up(c.cfg.TUNAddress, c.cfg.TUNAddress.IP)`, then installs the split-default route described in section 3.
5. **`routes.Add(c.xrayToGatewayRoute())`** — the loop-prevention exception, also from section 3.
6. **`go c.pipe.Copy(ctx, c.tunnel, c.cfg.InboundProxy.String())`** — the packet-pumping goroutine; this single call is the entire pipe2socks bridge, running for the lifetime of the connection. Its exit value is sent to `c.tunnelStopped`.

Every step from 2 onward is paired with a rollback closure appended to a
`rollback []func() error` slice. If any later step fails, `Connect` runs
the accumulated closures in reverse order before returning the error — so a
failure at step 5, for example, tears down the TUN device and stops
xray-core rather than leaking them.

---

## 5. Teardown — `client.go`, `Client.Disconnect`

In order:

1. `c.stopTunnel()` — cancels the context the copy goroutine in step 6 above is watching.
2. `c.xInst.Close()` — stops xray-core.
3. `c.tunnel.Close()` — closes the TUN device.
4. `c.routes.Delete(c.xrayToGatewayRoute())` — removes the loop-prevention exception.
5. `c.routes.Delete(route.Opts{IfName: c.tunIfaceName, Routes: c.cfg.RoutesToTUN})` — removes the split-default routes.
6. `<-c.tunnelStopped`, bounded by a `context.WithTimeout(ctx, disconnectTimeout)` (30s) — waits for the copy goroutine to actually exit before returning.

`Disconnect` checks an internal `state` field (`stateIdle` / `stateConnected`)
before doing any of the above, so calling it twice, or calling it when never
connected, is a no-op rather than a double-close panic. This matters in
`--tray` mode, where a menu click can call `Disconnect` at any point in the
connection lifecycle.

**Limitation:** if the process is killed with `SIGKILL` rather than
`SIGTERM`/`SIGINT`, none of the five steps above run, and the routes/TUN
device are left in place. This is a property of `-9`, not a bug in this
code — no userspace cleanup can run after that signal. The symptom on the
next start is `add route: ... file exists`; the fix is to manually find and
kill the stale process, then delete the leftover routes (`route delete
0.0.0.0/1`, `route delete 128.0.0.0/1`).

---

## 6. Reconnection loop — `main.go`, `runWithReconnect`

Wraps one connection attempt in a retry loop with exponential backoff via
`backoff(attempt, defaultReconnectInitial, defaultReconnectMax)` —
`defaultReconnectInitial` is 2 seconds, `defaultReconnectMax` caps it at 60
seconds, doubling on each failed attempt.

On a successful `vpn.Connect(profile.Link)`:

- `s.connected.Store(true)`, `s.connectedAt.Store(time.Now().UnixNano())`, `s.activeProfile.Store(&profile)`
- `s.bytesIn.Store(0)` and `s.bytesOut.Store(0)` — these counters are reset on every fresh connection; they are per-session totals, not lifetime totals
- a ticker goroutine on `metricsInterval` (5 seconds) copies `vpn.BytesRead()` / `vpn.BytesWritten()` (from `readerMetrics` in `metrics.go`) into `s.bytesIn` / `s.bytesOut`, which is what both the HTTP `/status` endpoint and the tray UI read

The loop then selects on two signals simultaneously:

- `<-ctx.Done()` — intentional shutdown (SIGTERM/SIGINT or tray Quit)
- `<-vpn.TunnelDone()` — the pipe2socks goroutine exited mid-session due to a network error

In either case, `s.connected.Store(false)`, `s.connectedAt.Store(0)`,
`s.activeProfile.Store(nil)`, then `vpn.Disconnect(dctx)` is called with its
own 10-second timeout context. If the trigger was `ctx.Done()`, the function
returns. If it was a mid-session drop, execution falls through to the backoff
and retry path — so dropped connections are recovered automatically, not just
initial connect failures.

If `maxAttempts > 0` and the attempt count reaches it, the loop logs and
returns without retrying further — by default `maxAttempts` is 0, meaning
unlimited retries.

---

## 7. Tray mode — `tray.go`

`systray.Run` owns the main thread, a macOS/Cocoa requirement. Three
goroutine groups coordinate through a single **unbuffered** channel,
`cmdCh chan vpnCmd`:

- **Controller goroutine** — the only code that calls the internal `start()`/`stop()` closures on a connection. Reads from `cmdCh` in a loop; serializes every connect, disconnect, and profile-switch so exactly one tunnel is ever active. Its `stop()` blocks on the running session's exit channel (`<-done`) bounded by a `context.WithTimeout(ctx, stopTimeout)` (3 seconds). On timeout, it returns an error and deliberately leaves `vpnCancel`/`done` set rather than calling `start()` on the next profile — this avoids two tunnels racing against the same TUN device and route table.
- **UI ticker goroutine** — reads `state` every `metricsInterval`, calls `mStatusLine.SetTitle()` / `mBandwidth.SetTitle()` / etc. only when the formatted value actually changed since the previous tick, tracked via `prevStatus`, `prevBandwidth`, and similar variables.
- **Per-profile click goroutines** — one per menu item, each blocking on `cmdCh <- vpnCmd{kind: cmdSwitch, profile: pi.profile}` until the controller is free to accept it, guarded by `<-ctx.Done()` so the goroutine exits cleanly on shutdown instead of blocking forever.

---

## 8. File reference

| File | Role |
|---|---|
| `client.go` | `Client` type — `Connect`, `Disconnect`, rollback, route exception, TUN setup |
| `main.go` | flag parsing, `state` struct, `runWithReconnect`, status server, bandwidth printer |
| `config.go` | parses `--link` / `.txt` / `.yaml` into a `Profile` |
| `metrics.go` | `readerMetrics` — wraps the TUN `io.ReadWriteCloser` to count bytes read/written |
| `interfaces.go` | shared interface types (`pipeIface`, `ipTable`) used for testability |
| `tray.go` | macOS menu bar UI and the connect/disconnect/switch controller |
| `tray_darwin.go` / `tray_other.go` | build-tag split: real tray implementation on darwin, panic stub elsewhere |
| `sudo_warn_darwin.go` / `sudo_warn_other.go` | platform-specific privilege check at startup |
| `pprof_debug.go` | opt-in, loopback-only `net/http/pprof` server, gated by the `XRAY_DEBUG_PPROF` environment variable |

---

## 9. Why the SOCKS hop stays

A separate architecture proposal suggested replacing `pipe2socks` with a
userspace TCP/IP stack (such as gVisor's netstack) talking directly to
xray-core, removing the local SOCKS hop entirely. That design exists and is
used by some other VPN clients. It is not adopted in this codebase, for two
concrete reasons:

1. **No measured bottleneck.** Profiling this binary (`go tool pprof`
   against the `XRAY_DEBUG_PPROF`-gated endpoint, plus `ps`/`top`) showed
   idle RSS around 110MB and near-zero CPU. The SOCKS hop is a loopback
   syscall, not a network round-trip, and nothing measured points at it as
   a cost worth removing.
2. **It substitutes one mature dependency for another, larger one.**
   `pipe2socks` already works. gVisor's netstack is a substantially larger
   codebase with its own integration surface and edge cases, traded in
   exchange for removing one local proxy hop.

If a specific, measured bottleneck is found in this layer, this section and
the corresponding code should be revisited together.