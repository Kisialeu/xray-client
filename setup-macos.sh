#!/bin/bash
set -euo pipefail

BINARY_NAME="xray-cli"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/usr/local/etc/xray-cli"
DAEMON_ADDR="127.0.0.1:19099"
DAEMON_PLIST="/Library/LaunchDaemons/com.xray-cli.plist"

REAL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo "$USER")}"
REAL_HOME=$(dscl . -read "/Users/$REAL_USER" NFSHomeDirectory | awk '{print $2}')
AGENT_PLIST="$REAL_HOME/Library/LaunchAgents/com.xray-cli.tray.plist"

usage() {
    echo "Usage: sudo $0 <command> [options]"
    echo ""
    echo "Commands:"
    echo "  install [config.yaml]   Install daemon + tray (default config: servers.yaml)"
    echo "  uninstall               Remove everything"
    echo "  start                   Start services"
    echo "  stop                    Stop services"
    echo "  restart                 Restart services"
    echo "  status                  Show service status"
    exit 1
}

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        echo "This script must be run as root (sudo)."
        exit 1
    fi
}

stop_services() {
    launchctl unload "$DAEMON_PLIST" 2>/dev/null || true
    sudo -u "$REAL_USER" launchctl unload "$AGENT_PLIST" 2>/dev/null || true
}

start_services() {
    launchctl load "$DAEMON_PLIST"
    sudo -u "$REAL_USER" launchctl load "$AGENT_PLIST"
}

do_install() {
    ARCH="$(uname -m)"
    case "$ARCH" in
        arm64)  BINARY_SRC="dist/darwin/xray-cli-arm64" ;;
        x86_64) BINARY_SRC="dist/darwin/xray-cli-amd64" ;;
        *)      echo "Unsupported architecture: $ARCH"; exit 1 ;;
    esac

    if [ ! -f "$BINARY_SRC" ]; then
        echo "Binary not found at $BINARY_SRC — run ./build.sh first."
        exit 1
    fi

    CONFIG_SRC="${1:-servers.yaml}"
    if [ ! -f "$CONFIG_SRC" ]; then
        echo "Config not found: $CONFIG_SRC"
        echo "Usage: sudo $0 install [path/to/servers.yaml]"
        exit 1
    fi

    echo "==> Stopping existing services (if any)..."
    stop_services

    echo "==> Installing binary to $INSTALL_DIR/$BINARY_NAME"
    cp "$BINARY_SRC" "$INSTALL_DIR/$BINARY_NAME"
    chmod 755 "$INSTALL_DIR/$BINARY_NAME"

    echo "==> Installing config to $CONFIG_DIR/"
    mkdir -p "$CONFIG_DIR"
    cp "$CONFIG_SRC" "$CONFIG_DIR/servers.yaml"
    chmod 600 "$CONFIG_DIR/servers.yaml"

    echo "==> Writing LaunchDaemon plist (root, headless VPN)"
    cat > "$DAEMON_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.xray-cli</string>
  <key>ProgramArguments</key>
  <array>
    <string>${INSTALL_DIR}/${BINARY_NAME}</string>
    <string>--config</string>
    <string>${CONFIG_DIR}/servers.yaml</string>
    <string>--daemon-addr</string>
    <string>${DAEMON_ADDR}</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/dev/null</string>
  <key>StandardErrorPath</key>
  <string>/dev/null</string>
</dict>
</plist>
PLIST
    chmod 644 "$DAEMON_PLIST"

    echo "==> Writing LaunchAgent plist (user, tray icon)"
    mkdir -p "$REAL_HOME/Library/LaunchAgents"
    cat > "$AGENT_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.xray-cli.tray</string>
  <key>ProgramArguments</key>
  <array>
    <string>${INSTALL_DIR}/${BINARY_NAME}</string>
    <string>--tray</string>
    <string>--daemon-addr</string>
    <string>${DAEMON_ADDR}</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>LimitLoadToSessionType</key>
  <string>Aqua</string>
  <key>StandardOutPath</key>
  <string>/dev/null</string>
  <key>StandardErrorPath</key>
  <string>/dev/null</string>
</dict>
</plist>
PLIST
    chown "$REAL_USER" "$AGENT_PLIST"
    chmod 644 "$AGENT_PLIST"

    echo "==> Starting services..."
    start_services

    echo ""
    echo "Done. VPN daemon + tray icon are running."
}

do_uninstall() {
    echo "==> Stopping services..."
    stop_services

    echo "==> Removing plists..."
    rm -f "$DAEMON_PLIST"
    rm -f "$AGENT_PLIST"

    echo "==> Removing binary..."
    rm -f "$INSTALL_DIR/$BINARY_NAME"

    echo "==> Removing config..."
    rm -rf "$CONFIG_DIR"

    echo "==> Removing logs..."
    rm -f /var/log/xray-cli.log
    rm -f /tmp/xray-cli-tray.log

    echo ""
    echo "Done. Everything removed."
}

do_status() {
    echo "Daemon:"
    sudo launchctl list 2>/dev/null | grep xray-cli || echo "  not loaded"
    echo "Tray:"
    sudo -u "$REAL_USER" launchctl list 2>/dev/null | grep xray-cli || echo "  not loaded"
    echo ""
    pid=$(pgrep -f "xray-cli.*daemon-addr" | head -1 2>/dev/null || true)
    if [ -n "$pid" ]; then
        ps -p "$pid" -o pid=,%cpu=,%mem=,rss= | awk '{printf "Daemon PID %s — CPU: %s%%  RAM: %.1f MB\n", $1, $2, $4/1024}'
    else
        echo "Daemon process not running"
    fi
}

# ── main ──────────────────────────────────────────────────────────────────────

COMMAND="${1:-}"
shift 2>/dev/null || true

case "$COMMAND" in
    install)
        require_root
        do_install "$@"
        ;;
    uninstall)
        require_root
        do_uninstall
        ;;
    start)
        require_root
        echo "==> Starting services..."
        start_services
        echo "Done."
        ;;
    stop)
        require_root
        echo "==> Stopping services..."
        stop_services
        echo "Done."
        ;;
    restart)
        require_root
        echo "==> Restarting services..."
        stop_services
        sleep 1
        start_services
        echo "Done."
        ;;
    status)
        require_root
        do_status
        ;;
    *)
        usage
        ;;
esac
