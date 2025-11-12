#!/usr/bin/env bash
# ==============================================================================
# 📦 Proxmox LXC Installer for Profilarr (No Docker, Lightweight)
# Author: Simon Ouellet / Serveur Le Refuge
# ==============================================================================
#  • Detects and downloads latest Debian template (11–13+)
#  • Creates lightweight unprivileged LXC
#  • Installs Profilarr natively with Node.js 20
#  • Enables root autologin on Proxmox console (Debian-11→13)
#  • Fixes locale warnings
#  • Starts container + prints access URL
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

YELLOW='\033[1;33m'; GREEN='\033[1;32m'; CYAN='\033[1;36m'; NC='\033[0m'

echo -e "${CYAN}🔍 Detecting latest Debian LXC template...${NC}"
LATEST_TEMPLATE=$(pveam available | grep 'debian-[0-9][0-9]-standard' | sort -V | tail -n 1 | awk '{print $2}')
if [ -z "$LATEST_TEMPLATE" ]; then
  echo -e "${YELLOW}⚠️  No template list found, updating...${NC}"
  pveam update
  LATEST_TEMPLATE=$(pveam available | grep 'debian-[0-9][0-9]-standard' | sort -V | tail -n 1 | awk '{print $2}') || true
  [ -z "$LATEST_TEMPLATE" ] && echo "❌ Could not find Debian template." && exit 1
fi
if ! pveam list local | grep -q $(basename "$LATEST_TEMPLATE"); then
  echo -e "${YELLOW}📦 Downloading ${LATEST_TEMPLATE}${NC}"
  pveam download local "$LATEST_TEMPLATE"
else
  echo -e "${GREEN}✅ Template already present${NC}"
fi

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
echo -e "${GREEN}✅ Container created.${NC}"

# --- Enable root autologin on Proxmox console (covers Debian 11 → 13+) ---
echo -e "${YELLOW}🔓 Configuring root autologin for console access...${NC}"
pct exec "$CTID" -- bash -c '
# Debian 13+ (container-getty@1.service)
if systemctl list-unit-files | grep -q "^container-getty@1.service"; then
  mkdir -p /etc/systemd/system/container-getty@1.service.d
  cat <<EOF >/etc/systemd/system/container-getty@1.service.d/override.conf
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear tty1 linux
EOF
  systemctl daemon-reload
  systemctl restart container-getty@1.service
# Debian 12 and earlier (console-getty or getty@tty1)
elif systemctl list-unit-files | grep -q "^console-getty.service"; then
  mkdir -p /etc/systemd/system/console-getty.service.d
  cat <<EOF >/etc/systemd/system/console-getty.service.d/override.conf
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear console 115200,38400,9600 \$TERM
EOF
  systemctl daemon-reload
  systemctl restart console-getty.service
else
  mkdir -p /etc/systemd/system/getty@tty1.service.d
  cat <<EOF >/etc/systemd/system/getty@tty1.service.d/autologin.conf
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear %I \$TERM
EOF
  systemctl daemon-reload
  systemctl restart getty@tty1.service
fi
'


# --- Start container ---
pct start "$CTID"
sleep 5

echo -e "${CYAN}⚙️ Installing $APP inside CT $CTID...${NC}"
pct exec "$CTID" -- bash -c "
set -e
echo 'en_US.UTF-8 UTF-8' > /etc/locale.gen
apt update -y && apt install -y locales
locale-gen en_US.UTF-8 && update-locale LANG=en_US.UTF-8
apt install -y curl git sudo ca-certificates gnupg lsb-release apt-transport-https
curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash -
apt install -y nodejs
useradd -r -s /usr/sbin/nologin profilarr || true
cd /opt
git clone https://github.com/Dictionarry-Hub/profilarr.git
cd profilarr
npm install --omit=dev
chown -R profilarr:profilarr /opt/profilarr

cat <<EOF >/etc/systemd/system/profilarr.service
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

# --- Ensure container auto-starts and is running now ---
pct set "$CTID" -onboot 1
pct start "$CTID" || true

IP=$(pct exec "$CTID" -- hostname -I | awk '{print $1}')
echo ""
echo -e "${GREEN}✅ $APP is installed and running in CT $CTID${NC}"
echo ""
echo "🌐 Access: http://$IP:$PORT"
echo "📦 Hostname: $HOSTNAME"
echo "📁 Path: /opt/profilarr"
echo "🧩 Service: systemctl status profilarr"
echo ""
echo "🔄 Update later with:"
echo "   pct exec $CTID -- bash -c 'cd /opt/profilarr && git pull && npm install --omit=dev && systemctl restart profilarr'"
echo ""
echo -e "${CYAN}🎉 Installation complete! Container will now autostart with Proxmox.${NC}"
