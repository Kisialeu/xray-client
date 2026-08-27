package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/getlantern/systray"
)

type daemonClient struct {
	base   string
	client *http.Client
}

func newDaemonClient(addr string) *daemonClient {
	return &daemonClient{
		base:   "http://" + addr,
		client: &http.Client{Timeout: 40 * time.Second},
	}
}

type daemonStatus struct {
	Connected     bool   `json:"connected"`
	ActiveProfile string `json:"active_profile"`
	UptimeS       int64  `json:"uptime_s"`
	BytesIn       int64  `json:"bytes_in"`
	BytesOut      int64  `json:"bytes_out"`
	Reconnects    int64  `json:"reconnects"`
}

type daemonProfiles struct {
	Profiles []struct {
		Name string `json:"name"`
	} `json:"profiles"`
	Active string `json:"active"`
}

func (dc *daemonClient) status() (daemonStatus, error) {
	resp, err := dc.client.Get(dc.base + "/status")
	if err != nil {
		return daemonStatus{}, err
	}
	defer resp.Body.Close()
	var s daemonStatus
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return daemonStatus{}, err
	}
	return s, nil
}

func (dc *daemonClient) profiles() (daemonProfiles, error) {
	resp, err := dc.client.Get(dc.base + "/profiles")
	if err != nil {
		return daemonProfiles{}, err
	}
	defer resp.Body.Close()
	var p daemonProfiles
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return daemonProfiles{}, err
	}
	return p, nil
}

