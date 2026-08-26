#!/bin/bash
set -euo pipefail

DAEMON_PLIST="/Library/LaunchDaemons/com.xray-cli.plist"

REAL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo "$USER")}"
REAL_HOME=$(dscl . -read "/Users/$REAL_USER" NFSHomeDirectory | awk '{print $2}')
AGENT_PLIST="$REAL_HOME/Library/LaunchAgents/com.xray-cli.tray.plist"

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root (sudo)."
    exit 1
fi

echo "==> Stopping services..."
launchctl unload "$DAEMON_PLIST" 2>/dev/null || true
sudo -u "$REAL_USER" launchctl unload "$AGENT_PLIST" 2>/dev/null || true

echo "==> Removing plists..."
rm -f "$DAEMON_PLIST"
rm -f "$AGENT_PLIST"

echo "==> Removing binary..."
rm -f /usr/local/bin/xray-cli

echo "==> Removing config..."
rm -rf /usr/local/etc/xray-cli

echo "==> Removing logs..."
rm -f /var/log/xray-cli.log
rm -f /tmp/xray-cli-tray.log

echo ""
echo "Done. Everything removed."
