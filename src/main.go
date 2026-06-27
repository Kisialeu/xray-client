package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultReconnectInitial = 2 * time.Second
	defaultReconnectMax     = 15 * time.Second
	metricsInterval         = 5 * time.Second  // how often byte counters are synced
	bandwidthInterval       = 10 * time.Second // how often bandwidth is logged (--verbose)
)

func init() {
	// Return memory to OS aggressively — we're a long-running network daemon,
	// not a throughput-sensitive computation. Keeps RSS low between bursts.
	debug.SetGCPercent(20)
	runtime.GOMAXPROCS(2) // TUN + XRay don't benefit from more than 2 OS threads
}

// ── shared state ──────────────────────────────────────────────────────────────

type state struct {
	link          string
	connected     atomic.Bool
	bytesIn       atomic.Int64
	bytesOut      atomic.Int64
	reconnects    atomic.Int64
	startAt       time.Time
	connectedAt   atomic.Int64 // unix nano; 0 = not connected
	activeProfile atomic.Pointer[Profile]
}

// ── status server (opt-in, --status) ─────────────────────────────────────────

func startStatusServer(addr string, s *state) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if s.connected.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintln(w, "ok")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintln(w, "disconnected")
		}
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connected":  s.connected.Load(),
			"uptime_s":   int64(time.Since(s.startAt).Seconds()),
			"bytes_in":   s.bytesIn.Load(),
			"bytes_out":  s.bytesOut.Load(),
			"reconnects": s.reconnects.Load(),
		})
	})
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() { _ = srv.ListenAndServe() }()
}

// ── bandwidth printer (opt-in, --verbose) ────────────────────────────────────

func startBandwidthPrinter(ctx context.Context, logger *slog.Logger, s *state) {
	go func() {
		t := time.NewTicker(bandwidthInterval)
		defer t.Stop()
		var prevIn, prevOut int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				in, out := s.bytesIn.Load(), s.bytesOut.Load()
				logger.Info("bandwidth",
					"rx/s", humanBytes(float64(in-prevIn)/bandwidthInterval.Seconds()),
					"tx/s", humanBytes(float64(out-prevOut)/bandwidthInterval.Seconds()),
					"total_rx", humanBytes(float64(in)),
					"total_tx", humanBytes(float64(out)),
				)
				prevIn, prevOut = in, out
			}
		}
	}()
}

func humanBytes(n float64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", n/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MiB", n/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KiB", n/(1<<10))
	default:
		return fmt.Sprintf("%.0f B", n)
	}
}

// ── reconnect loop ────────────────────────────────────────────────────────────

// runWithReconnect connects profile and keeps it connected, retrying with
// exponential backoff on failure, until ctx is cancelled or maxAttempts
// consecutive failures are reached.
func runWithReconnect(
	ctx context.Context,
	logger *slog.Logger,
	s *state,
	profile Profile,
	maxAttempts int,
) {
	attempt := 0
	for {
		vpn, err := NewClientWithOpts(Config{
			TLSAllowInsecure: profile.TLSInsecure,
			Logger:           logger,
		})
		if err == nil {
			err = vpn.Connect(profile.Link)
		}

		if err != nil {
			logger.Error("connect failed", "attempt", attempt+1, "err", err)
		} else {
			s.connected.Store(true)
			s.connectedAt.Store(time.Now().UnixNano())
			s.activeProfile.Store(&profile)
			// Reset session counters on each fresh connection.
			s.bytesIn.Store(0)
			s.bytesOut.Store(0)
			attempt = 0 // a successful connect resets the failure count
			logger.Info("tunnel up", "profile", profile.Name)

			// Single goroutine to sync metrics every metricsInterval.
			metricsDone := make(chan struct{})
			go func() {
				defer close(metricsDone)
				t := time.NewTicker(metricsInterval)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						s.bytesIn.Store(int64(vpn.BytesRead()))
						s.bytesOut.Store(int64(vpn.BytesWritten()))
					}
				}
			}()

			tunnelDone := vpn.TunnelDone()
			select {
			case <-ctx.Done():
				// Intentional shutdown.
			case tunnelErr := <-tunnelDone:
				// Tunnel pipe died mid-session — trigger reconnect.
				if tunnelErr != nil {
					logger.Error("tunnel dropped", "err", tunnelErr)
				}
			}

			s.connected.Store(false)
			s.connectedAt.Store(0)
			s.activeProfile.Store(nil)
			dctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = vpn.Disconnect(dctx)
			cancel()
			<-metricsDone

			if ctx.Err() != nil {
				return
			}
			// Fall through to reconnect if the tunnel dropped unexpectedly.
		}

		attempt++
		if maxAttempts > 0 && attempt >= maxAttempts {
			logger.Error("max reconnect attempts reached", "attempts", attempt)
			return
		}

		delay := backoff(attempt, defaultReconnectInitial, defaultReconnectMax)
		logger.Info("reconnecting", "in", delay, "attempt", attempt)
		s.reconnects.Add(1)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func backoff(attempt int, initial, maxDur time.Duration) time.Duration {
	exp := math.Pow(2, float64(attempt-1))
	// Guard against float64 overflow into negative time.Duration.
	if exp > float64(maxDur/initial) {
		return maxDur
	}
	d := time.Duration(exp) * initial
	if d > maxDur {
		return maxDur
	}
	return d
}

