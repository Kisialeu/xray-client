package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goxray/core/network/route"
	"github.com/goxray/core/network/tun"
	"github.com/goxray/core/pipe2socks"
	"github.com/jackpal/gateway"

	xapplog "github.com/xtls/xray-core/app/log"
	xcommon "github.com/xtls/xray-core/common"
	xcommlog "github.com/xtls/xray-core/common/log"
)

const (
	disconnectTimeout   = 30 * time.Second
	inboundReadyTimeout = 5 * time.Second
	inboundDialInterval = 20 * time.Millisecond
)

var (
	defaultTUNAddress = &net.IPNet{IP: net.IPv4(192, 18, 0, 1), Mask: net.IPv4Mask(255, 255, 255, 255)}

	// DefaultRoutesToTUN contains the split-default routes used to send IPv4
	// traffic through the TUN interface while leaving the proxy endpoint
	// reachable through the original gateway.
	DefaultRoutesToTUN = []*route.Addr{
		route.MustParseAddr("0.0.0.0/1"),
		route.MustParseAddr("128.0.0.0/1"),
	}
)

// Config contains the tun2socks, routing, DNS, logging, and Xray settings
// used to create a Client. Nil optional fields retain the defaults selected by
// NewClient.
type Config struct {
	// GatewayIP is the host gateway used for the proxy endpoint exception route.
	GatewayIP *net.IP
	// InboundProxy is the local SOCKS5 endpoint exposed by the embedded Xray instance.
	InboundProxy *Proxy
	// TUNAddress is the address assigned to the packet tunnel interface.
	TUNAddress *net.IPNet
	// RoutesToTUN are the routes installed on the packet tunnel interface.
	RoutesToTUN []*route.Addr
	// TLSAllowInsecure is retained for configuration compatibility; insecure TLS
	// is rejected when a connection is created.
	TLSAllowInsecure bool
	// DNSServers are applied to the active macOS network service while connected.
	DNSServers []string
	// Logger receives client lifecycle and error messages.
	Logger *slog.Logger
	// XRayLogType controls the log destination used by the embedded Xray core.
	XRayLogType xapplog.LogType
}

func (c *Config) apply(new *Config) {
	if new.GatewayIP != nil {
		c.GatewayIP = new.GatewayIP
	}
	if new.InboundProxy != nil {
		c.InboundProxy = new.InboundProxy
	}
	if new.TUNAddress != nil {
		c.TUNAddress = new.TUNAddress
	}
	if new.Logger != nil {
		c.Logger = new.Logger
	}
	if new.RoutesToTUN != nil {
		c.RoutesToTUN = new.RoutesToTUN
	}
	if new.DNSServers != nil {
		c.DNSServers = new.DNSServers
	}
	c.TLSAllowInsecure = new.TLSAllowInsecure
	if new.XRayLogType != xapplog.LogType_None {
		c.XRayLogType = new.XRayLogType
	}
}

// Client manages one Xray-backed TUN session at a time. A Client owns the
// embedded Xray instance, packet tunnel, routes, DNS override, and traffic
// counters for its current session.
type Client struct {
	cfg Config

	mu    sync.Mutex
	state connState

	xInst   xcommon.Runnable
	xSrvIP  *net.IPAddr
	tunnel  io.ReadWriteCloser
	metrics atomic.Pointer[readerMetrics] // lock-free read for BytesRead/BytesWritten
	pipe    pipeIface
	routes  ipTable

	tunIfaceName   string
	tunRouteAdded  bool
	xrayRouteAdded bool
	dnsState       *dnsOverride

	tunnelStopped chan error
	stopTunnel    func()
}

type connState int

const (
	stateIdle connState = iota
	stateConnected
)

// Proxy identifies the local SOCKS5 endpoint used by the packet-to-SOCKS
// bridge.
type Proxy struct {
	// IP is the endpoint address.
	IP net.IP
	// Port is the endpoint TCP port.
	Port int
}

