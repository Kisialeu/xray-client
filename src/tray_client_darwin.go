package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
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
		client: &http.Client{Timeout: 40 * time.Second, Transport: controlTransport{controlTokenPath}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

type daemonStatus struct {
	Connected     bool             `json:"connected"`
	Status        connectionStatus `json:"status"`
	ActiveProfile string           `json:"active_profile"`
	UptimeS       int64            `json:"uptime_s"`
	BytesIn       int64            `json:"bytes_in"`
	BytesOut      int64            `json:"bytes_out"`
	Reconnects    int64            `json:"reconnects"`
}

type daemonSettings struct {
	AutoConnect   bool             `json:"auto_connect"`
	AutoReconnect bool             `json:"auto_reconnect"`
	ActiveProfile string           `json:"active_profile"`
	Status        connectionStatus `json:"status"`
}

type daemonProfiles struct {
	Profiles []struct {
		Name string `json:"name"`
		Flag string `json:"flag,omitempty"`
	} `json:"profiles"`
	Active string `json:"active"`
}

type trayClientProfileItem struct {
	name         string
	flag         string
	item         *systray.MenuItem
	latency      int
	latencyKnown bool
}

// trayClientProfileState owns profile menu metadata shared by refresh, ping,
// and status-rendering goroutines.
type trayClientProfileState struct {
	mu        sync.RWMutex
	items     []trayClientProfileItem
	latencies map[string]int
	flags     map[string]string
}

func newTrayClientProfileState() *trayClientProfileState {
	return &trayClientProfileState{
		latencies: make(map[string]int),
		flags:     make(map[string]string),
	}
}

func (s *trayClientProfileState) add(item trayClientProfileItem) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.items {
		if existing.name == item.name {
			return false
		}
	}
	s.items = append(s.items, item)
	return true
}

func (s *trayClientProfileState) contains(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.items {
		if item.name == name {
			return true
		}
	}
	return false
}

func (s *trayClientProfileState) updatePing(results []PingResult) []trayClientProfileItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, result := range results {
		s.latencies[result.Name] = result.LatencyMs
		if result.Flag != "" {
			s.flags[result.Name] = result.Flag
		}
	}
	return s.snapshotLocked()
}

func (s *trayClientProfileState) flag(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.flags[name]
}

func (s *trayClientProfileState) snapshot() []trayClientProfileItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *trayClientProfileState) snapshotLocked() []trayClientProfileItem {
	items := make([]trayClientProfileItem, len(s.items))
	copy(items, s.items)
	for i := range items {
		if latency, ok := s.latencies[items[i].name]; ok {
			items[i].latency = latency
			items[i].latencyKnown = true
		}
	}
	return items
}

// daemonSettingsUpdater serializes tray setting toggles so each toggle reads
// the result of the previous daemon update.
type daemonSettingsUpdater struct {
	mu      sync.Mutex
	current *atomic.Value
	version uint64
	update  func(connectionSettingsPatch) (ConnectionSettings, error)
}

func (u *daemonSettingsUpdater) toggleAutoConnect() error {
	return u.toggle(func(settings ConnectionSettings) bool { return !settings.AutoConnect }, func(patch *connectionSettingsPatch, value bool) {
		patch.AutoConnect = &value
	})
}

func (u *daemonSettingsUpdater) toggleAutoReconnect() error {
	return u.toggle(func(settings ConnectionSettings) bool { return !settings.AutoReconnect }, func(patch *connectionSettingsPatch, value bool) {
		patch.AutoReconnect = &value
	})
}

func (u *daemonSettingsUpdater) toggle(value func(ConnectionSettings) bool, set func(*connectionSettingsPatch, bool)) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	current := u.current.Load().(ConnectionSettings)
	next := value(current)
	var patch connectionSettingsPatch
	set(&patch, next)
	updated, err := u.update(patch)
	if err != nil {
		return err
	}
	u.current.Store(updated)
	u.version++
	return nil
}

