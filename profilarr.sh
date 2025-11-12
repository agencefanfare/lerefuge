#!/usr/bin/env bash
# ==============================================================================
# 🐳 Serveur Le Refuge - Profilarr Helper Script
# Author: Simon Ouellet
# Description: Installs Profilarr (Docker version) in a Proxmox LXC container
# ==============================================================================
#   • Automatically detects latest Debian template (11–13)
#   • Creates a lightweight, unprivileged LXC
#   • Installs Docker & Docker Compose
#   • Deploys Profilarr container automatically
#   • Enables root autologin for Proxmox console
#   • Starts on boot
# ==============================================================================

set -e

APP="Profilarr"
CTID=${CTID:-118}
HOSTNAME="profilarr"
MEMORY="1024"
STORAGE="local-lvm"
BRIDGE="vmbr0"
PORT="6868"

YELLOW='\033[1;33m'; GREEN='\033[1;32m'; CYAN='\033[1;36m'; NC='\033[0m'

echo -e "${CYAN}🔍 Detecting latest Debian template...${NC}"
LATEST_TEMPLATE=$(pveam available | grep 'debian-[0-9][0-9]-standard' | sort -V | tail -n 1 | awk '{print $2}')
if [ -z "$LATEST_TEMPLATE" ]; then
  echo -e "${YELLOW}⚠️  Updating template list...${NC}"
  pveam update
  LATEST_TEMPLATE=$(pveam available | grep 'debian-[0-9][0-9]-standard' | sort -V | tail -n 1 | awk '{print $2}')
fi

if ! pveam list local | grep -q $(basename "$LATEST_TEMPLATE"); then
  echo -e "${YELLOW}📦 Downloading ${LATEST_TEMPLATE}${NC}"
  pveam download local "$LATEST_TEMPLATE"
else
  echo -e "${GREEN}✅ Template already available${NC}"
fi

echo -e "${CYAN}🧱 Creating LXC container ($HOSTNAME, CTID $CTID)...${NC}"
pct create "$CTID" "local:vztmpl/$(basename "$LATEST_TEMPLATE")" \
  --hostname "$HOSTNAME" \
  --cores 2 \
  --memory "$MEMORY" \
  --swap 256 \
  --net0 name=eth0,bridge="$BRIDGE",ip=dhcp \
  --rootfs "$STORAGE":8 \
  --features nesting=1,keyctl=1 \
  --unprivileged 1 \
  --onboot 1

# --- Root autologin (universal Debian 11–13+) ---
echo -e "${YELLOW}🔓 Configuring root autologin...${NC}"
pct exec "$CTID" -- bash -c '
mkdir -p /etc/systemd/system/container-getty@1.service.d
cat <<EOF > /etc/systemd/system/container-getty@1.service.d/override.conf
[Service]
ExecStart=
ExecStart=-/sbin/agetty --autologin root --noclear tty1 linux
EOF
systemctl daemon-reload
systemctl restart container-getty@1.service || true
'

# --- Start container ---
pct start "$CTID"
sleep 5

# --- Install Docker & Profilarr ---
echo -e "${CYAN}⚙️ Installing Docker and ${APP} inside CT $CTID...${NC}"

pct exec "$CTID" -- bash -c "
set -e
apt update -y && apt install -y curl gnupg ca-certificates lsb-release apt-transport-https locales sudo
echo 'en_US.UTF-8 UTF-8' > /etc/locale.gen && locale-gen en_US.UTF-8 && update-locale LANG=en_US.UTF-8

# Install Docker
curl -fsSL https://get.docker.com | sh
systemctl enable docker
systemctl start docker

# Install Docker Compose plugin
apt install -y docker-compose-plugin || apt install -y docker-compose

# Create Profilarr directory
mkdir -p /opt/profilarr
cd /opt/profilarr

# Write docker-compose.yml
cat <<EOF > docker-compose.yml
services:
  profilarr:
    image: santiagosayshey/profilarr:latest
    container_name: profilarr
    ports:
      - '${PORT}:6868'
    volumes:
      - /opt/profilarr/config:/config
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=America/Toronto
    restart: unless-stopped
EOF

# Deploy container
docker compose up -d
"

# --- Ensure startup ---
pct set "$CTID" -onboot 1
pct start "$CTID" || true

IP=$(pct exec "$CTID" -- hostname -I | awk '{print $1}')

echo ""
echo -e "${GREEN}✅ ${APP} successfully installed inside CT ${CTID}${NC}"
echo ""
echo "🌐 Access at: http://$IP:${PORT}"
echo "📦 Docker location: /opt/profilarr"
echo "🐳 Manage with:"
echo "   pct exec $CTID -- docker compose -f /opt/profilarr/docker-compose.yml ps"
echo ""
echo "🔄 Update later with:"
echo "   pct exec $CTID -- bash -c 'cd /opt/profilarr && docker compose pull && docker compose up -d'"
echo ""
echo -e "${CYAN}🎉 ${APP} is now running and will auto-start with Proxmox.${NC}"
