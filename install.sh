#!/usr/bin/env bash
set -e

# Determine if sudo is required
SUDO=""
if [ "$EUID" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    else
        echo "Error: This script requires root privileges. Please run as root or install sudo." >&2
        exit 1
    fi
fi

# Configuration variables
REPO="bembudo/health-analytics"  # Change to your GitHub repo
INSTALL_DIR="${INSTALL_DIR:-/opt/health-analytics}"
SERVICE_NAME="health-analytics"

echo "=== Installing Health Analytics Daemon ==="

# 1. Detect Architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)
        BINARY_NAME="health-analytics-linux-amd64"
        ;;
    aarch64|arm64)
        BINARY_NAME="health-analytics-linux-arm64"
        ;;
    armv7l|armv6l)
        BINARY_NAME="health-analytics-linux-armv7"
        ;;
    *)
        echo "Error: Unsupported architecture ($ARCH)"
        exit 1
        ;;
esac

# 2. Create Application Directory
echo "Creating installation directory at $INSTALL_DIR..."
$SUDO mkdir -p "$INSTALL_DIR"

# 3. Download Binary from GitHub Releases
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BINARY_NAME}"
echo "Downloading $BINARY_NAME from GitHub Releases..."
$SUDO curl -L -o "${INSTALL_DIR}/health-analytics-daemon" "$DOWNLOAD_URL"
$SUDO chmod +x "${INSTALL_DIR}/health-analytics-daemon"

# 4. Install Default Config if it doesn't exist
if [ ! -f "${INSTALL_DIR}/config.yaml" ]; then
    echo "Downloading default config.yaml..."
    CONFIG_URL="https://raw.githubusercontent.com/${REPO}/main/config.yaml"
    $SUDO curl -L -o "${INSTALL_DIR}/config.yaml" "$CONFIG_URL"
else
    echo "Existing config.yaml found. Preserving your configuration."
fi

# 5. Create Systemd Service File
echo "Configuring systemd service..."
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
$SUDO tee "$SERVICE_FILE" > /dev/null <<EOF
[Unit]
Description=Lightweight HTTP Heartbeat Healthchecks Daemon
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/health-analytics-daemon
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
EOF

# 6. Enable and Start Service (if systemd is available)
if command -v systemctl >/dev/null 2>&1 && systemctl list-units --type=target --all | grep -q basic.target; then
    echo "Enabling and starting systemd service..."
    $SUDO systemctl daemon-reload
    $SUDO systemctl enable "$SERVICE_NAME"
    $SUDO systemctl restart "$SERVICE_NAME"
    echo "=== Installation Complete! ==="
    echo "Check service status with: sudo systemctl status $SERVICE_NAME"
else
    echo "=== Installation Complete (Files & Config Installed)! ==="
    echo "Note: systemd is not active in this environment (e.g., Docker container), so the service was not started automatically."
fi
