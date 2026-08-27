package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"github.com/getlantern/systray"
)


// ── icon cache - generated once at startup, never reallocated (issue 7) ───────

var (
	cachedIconConn = circleIcon(true)
	cachedIconDisc = circleIcon(false)
)

func iconConn() []byte { return cachedIconConn }
func iconDisc() []byte { return cachedIconDisc }

// Controller command types (cmdKind, vpnCmd, stopTimeout) are in controller.go.

// ── menu layout ───────────────────────────────────────────────────────────────
//
//	XRay VPN                       ← icon + tooltip
//	─────────────────────────────
//	🟢 falkenstein                  ← mStatusLine (disabled)
//	⏱ 2m 30s                        ← mSession    (hidden when disconnected)
//	↓ 1.2 KiB/s   ↑ 340 B/s         ← mBandwidth  (hidden when disconnected)
//	↓ Total 4.5 MiB   ↑ 1.2 MiB     ← mTotals     (hidden when disconnected)
//	─────────────────────────────
//	Profiles  (label, disabled)
//	    falkenstein              ← flat list, ✓ marks active
//	    helsinki
//	─────────────────────────────
//	Connect / Disconnect
//	─────────────────────────────
//	Quit

func runTray(ctx context.Context, cancel context.CancelFunc, logger *slog.Logger, s *state, initial Profile, profiles []Profile, maxReconnects int) {
	systray.Run(
		trayOnReady(ctx, cancel, logger, s, initial, profiles, maxReconnects),
		func() { cancel() },
	)
}