func (dc *daemonClient) connect(name string) error {
	body, _ := json.Marshal(map[string]string{"profile": name})
	resp, err := dc.client.Post(dc.base+"/connect", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

func (dc *daemonClient) refresh() error {
	resp, err := dc.client.Post(dc.base+"/refresh", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

func (dc *daemonClient) disconnect() error {
	resp, err := dc.client.Post(dc.base+"/disconnect", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

func runTrayClient(ctx context.Context, cancel context.CancelFunc, logger *slog.Logger, addr string) {
	systray.Run(
		trayClientOnReady(ctx, cancel, logger, addr),
		func() { cancel() },
	)
}

func trayClientOnReady(ctx context.Context, rootCancel context.CancelFunc, logger *slog.Logger, addr string) func() {
	return func() {
		dc := newDaemonClient(addr)

		systray.SetTemplateIcon(iconDisc(), iconDisc())
		systray.SetTooltip("XRay VPN")

		mStatusLine := systray.AddMenuItem("⚫  Connecting to daemon…", "")
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

		mProfilesLabel := systray.AddMenuItem("Profiles", "")
		mProfilesLabel.Disable()

		type profileItem struct {
			name string
			item *systray.MenuItem
		}
		var profileItems []profileItem

		// Fetch profiles from daemon (retry until available)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				profs, err := dc.profiles()
				if err != nil {
					logger.Debug("waiting for daemon", "err", err)
					time.Sleep(2 * time.Second)
					continue
				}
				for _, p := range profs.Profiles {
					item := systray.AddMenuItem("    "+p.Name, p.Name)
					profileItems = append(profileItems, profileItem{name: p.Name, item: item})

					name := p.Name
					go func() {
						for range item.ClickedCh {
							logger.Info("switching profile", "profile", name)
							if err := dc.connect(name); err != nil {
								logger.Error("connect failed", "profile", name, "err", err)
							}
						}
					}()
				}

				systray.AddSeparator()

				// Add action buttons after profiles are loaded
				mConnect := systray.AddMenuItem("Connect", "")
				mDisconnect := systray.AddMenuItem("Disconnect", "")
				mDisconnect.Hide()
				mRefresh := systray.AddMenuItem("Refresh profiles", "")
				systray.AddSeparator()
				mQuit := systray.AddMenuItem("Quit", "")

				// Event loop
				go func() {
					defer systray.Quit()
					for {
						select {
						case <-ctx.Done():
							return
						case <-mConnect.ClickedCh:
							if len(profileItems) > 0 {
								// Reconnect the active or first profile
								active := ""
								if st, err := dc.status(); err == nil && st.ActiveProfile != "" {
									active = st.ActiveProfile
								}
								if active == "" {
									active = profileItems[0].name
								}
								go func() {
									if err := dc.connect(active); err != nil {
										logger.Error("connect failed", "err", err)
									}
								}()
							}
						case <-mDisconnect.ClickedCh:
							go func() {
								if err := dc.disconnect(); err != nil {
									logger.Error("disconnect failed", "err", err)
								}
							}()
						case <-mRefresh.ClickedCh:
							go func() {
								if err := dc.refresh(); err != nil {
									logger.Error("refresh failed", "err", err)
									return
								}
								profs, err := dc.profiles()
								if err != nil {
									logger.Error("fetch profiles after refresh", "err", err)
									return
								}
								for _, p := range profs.Profiles {
									exists := false
									for _, pi := range profileItems {
										if pi.name == p.Name {
											exists = true
											break
										}
									}
									if exists {
										continue
									}
									item := systray.AddMenuItem("    "+p.Name, p.Name)
									profileItems = append(profileItems, profileItem{name: p.Name, item: item})
									name := p.Name
									go func() {
										for range item.ClickedCh {
											logger.Info("switching profile", "profile", name)
											if err := dc.connect(name); err != nil {
												logger.Error("connect failed", "profile", name, "err", err)
											}
										}
									}()
								}
								logger.Info("profiles refreshed", "count", len(profs.Profiles))
							}()
						case <-mQuit.ClickedCh:
							rootCancel()
							return
						}
					}
				}()

				// UI ticker - polls daemon status
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
						daemonOnline   bool
					)

					for {
						select {
						case <-ctx.Done():
							return
						case <-t.C:
							st, err := dc.status()
							if err != nil {
								if daemonOnline {
									systray.SetTemplateIcon(iconDisc(), iconDisc())
									mStatusLine.SetTitle("⚫  Daemon offline")
									prevStatus = "⚫  Daemon offline"
									mSession.Hide()
									mBandwidth.Hide()
									mTotals.Hide()
									mDisconnect.Hide()
									mConnect.Show()
									daemonOnline = false
									prevConnected = false
								}
								continue
							}
							daemonOnline = true

							rxRate := float64(st.BytesIn-prevIn) / metricsInterval.Seconds()
							txRate := float64(st.BytesOut-prevOut) / metricsInterval.Seconds()
							prevIn, prevOut = st.BytesIn, st.BytesOut

							if st.Connected {
								systray.SetTemplateIcon(iconConn(), iconConn())

								pName := st.ActiveProfile
								session := ""
								if st.UptimeS > 0 {
									session = formatDuration(time.Duration(st.UptimeS) * time.Second)
								}

								status := "🟢  " + pName
								bw := fmt.Sprintf(" ↓ %s/s   ↑ %s/s", humanBytes(rxRate), humanBytes(txRate))
								tot := fmt.Sprintf(" Total ↑ %s   ↓ %s", humanBytes(float64(st.BytesIn)), humanBytes(float64(st.BytesOut)))

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

								if !prevConnected {
									mSession.Show()
									mBandwidth.Show()
									mTotals.Show()
									mConnect.Hide()
									mDisconnect.Show()
								}

								if pName != prevActiveName {
									for _, pi := range profileItems {
										if pi.name == pName {
											pi.item.SetTitle("  ✓ " + pi.name)
										} else {
											pi.item.SetTitle("    " + pi.name)
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

								if prevConnected {
									mSession.Hide()
									mBandwidth.Hide()
									mTotals.Hide()
									mDisconnect.Hide()
									mConnect.Show()
									for _, pi := range profileItems {
										pi.item.SetTitle("    " + pi.name)
									}
									prevActiveName = ""
								}
							}

							prevConnected = st.Connected
						}
					}
				}()

				return // profile fetch succeeded, goroutines launched
			}
		}()
	}
}