// String returns the proxy endpoint in host:port form.
func (p *Proxy) String() string {
	return fmt.Sprintf("%s:%d", p.IP, p.Port)
}

// NewClient creates an idle Client using the discovered default gateway, an
// ephemeral localhost SOCKS port, the default TUN address, and split-default
// IPv4 routes.
func NewClient() (*Client, error) {
	gatewayIP, err := gateway.DiscoverGateway()
	if err != nil {
		return nil, fmt.Errorf("discover gateway: %w", err)
	}

	p, err := pipe2socks.NewPipe(pipe2socks.DefaultOpts)
	if err != nil {
		return nil, fmt.Errorf("tun2socks new pipe: %w", err)
	}

	r, err := route.New()
	if err != nil {
		return nil, fmt.Errorf("route new: %w", err)
	}

	port, err := getFreePortSafe()
	if err != nil {
		return nil, fmt.Errorf("allocate inbound port: %w", err)
	}

	return &Client{
		cfg: Config{
			GatewayIP:    &gatewayIP,
			InboundProxy: &Proxy{IP: net.IPv4(127, 0, 0, 1), Port: port},
			TUNAddress:   defaultTUNAddress,
			RoutesToTUN:  DefaultRoutesToTUN,
			Logger:       slog.New(slog.NewTextHandler(os.Stdout, nil)),
		},
		state:  stateIdle,
		pipe:   p,
		routes: r,
	}, nil
}

// NewClientWithOpts creates an idle Client and applies the non-zero settings
// in cfg over the defaults created by NewClient.
func NewClientWithOpts(cfg Config) (*Client, error) {
	cl, err := NewClient()
	if err != nil {
		return nil, err
	}
	cl.cfg.apply(&cfg)
	return cl, nil
}

// GatewayIP returns the gateway used for the proxy endpoint route exception.
func (c *Client) GatewayIP() net.IP { return *c.cfg.GatewayIP }

// TUNAddress returns the IP assigned to the packet tunnel interface.
func (c *Client) TUNAddress() net.IP { return c.cfg.TUNAddress.IP }

// InboundProxy returns the local SOCKS5 endpoint exposed by the Xray instance.
func (c *Client) InboundProxy() Proxy { return *c.cfg.InboundProxy }

// TunnelDone returns a channel that is closed (or receives an error) when the
// tunnel pipe exits — either because ctx was cancelled or due to a network
// error. Callers can select on this to detect mid-session drops without
// polling. Returns nil if not currently connected.
func (c *Client) TunnelDone() <-chan error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tunnelStopped
}

