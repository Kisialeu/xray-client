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

# Detect arch
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
    echo "Usage: sudo $0 [path/to/servers.yaml]"
    exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root (sudo)."
    exit 1
fi

echo "==> Stopping existing services (if any)..."
launchctl unload "$DAEMON_PLIST" 2>/dev/null || true
sudo -u "$REAL_USER" launchctl unload "$AGENT_PLIST" 2>/dev/null || true

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

echo "==> Loading daemon"
launchctl load "$DAEMON_PLIST"

echo "==> Loading tray agent (user: $REAL_USER)"
sudo -u "$REAL_USER" launchctl load "$AGENT_PLIST"

echo ""
echo "Done. VPN daemon + tray icon are running."
echo ""
echo "  Daemon status:  sudo launchctl list | grep xray-cli"
echo "  Tray status:    launchctl list | grep xray-cli"
echo ""
echo "  Stop daemon:    sudo launchctl unload $DAEMON_PLIST"
echo "  Stop tray:      launchctl unload $AGENT_PLIST"
echo "  Start daemon:   sudo launchctl load $DAEMON_PLIST"
echo "  Start tray:     launchctl load $AGENT_PLIST"
