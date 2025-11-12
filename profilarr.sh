#!/usr/bin/env bash
# ==============================================================================
# 📦 Proxmox LXC Installer for Profilarr (No Docker, Lightweight)
# Author: Simon Ouellet / Serveur Le Refuge
# Inspired by Proxmox Community Scripts style
# ==============================================================================
# This script automatically:
#   - Detects and downloads the latest Debian LXC template
#   - Creates a lightweight, unprivileged container
#   - Installs Profilarr natively with Node.js
#   - Enables root autologin in Proxmox console
#   - Fixes locale and npm issues
#   - Starts the service and prints access info
# ==============================================================================

set -e

# --- Configuration ---
APP="Profilarr"
CTID=${CTID:-118}
HOSTNAME="profilarr"
MEMORY="1024"
STORAGE="local-lvm"
BRIDGE="vmbr0"
NODE_VERSION="20"
PORT="9898"

# --- Styling ---
YELLOW='\033[1;33m'
GREEN='\033[1;32m'
CYAN='\033[1;36m'
NC='\033[0m'

echo -e "${CYAN}🔍 Detecting latest Debian LXC template...${NC}"

# --- Find newest Debian standard template ---
LATEST_TEMPLATE=$(pveam available | grep 'debian-[0-9][0-9]-standard' | sort -V | tail -n 1 | awk '{print $2}')

if [ -z "$LATEST_TEMPLATE" ]; then
  echo -e "${YELLOW}⚠️  No Debian template found. Updating template list...${NC}"
  pveam update
  LATEST_TEMPLATE=$(pveam available | grep 'debian-[0-9][0-9]-standard' | sort -V | tail -n 1 | awk '{print $2}')
  if [ -z "$LATEST_TEMPLATE" ]; then
    echo "❌ Still no template found. Exiting."
    exit 1
  fi
fi

# --- Download if missing ---
if ! pveam list local | grep -q $(basename "$LATEST_TEMPLATE"); then
  echo -e "${YELLOW}📦 Downloading latest Debian template: ${LATEST_TEMPLATE}${NC}"
  pveam download local "$LATEST_TEMPLATE"
else
  echo -e "${GREEN}✅ Template already downloaded: ${LATEST_TEMPLATE}${NC}"
fi

# --- Create the container ---
echo -e "${CYAN}🧱 Creating LXC container ($HOSTNAME, CTID $CTID)...${NC}"

pct create "$CTID" "local:vztmpl/$(basename "$LATEST_TEMPLATE")" \
  --hostname "$HOSTNAME" \
  --cores 2 \
  --memory "$MEMORY" \
  --swap 256 \
  --net0 name=eth0,bridge="$BRIDGE",ip=dhcp \
  --rootfs "$STORAGE":8 \
  --features nesting=1 \
  --unprivileged 1 \
  --onboot 1

# --- Enable root autologin on Proxmox console ---
echo -e "${YELLOW}🔓 Enabling root autologin for console access...${NC}"
pct exec "$CTID" -- bash -c '
mkdir -p /etc/systemd/system/getty@tty1.service.d
cat <<EOF > /etc/systemd/system/getty@tty1.service.d/autologin.conf
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear %I \$TERM
EOF
systemctl daemon-reload
'

echo -e "${GREEN}✅ Container created successfully.${NC}"

# --- Start the container ---
pct start "$CTID"
sleep 5

echo -e "${CYAN}⚙️ Installing $APP inside CT $CTID...${NC}"

pct exec "$CTID" -- bash -c "
set -e

# --- Fix locale warnings ---
echo 'en_US.UTF-8 UTF-8' > /etc/locale.gen
apt update -y && apt install -y locales
locale-gen en_US.UTF-8
update-locale LANG=en_US.UTF-8

# --- Install dependencies ---
apt install -y curl git sudo ca-certificates gnupg lsb-release apt-transport-https

# --- Install Node.js (LTS) ---
curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash -
apt install -y nodejs

# --- Create dedicated non-root user ---
useradd -r -s /usr/sbin/nologin profilarr || true

# --- Install Profilarr ---
cd /opt
git clone https://github.com/Dictionarry-Hub/profilarr.git
cd profilarr
npm install --omit=dev
chown -R profilarr:profilarr /opt/profilarr

# --- Create systemd service ---
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

# --- Get container IP ---
IP=$(pct exec "$CTID" -- hostname -I | awk '{print $1}')

echo ""
echo -e "${GREEN}✅ $APP successfully installed and running inside CT $CTID${NC}"
echo ""
echo "🌐 Access it at: http://$IP:$PORT"
echo "📦 Hostname: $HOSTNAME"
echo "📁 Installed in: /opt/profilarr"
echo "🧩 Service: systemctl status profilarr"
echo ""
echo "🔄 To update later:"
echo "   pct exec $CTID -- bash -c 'cd /opt/profilarr && git pull && npm install --omit=dev && systemctl restart profilarr'"
echo ""
echo -e "${CYAN}🎉 Installation complete!${NC}"
