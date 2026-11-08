#!/usr/bin/env bash
set -e

# Configuration variables
REPO="https://github.com/bembudo/health-analytics.git"  # Change to your GitHub repo
INSTALL_DIR="/home/orangepi/apps/health-analytics"
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
done

# 2. Create Application Directory
echo "Creating installation directory at $INSTALL_DIR..."
sudo mkdir -p "$INSTALL_DIR"

# 3. Download Latest Binary from GitHub Releases
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${BINARY_NAME}"
echo "Downloading binary for $ARCH..."
sudo curl -L -o "${INSTALL_DIR}/health-analytics-daemon" "$DOWNLOAD_URL"
sudo chmod +x "${INSTALL_DIR}/health-analytics-daemon"

# 4. Install Default Config if it doesn't exist
if [ ! -f "${INSTALL_DIR}/config.yaml" ]; then
    echo "Downloading default config.yaml..."
    CONFIG_URL="https://raw.githubusercontent.com/${REPO}/main/config.yaml"
    sudo curl -L -o "${INSTALL_DIR}/config.yaml" "$CONFIG_URL"
else
    echo "Existing config.yaml found. Preserving your configuration."
fi

# 5. Create Systemd Service File
echo "Configuring systemd service..."
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
sudo bash -c "cat > $SERVICE_FILE" <<EOF
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

# 6. Enable and Start Service
echo "Enabling and starting systemd service..."
sudo systemctl daemon-reload
sudo systemctl enable "$SERVICE_NAME"
sudo systemctl restart "$SERVICE_NAME"

echo "=== Installation Complete! ==="
echo "Check service status with: sudo systemctl status $SERVICE_NAME"
