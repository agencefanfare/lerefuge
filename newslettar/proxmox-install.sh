#!/usr/bin/env bash

# Newslettar LXC Installer for Proxmox
# Run on Proxmox host: bash <(curl -s https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/proxmox-install.sh)

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Check if running on Proxmox
if ! command -v pct &> /dev/null; then
    echo -e "${RED}Error: This script must be run on a Proxmox host${NC}"
    exit 1
fi

echo -e "${GREEN}╔════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║  Newslettar LXC Container Creator          ║${NC}"
echo -e "${GREEN}║  Automated Setup for Proxmox VE            ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════╝${NC}"
echo ""

# Get next available CT ID
NEXTID=$(pvesh get /cluster/nextid)
echo -e "${BLUE}Next available CT ID: ${NEXTID}${NC}"
read -p "Press Enter to use ${NEXTID} or enter a different ID: " CTID
CTID=${CTID:-$NEXTID}

# Container configuration
read -p "Hostname [newslettar]: " HOSTNAME
HOSTNAME=${HOSTNAME:-newslettar}

read -p "Root password [random will be generated]: " ROOTPW
if [ -z "$ROOTPW" ]; then
    ROOTPW=$(openssl rand -base64 12)
    echo -e "${YELLOW}Generated password: ${ROOTPW}${NC}"
fi

read -p "Storage [local-lxc]: " STORAGE
STORAGE=${STORAGE:-local-lxc}

read -p "Disk size in GB [4]: " DISK
DISK=${DISK:-4}

read -p "RAM in MB [1024]: " RAM
RAM=${RAM:-1024}

read -p "CPU cores [2]: " CORES
CORES=${CORES:-2}

read -p "Bridge [vmbr0]: " BRIDGE
BRIDGE=${BRIDGE:-vmbr0}

read -p "Use DHCP for IP? [Y/n]: " USE_DHCP
USE_DHCP=${USE_DHCP:-Y}

if [[ $USE_DHCP =~ ^[Nn]$ ]]; then
    read -p "Static IP (e.g., 192.168.1.100/24): " STATIC_IP
    read -p "Gateway: " GATEWAY
    NET_CONFIG="ip=${STATIC_IP},gw=${GATEWAY}"
else
    NET_CONFIG="ip=dhcp"
fi

echo ""
echo -e "${YELLOW}Creating LXC container with:${NC}"
echo "  CT ID: ${CTID}"
echo "  Hostname: ${HOSTNAME}"
echo "  Storage: ${STORAGE}"
echo "  Disk: ${DISK}GB"
echo "  RAM: ${RAM}MB"
echo "  CPU: ${CORES} cores"
echo "  Network: ${BRIDGE} (${NET_CONFIG})"
echo ""
read -p "Continue? [Y/n]: " CONFIRM
CONFIRM=${CONFIRM:-Y}

