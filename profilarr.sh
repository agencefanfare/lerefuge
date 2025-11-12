#!/usr/bin/env bash
# ==============================================================================
# 👋 Profilarr LXC Installer for Proxmox (no Docker, lightweight)
# Author: Simon / Serveur Le Refuge (based on Community Scripts style)
# ==============================================================================

set -e

APP="Profilarr"
REPO="https://github.com/Dictionarry-Hub/profilarr"
INSTALL_DIR="/opt/profilarr"
SERVICE_FILE="/etc/systemd/system/profilarr.service"
NODE_VERSION="20"

echo "⚙️ Installing $APP natively (Node.js v$NODE_VERSION)..."

# --- Update system ---
apt update -y
apt upgrade -y
apt install -y curl git sudo ca-certificates lsb-release gnupg

# --- Install Node.js (LTS) ---
if ! command -v node >/dev/null 2>&1; then
  echo "📦 Installing Node.js..."
  curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash -
  apt install -y nodejs
else
  echo "✅ Node.js already installed."
fi

# --- Create system user ---
if ! id -u profilarr >/dev/null 2>&1; then
  useradd -r -s /usr/sbin/nologin profilarr
fi

# --- Download and install Profilarr ---
echo "⬇️ Cloning $REPO..."
rm -rf "$INSTALL_DIR"
git clone "$REPO" "$INSTALL_DIR"

cd "$INSTALL_DIR"
npm ci --omit=dev

# --- Permissions ---
chown -R profilarr:profilarr "$INSTALL_DIR"

# --- Create systemd service ---
echo "🧩 Creating systemd service..."
cat <<EOF > $SERVICE_FILE
[Unit]
Description=Profilarr (Radarr/Sonarr Profile Manager)
After=network.target

[Service]
Type=simple
User=profilarr
WorkingDirectory=$INSTALL_DIR
ExecStart=/usr/bin/npm run start
Restart=on-failure
Environment=NODE_ENV=production
StandardOutput=append:/var/log/profilarr.log
StandardError=append:/var/log/profilarr.err.log

[Install]
WantedBy=multi-user.target
EOF

# --- Enable and start service ---
systemctl daemon-reload
systemctl enable profilarr
systemctl start profilarr

# --- Done ---
IP=$(hostname -I | awk '{print $1}')
echo ""
echo "✅ $APP installation complete!"
echo "🌐 Access it at: http://$IP:9898"
echo "📁 Installed in: $INSTALL_DIR"
echo "🧱 Service: systemctl status profilarr"
echo "🔄 To update later:"
echo "   cd $INSTALL_DIR && sudo -u profilarr git pull && npm ci --omit=dev && systemctl restart profilarr"
echo ""
