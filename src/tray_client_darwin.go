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

func (dc *daemonClient) ping() ([]PingResult, error) {
	resp, err := dc.client.Get(dc.base + "/ping")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		Results []PingResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Results, nil
}

func (dc *daemonClient) serverInfo() (*ServerInfo, error) {
	resp, err := dc.client.Get(dc.base + "/server-info")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server-info: status %d", resp.StatusCode)
	}
	var info ServerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
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

		formatProfileTitle := func(prefix, name string, latencyMs int, flag string) string {
			if flag != "" {
				name = flag + " " + name
			}
			if latencyMs > 0 {
				return fmt.Sprintf("%s%s  (%dms)", prefix, name, latencyMs)
			}
			if latencyMs == 0 {
				return fmt.Sprintf("%s%s  (<1ms)", prefix, name)
			}
			return prefix + name
		}

		latencies := make(map[string]int)
		flags := make(map[string]string)

		updatePingResults := func(results []PingResult) {
			for _, r := range results {
				latencies[r.Name] = r.LatencyMs
				if r.Flag != "" {
					flags[r.Name] = r.Flag
				}
			}
			for _, pi := range profileItems {
				lat, ok := latencies[pi.name]
				if !ok {
					lat = -1
				}
				pi.item.SetTitle(formatProfileTitle("    ", pi.name, lat, flags[pi.name]))
			}
		}

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

				// Ping servers in background to show latency
				go func() {
					if results, err := dc.ping(); err == nil {
						updatePingResults(results)
					}
				}()

				systray.AddSeparator()

				// Add action buttons after profiles are loaded
				mConnect := systray.AddMenuItem("Connect", "")
				mDisconnect := systray.AddMenuItem("Disconnect", "")
				mDisconnect.Hide()
				mRefresh := systray.AddMenuItem("Refresh profiles", "")
				systray.AddSeparator()

				mServerInfoLabel := systray.AddMenuItem("Server Info", "")
				mServerInfoLabel.Disable()
				mInfoIP := systray.AddMenuItem("    IP: —", "")
				mInfoIP.Disable()
				mInfoCountry := systray.AddMenuItem("    Location: —", "")
				mInfoCountry.Disable()
				mInfoProto := systray.AddMenuItem("    Protocol: —", "")
				mInfoProto.Disable()
				mInfoDNS := systray.AddMenuItem("    DNS: —", "")
				mInfoDNS.Disable()
				mInfoLeak := systray.AddMenuItem("    IP Leak: —", "")
				mInfoLeak.Disable()

				updateServerInfo := func() {
					info, err := dc.serverInfo()
					if err != nil {
						logger.Debug("server info fetch failed", "err", err)
						return
					}
					if info.PublicIP != "" {
						mInfoIP.SetTitle("    IP: " + info.PublicIP)
					}
					if info.Flag != "" {
						mInfoCountry.SetTitle("    Location: " + info.Flag + " " + info.Country)
					} else {
						mInfoCountry.SetTitle("    Location: —")
					}
					mInfoProto.SetTitle("    Protocol: " + info.Protocol)
					if info.DNSServer != "" {
						mInfoDNS.SetTitle("    DNS: " + info.DNSServer)
					}
					if info.IPLeak {
						mInfoLeak.SetTitle("    IP Leak: ⚠ DETECTED")
					} else {
						mInfoLeak.SetTitle("    IP Leak: ✓ None")
					}
				}

				serverInfoCh := make(chan struct{}, 1)
				triggerServerInfo := func() {
					select {
					case serverInfoCh <- struct{}{}:
					default:
					}
				}

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
								if results, err := dc.ping(); err == nil {
									updatePingResults(results)
								}
							}()
						}
					}
				}()

				// Server info auto-refresh: on signal or every 60s while connected
				go func() {
					const serverInfoInterval = 60 * time.Second
					t := time.NewTicker(serverInfoInterval)
					defer t.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-serverInfoCh:
							updateServerInfo()
							t.Reset(serverInfoInterval)
						case <-t.C:
							updateServerInfo()
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
								pName := st.ActiveProfile
								if f := flags[pName]; f != "" {
									if icon := renderEmojiIcon(f, 22); icon != nil {
										systray.SetIcon(icon)
									}
								} else {
									systray.SetTemplateIcon(iconConn(), iconConn())
								}
								session := ""
								if st.UptimeS > 0 {
									session = formatDuration(time.Duration(st.UptimeS) * time.Second)
								}

								flagStr := flags[pName]
								status := "🟢  "
								if flagStr != "" {
									status += flagStr + " "
								}
								status += pName
								bw := fmt.Sprintf(" ↑ %s/s   ↓ %s/s", humanBytes(rxRate), humanBytes(txRate))
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
									triggerServerInfo()
								}

								if pName != prevActiveName {
									triggerServerInfo()
									for _, pi := range profileItems {
										lat, ok := latencies[pi.name]
										if !ok {
											lat = -1
										}
                                        pi.item.SetTitle(formatProfileTitle("    ", pi.name, lat, flags[pi.name]))

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
									mInfoIP.SetTitle("    IP: —")
									mInfoCountry.SetTitle("    Location: —")
									mInfoProto.SetTitle("    Protocol: —")
									mInfoDNS.SetTitle("    DNS: —")
									mInfoLeak.SetTitle("    IP Leak: —")
									for _, pi := range profileItems {
										lat, ok := latencies[pi.name]
										if !ok {
											lat = -1
										}
										pi.item.SetTitle(formatProfileTitle("    ", pi.name, lat, flags[pi.name]))
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