func trayOnReady(ctx context.Context, rootCancel context.CancelFunc, logger *slog.Logger, s *state, initial Profile, profiles []Profile, maxReconnects int) func() {
	return func() {
		systray.SetTemplateIcon(iconDisc(), iconDisc())
		systray.SetTooltip("XRay VPN")

		// ── info rows ─────────────────────────────────────────────────────
		mStatusLine := systray.AddMenuItem("⚫  Disconnected", "")
		mStatusLine.Disable()
		mSession := systray.AddMenuItem("", "")
		mSession.Disable()
		mSession.Hide()
		mBandwidth := systray.AddMenuItem("", "")
		mBandwidth.Disable()
		mBandwidth.Hide()
		mTotals := systray.AddMenuItem("", "")
		mTotals.Disable()
		mTotals.Hide()
		systray.AddSeparator()

		// ── profile list ──────────────────────────────────────────────────
		// systray has no submenu API, so profiles are flat siblings.
		mProfilesLabel := systray.AddMenuItem("Profiles", "")
		mProfilesLabel.Disable()
		type profileItem struct {
			profile Profile
			item    *systray.MenuItem
		}
		var profileItems []profileItem
		for _, p := range profiles {
			item := systray.AddMenuItem("    "+p.Name, p.Name)
			profileItems = append(profileItems, profileItem{p, item})
		}
		systray.AddSeparator()

		// ── actions ───────────────────────────────────────────────────────
		mConnect := systray.AddMenuItem("Connect", "")
		mDisconnect := systray.AddMenuItem("Disconnect", "")
		mDisconnect.Hide()
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Quit", "")

		// ── currentProfile: written by controller, read by UI ticker ──────
		// atomic.Pointer gives a safe unsynchronized read. (issue 1)
		var currentProfile atomic.Pointer[Profile]
		currentProfile.Store(&initial)

		// ── command channel - all lifecycle ops go through here (issues 1,3,5)
		// Unbuffered: senders block until the controller accepts the command,
		// which is the bounded wait requested for issue 1 (no silent drops).
		cmdCh := make(chan vpnCmd)

		// ── controller goroutine ──────────────────────────────────────────
		// Sole owner of vpnCancel + done. Serializes start/stop/switch.
		go func() {
			var (
				vpnCancel context.CancelFunc
				done      <-chan struct{}
			)

			start := func(p Profile) {
				currentProfile.Store(&p)
				vpnCtx, cancel := context.WithCancel(ctx)
				vpnCancel = cancel
				d := make(chan struct{})
				done = d
				go func() {
					defer close(d)
					runWithReconnect(vpnCtx, logger, s, p, maxReconnects)
				}()
			}

			// stop blocks on <-done bounded by stopTimeout (ctx-derived). On
			// timeout it returns an error and deliberately leaves vpnCancel/done
			// set so a caller can be told the previous session may still be
			// running, rather than silently allowing a second start() to race
			// against it (issue 3).
			stop := func() error {
				if vpnCancel == nil {
					return nil
				}
				vpnCancel()

				timeoutCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
				defer cancel()
				select {
				case <-done:
					vpnCancel = nil
					done = nil
					return nil
				case <-timeoutCtx.Done():
					err := fmt.Errorf("previous session did not stop within %s: %w", stopTimeout, timeoutCtx.Err())
					logger.Error("controller stop timed out, refusing to start new session", "err", err)
					return err
				}
			}

			start(initial)

			for {
				select {
				case <-ctx.Done():
					_ = stop()
					return
				case cmd := <-cmdCh:
					var err error
					switch cmd.kind {
					case cmdSwitch:
						if err = stop(); err == nil {
							start(cmd.profile)
						}
					case cmdStop:
						err = stop()
					}
					if cmd.done != nil {
						cmd.done <- err
						close(cmd.done)
					}
				}
			}
		}()

		// ── UI ticker - only goroutine that writes menu items (issue 4) ───
		// Tracks previous rendered values; only calls SetTitle when changed. (issue 8)
		go func() {
			t := time.NewTicker(metricsInterval)
			defer t.Stop()

			var (
				prevConnected  bool
				prevStatus     string
				prevSession    string
				prevBandwidth  string
				prevTotals     string
				prevActiveName string
				prevIn         int64
				prevOut        int64
			)

			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					in := s.bytesIn.Load()
					out := s.bytesOut.Load()
					rxRate := float64(in-prevIn) / metricsInterval.Seconds()
					txRate := float64(out-prevOut) / metricsInterval.Seconds()
					prevIn, prevOut = in, out

					connected := s.connected.Load()

					if connected {
						systray.SetTemplateIcon(iconConn(), iconConn())

						// Prefer the authoritative state.activeProfile; fall back to
						// currentProfile (the one we launched, pre-connection). (issue 1)
						pName := ""
						if ap := s.activeProfile.Load(); ap != nil {
							pName = ap.Name
						} else if cp := currentProfile.Load(); cp != nil {
							pName = cp.Name
						}

						session := ""
						if nanos := s.connectedAt.Load(); nanos != 0 {
							session = formatDuration(time.Since(time.Unix(0, nanos)))
						}

						status := "🟢  " + pName
						bw := fmt.Sprintf(" ↓ %s/s   ↑ %s/s", humanBytes(rxRate), humanBytes(txRate))
						tot := fmt.Sprintf(" Total ↑ %s   ↓ %s", humanBytes(float64(in)), humanBytes(float64(out)))

						if status != prevStatus {
							mStatusLine.SetTitle(status)
							prevStatus = status
						}
						if session != prevSession {
							mSession.SetTitle("⏱  " + session)
							prevSession = session
						}
						if bw != prevBandwidth {
							mBandwidth.SetTitle(bw)
							prevBandwidth = bw
						}
						if tot != prevTotals {
							mTotals.SetTitle(tot)
							prevTotals = tot
						}

						// Show session rows only on transition. (issue 8)
						if !prevConnected {
							mSession.Show()
							mBandwidth.Show()
							mTotals.Show()
							mConnect.Hide()
							mDisconnect.Show()
						}

						// Checkmarks only on profile change. (issue 8)
						if pName != prevActiveName {
							for _, pi := range profileItems {
								if pi.profile.Name == pName {
									pi.item.SetTitle("  ✓ " + pi.profile.Name)
								} else {
									pi.item.SetTitle("    " + pi.profile.Name)
								}
							}
							prevActiveName = pName
						}
					} else {
						systray.SetTemplateIcon(iconDisc(), iconDisc())

						if "⚫  Disconnected" != prevStatus {
							mStatusLine.SetTitle("⚫  Disconnected")
							prevStatus = "⚫  Disconnected"
						}

						// Hide rows only on transition. (issue 8)
						if prevConnected {
							mSession.Hide()
							mBandwidth.Hide()
							mTotals.Hide()
							mDisconnect.Hide()
							mConnect.Show()
							for _, pi := range profileItems {
								pi.item.SetTitle("    " + pi.profile.Name)
							}
							prevActiveName = ""
						}
					}

					prevConnected = connected
				}
			}
		}()

		// ── event loop ────────────────────────────────────────────────────
		// One goroutine per profile button; each blocks sending on cmdCh until
		// the controller is free to accept it (bounded wait, issue 2 resolved
		// per explicit instruction - no drops, no unbounded queueing beyond
		// the controller's own serialization).
		for _, pi := range profileItems {
			pi := pi
			go func() {
				for range pi.item.ClickedCh {
					select {
					case cmdCh <- vpnCmd{kind: cmdSwitch, profile: pi.profile}:
					case <-ctx.Done():
						return
					}
				}
			}()
		}

		go func() {
			defer systray.Quit()
			for {
				select {
				case <-ctx.Done():
					return
				case <-mConnect.ClickedCh:
					if cp := currentProfile.Load(); cp != nil {
						select {
						case cmdCh <- vpnCmd{kind: cmdSwitch, profile: *cp}:
						case <-ctx.Done():
							return
						}
					}
				case <-mDisconnect.ClickedCh:
					select {
					case cmdCh <- vpnCmd{kind: cmdStop}:
					case <-ctx.Done():
						return
					}
				case <-mQuit.ClickedCh:
					rootCancel()
					return
				}
			}
		}()
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// ── icons ─────────────────────────────────────────────────────────────────────

func circleIcon(filled bool) []byte {
	const size = 22
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			dist := math.Sqrt(dx*dx + dy*dy)
			var a uint8
			if filled {
				switch {
				case dist < 8:
					a = 255
				case dist < 9:
					a = uint8(255 * (9 - dist))
				}
			} else {
				switch {
				case dist >= 6.5 && dist < 8.5:
					a = 255
				case dist >= 5.5 && dist < 6.5:
					a = uint8(255 * (dist - 5.5))
				case dist >= 8.5 && dist < 9.5:
					a = uint8(255 * (9.5 - dist))
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, a})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}