// Connect establishes the tunnel. Returns an error and leaves the Client in
// stateIdle with all partial resources rolled back if any step fails.
// Calling Connect while already connected returns an error without side effects.
func (c *Client) Connect(link string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == stateConnected {
		return errors.New("already connected: call Disconnect first")
	}

	c.cfg.Logger.Debug("connecting to tunnel", "cfg", c.cfg)

	var rollback []func() error
	runRollback := func() {
		for i := len(rollback) - 1; i >= 0; i-- {
			if err := rollback[i](); err != nil {
				c.cfg.Logger.Error("rollback step failed", "err", err)
			}
		}
	}

	xInst, xSrvIP, err := c.createXrayProxy(link)
	if err != nil {
		return fmt.Errorf("create xray core instance: %w", err)
	}
	c.xInst, c.xSrvIP = xInst, xSrvIP
	rollback = append(rollback, func() error { return c.xInst.Close() })

	if err = c.xInst.Start(); err != nil {
		runRollback()
		return fmt.Errorf("start xray core instance: %w", err)
	}

	if err = waitForInboundReady(c.cfg.InboundProxy.String(), inboundReadyTimeout, inboundDialInterval); err != nil {
		runRollback()
		return fmt.Errorf("xray inbound not ready: %w", err)
	}

	tunnel, ifaceName, err := c.setupTunnel()
	if err != nil {
		runRollback()
		return fmt.Errorf("setup TUN device: %w", err)
	}
	c.tunIfaceName = ifaceName
	if err := protectInterface(ifaceName); err != nil {
		_ = c.routes.Delete(route.Opts{IfName: ifaceName, Routes: c.cfg.RoutesToTUN})
		_ = tunnel.Close()
		runRollback()
		return fmt.Errorf("tunnel firewall: %w", err)
	}
	rollback = append(rollback, func() error { return protectInterface("") })
	c.tunRouteAdded = true
	rollback = append(rollback, func() error {
		err := c.routes.Delete(route.Opts{IfName: c.tunIfaceName, Routes: c.cfg.RoutesToTUN})
		c.tunRouteAdded = false
		return err
	})
	rollback = append(rollback, func() error { return tunnel.Close() })
	m := newReaderMetrics(tunnel)
	c.tunnel = m
	c.metrics.Store(m)

	gwRoute := c.xrayToGatewayRoute()
	_ = c.routes.Delete(gwRoute)
	if err = c.routes.Add(gwRoute); err != nil {
		runRollback()
		return fmt.Errorf("add xray server route exception: %w", err)
	}
	c.xrayRouteAdded = true

	if len(c.cfg.DNSServers) > 0 {
		c.dnsState = overrideDNS(c.cfg.DNSServers, c.cfg.Logger)
		if c.dnsState == nil {
			_ = c.routes.Delete(gwRoute)
			c.xrayRouteAdded = false
			runRollback()
			return fmt.Errorf("DNS protection could not be configured")
		}
		rollback = append(rollback, func() error { c.dnsState.restore(c.cfg.Logger); return nil })
	}

	c.tunnelStopped = make(chan error, 1)
	var ctx context.Context
	ctx, c.stopTunnel = context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	stopped := c.tunnelStopped
	go func() {
		defer close(stopped)
		wg.Done()
		stopped <- c.pipe.Copy(ctx, m, c.cfg.InboundProxy.String())
	}()
	wg.Wait()

	c.state = stateConnected
	return nil
}

// Disconnect tears down the tunnel. Safe to call multiple times; subsequent
// calls after a successful disconnect are no-ops.
//
// The slow wait on tunnelStopped happens outside the mutex so BytesRead /
// BytesWritten are not blocked for the full disconnectTimeout.
func (c *Client) Disconnect(ctx context.Context) error {
	c.mu.Lock()

	if c.state != stateConnected {
		c.mu.Unlock()
		return nil
	}

	// Capture everything we need, clear state, then unlock before the slow
	// wait so concurrent callers are not blocked.
	stopFn := c.stopTunnel
	xInst := c.xInst
	tunnel := c.tunnel
	tunnelStopped := c.tunnelStopped
	xrayRoute := c.xrayToGatewayRoute()
	xrayRouteAdded := c.xrayRouteAdded
	tunIfaceName := c.tunIfaceName
	tunRouteAdded := c.tunRouteAdded
	tunRoutes := c.cfg.RoutesToTUN
	dnsState := c.dnsState

	c.xInst, c.tunnel, c.stopTunnel, c.tunnelStopped = nil, nil, nil, nil
	c.metrics.Store(nil)
	c.xrayRouteAdded = false
	c.tunRouteAdded = false
	c.dnsState = nil
	c.state = stateIdle

	c.mu.Unlock()

	// Slow path — no lock held.
	protectionErr := protectInterface("")
	dnsState.restore(c.cfg.Logger)
	stopFn()

	var errs []error
	if protectionErr != nil {
		errs = append(errs, protectionErr)
	}
	if err := tunnel.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close tunnel: %w", err))
	}
	if xrayRouteAdded {
		if err := c.routes.Delete(xrayRoute); err != nil {
			errs = append(errs, fmt.Errorf("delete gateway route: %w", err))
		}
	}
	if tunRouteAdded {
		if err := c.routes.Delete(route.Opts{IfName: tunIfaceName, Routes: tunRoutes}); err != nil {
			errs = append(errs, fmt.Errorf("delete tun route: %w", err))
		}
	}
	if err := xInst.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close xray instance: %w", err))
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, disconnectTimeout)
	defer cancel()
	select {
	case tunErr := <-tunnelStopped:
		if tunErr != nil {
			errs = append(errs, fmt.Errorf("tunnel copy: %w", tunErr))
		}
	case <-timeoutCtx.Done():
		errs = append(errs, fmt.Errorf("waiting for tunnel stop: %w", timeoutCtx.Err()))
	}

	return errors.Join(errs...)
}