if [[ ! $CONFIRM =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 0
fi

echo ""
echo -e "${YELLOW}[1/6] Downloading Debian 12 template...${NC}"
TEMPLATE="debian-12-standard_12.7-1_amd64.tar.zst"
TEMPLATE_PATH="local:vztmpl/${TEMPLATE}"

if ! pveam list local | grep -q $TEMPLATE; then
    pveam download local $TEMPLATE
fi
echo -e "${GREEN}✓ Template ready${NC}"

echo -e "${YELLOW}[2/6] Creating container...${NC}"
pct create $CTID $TEMPLATE_PATH \
    --hostname $HOSTNAME \
    --password "$ROOTPW" \
    --storage $STORAGE \
    --rootfs $STORAGE:$DISK \
    --memory $RAM \
    --cores $CORES \
    --net0 name=eth0,bridge=$BRIDGE,$NET_CONFIG \
    --unprivileged 1 \
    --features nesting=1 \
    --ostype debian \
    --onboot 1

echo -e "${GREEN}✓ Container ${CTID} created${NC}"

echo -e "${YELLOW}[3/6] Starting container...${NC}"
pct start $CTID
sleep 5
echo -e "${GREEN}✓ Container started${NC}"

echo -e "${YELLOW}[4/6] Installing Go...${NC}"
pct exec $CTID -- bash -c "
    apt-get update -qq
    apt-get install -y wget curl ca-certificates sudo mc >/dev/null 2>&1
    
    cd /tmp
    wget -q https://go.dev/dl/go1.23.5.linux-amd64.tar.gz
    tar -C /usr/local -xzf go1.23.5.linux-amd64.tar.gz
    rm go1.23.5.linux-amd64.tar.gz
    echo 'export PATH=\$PATH:/usr/local/go/bin' >> /etc/profile
"
echo -e "${GREEN}✓ Go installed${NC}"

echo -e "${YELLOW}[5/6] Installing Newslettar...${NC}"
pct exec $CTID -- bash -c "
    export PATH=\$PATH:/usr/local/go/bin
    mkdir -p /opt/newslettar
    cd /opt/newslettar
    
    # Download application
    wget -q -O main.go https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/main.go
    wget -q -O go.mod https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/go.mod
    
    # Build
    go mod tidy
    go build -o newslettar main.go
    chmod +x newslettar
    
    # Create config
    cat > .env << 'EOF'
SONARR_URL=http://localhost:8989
SONARR_API_KEY=
RADARR_URL=http://localhost:7878
RADARR_API_KEY=
MAILGUN_SMTP=smtp.mailgun.org
MAILGUN_PORT=587
MAILGUN_USER=
MAILGUN_PASS=
FROM_EMAIL=newsletter@yourdomain.com
TO_EMAILS=user@example.com
WEBUI_PORT=8080
EOF
    
    # Create services
    cat > /etc/systemd/system/newslettar.service << 'SVCEOF'
[Unit]
Description=Newslettar Web UI
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/newslettar
EnvironmentFile=/opt/newslettar/.env
ExecStart=/opt/newslettar/newslettar -web
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
SVCEOF

    cat > /etc/systemd/system/newslettar-send.service << 'SENDEOF'
[Unit]
Description=Newslettar Newsletter Sender
After=network.target

[Service]
Type=oneshot
User=root
WorkingDirectory=/opt/newslettar
EnvironmentFile=/opt/newslettar/.env
ExecStart=/opt/newslettar/newslettar
StandardOutput=append:/var/log/newslettar.log
StandardError=append:/var/log/newslettar.log
SENDEOF

    cat > /etc/systemd/system/newslettar-send.timer << 'TIMEREOF'
[Unit]
Description=Newslettar Weekly Timer
Requires=newslettar-send.service

[Timer]
OnCalendar=Sun *-*-* 09:00:00
Persistent=true

[Install]
WantedBy=timers.target
TIMEREOF

    # Create management script
    cat > /usr/local/bin/newslettar-ctl << 'CTLEOF'
#!/bin/bash
case \"\$1\" in
    start) systemctl start newslettar.service ;;
    stop) systemctl stop newslettar.service ;;
    restart) systemctl restart newslettar.service ;;
    status) 
        systemctl status newslettar.service --no-pager
        echo \"\"
        systemctl list-timers newslettar-send.timer --no-pager
        ;;
    logs) tail -f /var/log/newslettar.log ;;
    test) cd /opt/newslettar && source .env && ./newslettar ;;
    edit) \${EDITOR:-nano} /opt/newslettar/.env ;;
    web) 
        IP=\$(hostname -I | awk '{print \$1}')
        echo \"Web UI: http://\${IP}:8080\"
        ;;
    update)
        cd /opt/newslettar
        cp .env .env.backup
        wget -q -O main.go https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/main.go
        go build -o newslettar main.go
        mv .env.backup .env
        systemctl restart newslettar.service
        echo \"✓ Updated!\"
        ;;
    *)
        echo \"Usage: newslettar-ctl {start|stop|restart|status|logs|test|edit|web|update}\"
        exit 1
        ;;
esac
CTLEOF
    
    chmod +x /usr/local/bin/newslettar-ctl
    touch /var/log/newslettar.log
    
    # Enable and start
    systemctl daemon-reload
    systemctl enable --now newslettar.service
    systemctl enable --now newslettar-send.timer
"
echo -e "${GREEN}✓ Newslettar installed${NC}"

echo -e "${YELLOW}[6/6] Getting container IP...${NC}"
sleep 3
CT_IP=$(pct exec $CTID -- hostname -I | awk '{print $1}')
echo -e "${GREEN}✓ Container IP: ${CT_IP}${NC}"

echo ""
echo -e "${GREEN}╔════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║          Installation Complete! 🎉         ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${BLUE}Container Information:${NC}"
echo "  CT ID: ${CTID}"
echo "  Hostname: ${HOSTNAME}"
echo "  IP Address: ${CT_IP}"
echo "  Root Password: ${ROOTPW}"
echo ""
echo -e "${BLUE}Web UI Access:${NC}"
echo -e "  ${GREEN}http://${CT_IP}:8080${NC}"
echo ""
echo -e "${BLUE}SSH Access:${NC}"
echo "  ssh root@${CT_IP}"
echo ""
echo -e "${BLUE}Next Steps:${NC}"
echo "  1. Open http://${CT_IP}:8080 in your browser"
echo "  2. Configure Sonarr, Radarr, and Email settings"
echo "  3. Test connections and send a newsletter"
echo ""
echo -e "${BLUE}Inside Container:${NC}"
echo "  newslettar-ctl status   - Check status"
echo "  newslettar-ctl web      - Show Web UI URL"
echo "  newslettar-ctl update   - Update to latest version"
echo ""
echo -e "${YELLOW}Save this information!${NC}"
echo ""