// ── help ──────────────────────────────────────────────────────────────────────

const helpText = `xray-cli — lightweight XRay VPN client

USAGE
  xray-cli [flags]

CONNECTION (pick one)
  --link   <url>    XRay link directly  (vless://, vmess://, trojan://, …)
  --config <file>   Config file:
                      .txt  — first non-comment line is the link
                      .yaml — multi-profile (see below)
  XRAY_LINK env var is also accepted instead of --link

PROFILE SELECTION (YAML config only)
  --profile <name>  Use named profile (default: "default" key, else first entry)
  --list-profiles   Print available profiles and exit

FLAGS
  --status  <addr>  Enable HTTP status server (e.g. 127.0.0.1:9999)
                      GET /health  → 200 ok / 503 disconnected
                      GET /status  → JSON metrics
  --verbose         Log bandwidth stats every 10 s
  --log     <lvl>   Log level: debug|info|warn|error  (default: info)
  --tray            macOS menu bar mode (default: on for darwin, off elsewhere)
  --tls-insecure    Allow self-signed TLS certificates
  --max-reconnects  Max reconnect attempts, 0 = unlimited (default: 0)
  --help            Show this help

YAML CONFIG FORMAT
  default: home          # optional default profile name

  profiles:
    - name: home
      link: "vless://..."
    - name: work
      link: "vmess://..."
      tls_insecure: true

TXT CONFIG FORMAT
  # comment lines are ignored
  vless://user@host:443?...

EXAMPLES
  xray-cli --link "vless://..."
  xray-cli --config servers.yaml --profile work
  xray-cli --config servers.yaml --list-profiles
  xray-cli --config link.txt --status 127.0.0.1:9999 --verbose
  xray-cli --link "vless://..." --tray          # macOS only
`

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	fs := flag.NewFlagSet("xray-cli", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(helpText) }

	link := fs.String("link", "", "")
	configFile := fs.String("config", "", "")
	profileName := fs.String("profile", "", "")
	doListProfiles := fs.Bool("list-profiles", false, "")
	statusAddr := fs.String("status", "", "")
	verbose := fs.Bool("verbose", false, "")
	logLevel := fs.String("log", "info", "")
	tlsInsecure := fs.Bool("tls-insecure", false, "")
	maxReconnects := fs.Int("max-reconnects", 0, "")
	tray := fs.Bool("tray", defaultTray, "")
	help := fs.Bool("help", false, "")

	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}

	if *help {
		fmt.Print(helpText)
		return
	}

	warnIfNotRoot(*tray)

	// Env fallback for link.
	if *link == "" {
		*link = strings.TrimSpace(os.Getenv("XRAY_LINK"))
	}

	// No connection source given at all, and not asking to list profiles:
	// fall back to a native macOS file picker rather than immediately
	// erroring out. On non-darwin this is a no-op (PickConfigFile returns
	// ""), so behavior there is unchanged from before — straight to the
	// existing error path below.
	if *link == "" && *configFile == "" && !*doListProfiles {
		if picked := PickConfigFile(); picked != "" {
			*configFile = picked
		}
	}

	// --list-profiles needs a config file but no link.
	if *doListProfiles {
		if *configFile == "" {
			fmt.Fprintln(os.Stderr, "error: --list-profiles requires --config")
			os.Exit(1)
		}
		if err := listProfiles(*configFile); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	profile, err := loadLink(*configFile, *profileName, *link)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		fmt.Fprintln(os.Stderr, "Run with --help for usage.")
		os.Exit(1)
	}

	// CLI --tls-insecure overrides file value.
	if *tlsInsecure {
		profile.TLSInsecure = true
	}

	logger := buildLogger(*logLevel)

	s := &state{link: profile.Link, startAt: time.Now()}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *statusAddr != "" {
		startStatusServer(*statusAddr, s)
		logger.Info("status server listening", "addr", *statusAddr)
	}

	if *tray {
		allProfiles, _ := loadAllProfiles(*configFile)
		if len(allProfiles) == 0 {
			allProfiles = []Profile{profile}
		}
		runTray(ctx, stop, logger, s, profile, allProfiles, *maxReconnects)
		return
	}

	if *verbose {
		startBandwidthPrinter(ctx, logger, s)
	}

	runWithReconnect(ctx, logger, s, profile, *maxReconnects)
}

func buildLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