// BytesRead returns the number of bytes read from the current tunnel session.
// It returns zero while the client is idle.
func (c *Client) BytesRead() int {
	if m := c.metrics.Load(); m != nil {
		return m.BytesRead()
	}
	return 0
}

// BytesWritten returns the number of bytes written to the current tunnel
// session. It returns zero while the client is idle.
func (c *Client) BytesWritten() int {
	if m := c.metrics.Load(); m != nil {
		return m.BytesWritten()
	}
	return 0
}

func (c *Client) xrayToGatewayRoute() route.Opts {
	return route.Opts{Gateway: *c.cfg.GatewayIP, Routes: []*route.Addr{route.MustParseAddr(c.xSrvIP.String() + "/32")}}
}

func (c *Client) createXrayProxy(link string) (xcommon.Runnable, *net.IPAddr, error) {
	if c.cfg.TLSAllowInsecure {
		return nil, nil, fmt.Errorf("insecure TLS is not supported; use a trusted certificate")
	}
	_, address, _, _, err := buildOutbound(link)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid proxy profile")
	}
	ip, err := resolveEndpoint(address)
	if err != nil {
		return nil, nil, err
	}
	result, err := buildXrayInstance(
		link,
		c.cfg.InboundProxy.IP.String(),
		c.cfg.InboundProxy.Port,
		xRayLogLevel(c.cfg.Logger.Handler()),
		c.cfg.XRayLogType,
		ip.String(),
	)
	if err != nil {
		return nil, nil, err
	}

	return result.instance, ip, nil
}

func xRayLogLevel(h slog.Handler) xcommlog.Severity {
	ctx := context.Background()
	switch {
	case h.Enabled(ctx, slog.LevelDebug):
		return xcommlog.Severity_Debug
	case h.Enabled(ctx, slog.LevelInfo):
		return xcommlog.Severity_Info
	case h.Enabled(ctx, slog.LevelError):
		return xcommlog.Severity_Error
	case h.Enabled(ctx, slog.LevelWarn):
		return xcommlog.Severity_Warning
	}
	return xcommlog.Severity_Unknown
}

// setupTunnel creates and brings up the TUN interface and installs routes to it.
// On failure it rolls back any partial state (interface left up with no routes).
func (c *Client) setupTunnel() (*tun.Interface, string, error) {
	ifc, err := tun.New("", 1500)
	if err != nil {
		return nil, "", fmt.Errorf("create tun: %w", err)
	}
	if err = ifc.Up(c.cfg.TUNAddress, c.cfg.TUNAddress.IP); err != nil {
		_ = ifc.Close()
		return nil, "", fmt.Errorf("setup interface: %w", err)
	}
	if err = c.routes.Add(route.Opts{IfName: ifc.Name(), Routes: c.cfg.RoutesToTUN}); err != nil {
		_ = ifc.Close()
		return nil, "", fmt.Errorf("add route: %w", err)
	}
	return ifc, ifc.Name(), nil
}

// waitForInboundReady polls addr with TCP dials until a connection succeeds,
// the timeout elapses, or the dial fails with a non-retryable error.
func waitForInboundReady(addr string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		conn, err := net.DialTimeout("tcp", addr, interval)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s: %w", timeout, lastErr)
		}
		time.Sleep(interval)
	}
}

// getFreePortSafe returns an ephemeral free TCP port on localhost, or an
// error if one cannot be obtained. Callers must decide how to handle failure
// rather than silently falling back to a fixed port.
func getFreePortSafe() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen on ephemeral port: %w", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
