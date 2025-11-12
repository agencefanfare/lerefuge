#!/usr/bin/env bash
# ==============================================================================
# 📦 Proxmox LXC Installer for Profilarr (No Docker, Lightweight)
# Author: Simon Ouellet / Serveur Le Refuge
# Inspired by Proxmox Community Scripts style
# ==============================================================================

set -e

APP="Profilarr"
CTID=${CTID:-118}
HOSTNAME="profilarr"
MEMORY="1024"
STORAGE="local-lvm"
BRIDGE="vmbr0"
NODE_VERSION="20"
PORT="9898"

# Colors
YELLOW='\033[1;33m'
GREEN='\033[1;32m'
NC='\033[0m'

echo -e "${YELLOW}🧱 Creating $APP LXC container...${NC}"

# --- Create container ---
pveam update >/dev/null
TEMPLATE=$(pveam available | grep debian-12 | head -n1 | awk '{print $1}')
if [ -z "$TEMPLATE" ]; then
  echo "❌ No Debian 12 template found. Please download one with:"
  echo "pveam download local debian-12-standard_12.*_amd64.tar.zst"
  exit 1
fi

pct create $CTID $TEMPLATE \
  --hostname $HOSTNAME \
  --cores 2 \
  --memory $MEMORY \
  --swap 256 \
  --net0 name=eth0,bridge=$BRIDGE,ip=dhcp \
  --rootfs $STORAGE:8 \
  --features nesting=1 \
  --unprivileged 1 \
  --onboot 1

echo -e "${GREEN}✅ Container created (ID $CTID)${NC}"

# --- Start container ---
pct start $CTID
sleep 5

echo -e "${YELLOW}⚙️ Installing $APP inside the container...${NC}"

# --- Run installation inside LXC ---
pct exec $CTID -- bash -c "
set -e
apt update -y && apt install -y curl git sudo ca-certificates gnupg lsb-release

# Install Node.js
curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash -
apt install -y nodejs

# Create user
useradd -r -s /usr/sbin/nologin profilarr

# Clone Profilarr
cd /opt
git clone https://github.com/Dictionarry-Hub/profilarr.git
cd profilarr
npm ci --omit=dev
chown -R profilarr:profilarr /opt/profilarr

# Create systemd service
cat <<EOF > /etc/systemd/system/profilarr.service
[Unit]
Description=Profilarr (Radarr/Sonarr Profile Manager)
After=network.target

[Service]
Type=simple
User=profilarr
WorkingDirectory=/opt/profilarr
ExecStart=/usr/bin/npm run start
Restart=on-failure
Environment=NODE_ENV=production
StandardOutput=append:/var/log/profilarr.log
StandardError=append:/var/log/profilarr.err.log

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable profilarr
systemctl start profilarr
"

# --- Get IP ---
IP=$(pct exec $CTID -- hostname -I | awk '{print $1}')

echo ""
echo -e "${GREEN}✅ $APP is now installed and running inside CT $CTID${NC}"
echo ""
echo "🌐 Access it at: http://$IP:$PORT"
echo "📦 LXC Hostname: $HOSTNAME"
echo "📁 App directory: /opt/profilarr"
echo "🧩 Service: systemctl status profilarr"
echo ""
echo "🔄 To update later:"
echo "   pct exec $CTID -- bash -c 'cd /opt/profilarr && git pull && npm ci --omit=dev && systemctl restart profilarr'"
echo ""
echo "🎉 Installation complete!"