func (u *daemonSettingsUpdater) set(settings ConnectionSettings) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.current.Store(settings)
	u.version++
}

func (u *daemonSettingsUpdater) versionAtStart() uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.version
}

func (u *daemonSettingsUpdater) setIfVersion(version uint64, settings ConnectionSettings) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.version != version {
		return false
	}
	u.current.Store(settings)
	u.version++
	return true
}

func (dc *daemonClient) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, dc.base+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return dc.client.Do(req)
}

func (dc *daemonClient) status(ctx context.Context) (daemonStatus, error) {
	resp, err := dc.request(ctx, http.MethodGet, "/status", nil)
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

func (dc *daemonClient) profiles(ctx context.Context) (daemonProfiles, error) {
	resp, err := dc.request(ctx, http.MethodGet, "/profiles", nil)
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

func (dc *daemonClient) connect(ctx context.Context, name string) error {
	body, err := json.Marshal(map[string]string{"profile": name})
	if err != nil {
		return err
	}
	resp, err := dc.request(ctx, http.MethodPost, "/connect", bytes.NewReader(body))
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

func (dc *daemonClient) refresh(ctx context.Context) error {
	resp, err := dc.request(ctx, http.MethodPost, "/refresh", nil)
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

func (dc *daemonClient) ping(ctx context.Context) ([]PingResult, error) {
	resp, err := dc.request(ctx, http.MethodGet, "/ping", nil)
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

func (dc *daemonClient) serverInfo(ctx context.Context) (*ServerInfo, error) {
	resp, err := dc.request(ctx, http.MethodGet, "/server-info", nil)
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

func (dc *daemonClient) disconnect(ctx context.Context) error {
	resp, err := dc.request(ctx, http.MethodPost, "/disconnect", nil)
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

func (dc *daemonClient) settings(ctx context.Context) (daemonSettings, error) {
	resp, err := dc.request(ctx, http.MethodGet, "/settings", nil)
	if err != nil {
		return daemonSettings{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return daemonSettings{}, fmt.Errorf("settings: status %d", resp.StatusCode)
	}
	var settings daemonSettings
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		return daemonSettings{}, err
	}
	return settings, nil
}

func (dc *daemonClient) updateSettings(ctx context.Context, patch connectionSettingsPatch) (daemonSettings, error) {
	body, err := json.Marshal(patch)
	if err != nil {
		return daemonSettings{}, err
	}
	resp, err := dc.request(ctx, http.MethodPost, "/settings", bytes.NewReader(body))
	if err != nil {
		return daemonSettings{}, err
	}
	defer resp.Body.Close()
	var result struct {
		OK            bool   `json:"ok"`
		Error         string `json:"error"`
		AutoConnect   bool   `json:"auto_connect"`
		AutoReconnect bool   `json:"auto_reconnect"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return daemonSettings{}, err
	}
	if !result.OK {
		return daemonSettings{}, fmt.Errorf("%s", result.Error)
	}
	return daemonSettings{AutoConnect: result.AutoConnect, AutoReconnect: result.AutoReconnect}, nil
}

func (dc *daemonClient) reconnect(ctx context.Context) error {
	resp, err := dc.request(ctx, http.MethodPost, "/reconnect", nil)
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

		profileState := newTrayClientProfileState()
		var selectedProfile atomic.Value
		selectedProfile.Store("")
		var currentSettings atomic.Value
		currentSettings.Store(defaultConnectionSettings())
		var refreshMu sync.Mutex
		settingsUpdater := &daemonSettingsUpdater{
			current: &currentSettings,
			update: func(patch connectionSettingsPatch) (ConnectionSettings, error) {
				updated, err := dc.updateSettings(ctx, patch)
				if err != nil {
					return ConnectionSettings{}, err
				}
				return ConnectionSettings{AutoConnect: updated.AutoConnect, AutoReconnect: updated.AutoReconnect}, nil
			},
		}

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

		updatePingResults := func(results []PingResult) {
			for _, pi := range profileState.updatePing(results) {
				lat := -1
				if pi.latencyKnown {
					lat = pi.latency
				}
				pi.item.SetTitle(formatProfileTitle("    ", pi.name, lat, pi.flag))
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
				profs, err := dc.profiles(ctx)
				if err != nil {
					logger.Debug("waiting for daemon", "err", err)
					timer := time.NewTimer(2 * time.Second)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
					continue
				}
				for _, p := range profs.Profiles {
					name := p.Name
					title := "    " + name
					if p.Flag != "" {
						title = "    " + p.Flag + " " + name
					}
					item := systray.AddMenuItem(title, name)
					profileState.add(trayClientProfileItem{name: name, flag: p.Flag, item: item})

					go func() {
						for range item.ClickedCh {
							selectedProfile.Store(name)
							logger.Info("switching profile", "profile", name)
							if err := dc.connect(ctx, name); err != nil {
								logger.Error("connect failed", "profile", name, "err", err)
							}
						}
					}()
				}
				if profs.Active != "" {
					selectedProfile.Store(profs.Active)
				} else if len(profs.Profiles) > 0 {
					selectedProfile.Store(profs.Profiles[0].Name)
				}
				if settings, err := dc.settings(ctx); err == nil {
					settingsUpdater.set(ConnectionSettings{AutoConnect: settings.AutoConnect, AutoReconnect: settings.AutoReconnect})
				}

				// Ping servers in background to show latency
				go func() {
					if results, err := dc.ping(ctx); err == nil {
						updatePingResults(results)
					}
				}()

				systray.AddSeparator()

				// Add action buttons after profiles are loaded
				mConnect := systray.AddMenuItem("Connect selected profile", "")
				mReconnect := systray.AddMenuItem("Reconnect now", "")
				mDisconnect := systray.AddMenuItem("Disconnect and stop", "")
				mDisconnect.Hide()
				mRefresh := systray.AddMenuItem("Refresh profiles", "")
				systray.AddSeparator()
				mSettingsLabel := systray.AddMenuItem("Connection Settings", "")
				mSettingsLabel.Disable()
				mAutoConnect := systray.AddMenuItem("", "")
				mAutoReconnect := systray.AddMenuItem("", "")
				setSettingTitle := func(item *systray.MenuItem, name string, enabled bool) {
					prefix := "    "
					if enabled {
						prefix = "  ✓ "
					}
					item.SetTitle(prefix + name)
				}
				setSettingTitle(mAutoConnect, "Auto-connect", currentSettings.Load().(ConnectionSettings).AutoConnect)
				setSettingTitle(mAutoReconnect, "Auto-reconnect", currentSettings.Load().(ConnectionSettings).AutoReconnect)
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
					info, err := dc.serverInfo(ctx)
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
					mInfoLeak.SetTitle("    Leak protection: not measured")
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
							if active, ok := selectedProfile.Load().(string); ok && active != "" {
								go func() {
									if err := dc.connect(ctx, active); err != nil {
										logger.Error("connect failed", "err", err)
									}
								}()
							}
						case <-mReconnect.ClickedCh:
							go func() {
								if err := dc.reconnect(ctx); err != nil {
									logger.Error("reconnect failed", "err", err)
								}
							}()
						case <-mAutoConnect.ClickedCh:
							go func() {
								if err := settingsUpdater.toggleAutoConnect(); err != nil {
									logger.Error("update auto-connect failed", "err", err)
								}
							}()
						case <-mAutoReconnect.ClickedCh:
							go func() {
								if err := settingsUpdater.toggleAutoReconnect(); err != nil {
									logger.Error("update auto-reconnect failed", "err", err)
								}
							}()
						case <-mDisconnect.ClickedCh:
							go func() {
								if err := dc.disconnect(ctx); err != nil {
									logger.Error("disconnect failed", "err", err)
								}
							}()
						case <-mRefresh.ClickedCh:
							go func() {
								refreshMu.Lock()
								defer refreshMu.Unlock()
								if err := dc.refresh(ctx); err != nil {
									logger.Error("refresh failed", "err", err)
									return
								}
								profs, err := dc.profiles(ctx)
								if err != nil {
									logger.Error("fetch profiles after refresh", "err", err)
									return
								}
								for _, p := range profs.Profiles {
									if profileState.contains(p.Name) {
										continue
									}
									item := systray.AddMenuItem("    "+p.Name, p.Name)
									title := "    " + p.Name
									if p.Flag != "" {
										title = "    " + p.Flag + " " + p.Name
									}
									item.SetTitle(title)
									if !profileState.add(trayClientProfileItem{name: p.Name, flag: p.Flag, item: item}) {
										continue
									}
									name := p.Name
									go func() {
										for range item.ClickedCh {
											selectedProfile.Store(name)
											logger.Info("switching profile", "profile", name)
											if err := dc.connect(ctx, name); err != nil {
												logger.Error("connect failed", "profile", name, "err", err)
											}
										}
									}()
								}
								logger.Info("profiles refreshed", "count", len(profs.Profiles))
								if results, err := dc.ping(ctx); err == nil {
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
							settingsVersion := settingsUpdater.versionAtStart()
							st, err := dc.status(ctx)
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
							if settings, settingsErr := dc.settings(ctx); settingsErr == nil {
								value := ConnectionSettings{AutoConnect: settings.AutoConnect, AutoReconnect: settings.AutoReconnect}
								if settingsUpdater.setIfVersion(settingsVersion, value) {
									setSettingTitle(mAutoConnect, "Auto-connect", value.AutoConnect)
									setSettingTitle(mAutoReconnect, "Auto-reconnect", value.AutoReconnect)
								}
							}

							rxRate := float64(st.BytesIn-prevIn) / metricsInterval.Seconds()
							txRate := float64(st.BytesOut-prevOut) / metricsInterval.Seconds()
							prevIn, prevOut = st.BytesIn, st.BytesOut

							statusValue := st.Status
							if statusValue == "" {
								statusValue = statusDisconnected
								if st.Connected {
									statusValue = statusConnected
								}
							}
							if statusValue == statusConnected {
								pName := st.ActiveProfile
								if f := profileState.flag(pName); f != "" {
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

								flagStr := profileState.flag(pName)
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
									for _, pi := range profileState.snapshot() {
										lat := -1
										if pi.latencyKnown {
											lat = pi.latency
										}
										pi.item.SetTitle(formatProfileTitle("    ", pi.name, lat, pi.flag))

									}
									prevActiveName = pName
								}
							} else {
								systray.SetTemplateIcon(iconDisc(), iconDisc())
								if statusValue == statusConnecting || statusValue == statusReconnecting {
									mDisconnect.Show()
								} else {
									mDisconnect.Hide()
								}

								statusTitle := "⚫  Disconnected"
								switch statusValue {
								case statusConnecting:
									statusTitle = "🟡  Connecting"
								case statusReconnecting:
									statusTitle = "🟠  Reconnecting"
								case statusFailed:
									statusTitle = "🔴  Operation failed"
								}
								if statusTitle != prevStatus {
									mStatusLine.SetTitle(statusTitle)
									prevStatus = statusTitle
								}

								if prevConnected {
									mSession.Hide()
									mBandwidth.Hide()
									mTotals.Hide()
									mDisconnect.Hide()
									mConnect.Show()
									mReconnect.Show()
									mInfoIP.SetTitle("    IP: —")
									mInfoCountry.SetTitle("    Location: —")
									mInfoProto.SetTitle("    Protocol: —")
									mInfoDNS.SetTitle("    DNS: —")
									mInfoLeak.SetTitle("    IP Leak: —")
									for _, pi := range profileState.snapshot() {
										lat := -1
										if pi.latencyKnown {
											lat = pi.latency
										}
										pi.item.SetTitle(formatProfileTitle("    ", pi.name, lat, pi.flag))
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
