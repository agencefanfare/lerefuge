#!/bin/bash

# Newslettar Installer for LXC Container
# Run this INSIDE your Debian LXC container
# curl -sSL https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/install.sh | bash

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║      Newslettar Installer v1.0         ║${NC}"
echo -e "${GREEN}║      For Debian LXC Container          ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
echo ""

# Check if running as root
if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}Please run as root (use sudo or run as root)${NC}"
    exit 1
fi

# Check if Debian-based
if [ ! -f /etc/debian_version ]; then
    echo -e "${RED}This script is designed for Debian-based systems${NC}"
    exit 1
fi

DEBIAN_VERSION=$(cat /etc/debian_version | cut -d'.' -f1)
echo -e "${BLUE}Detected Debian version: ${DEBIAN_VERSION}${NC}"
echo ""

INSTALL_DIR="/opt/newslettar"
REPO_URL="https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar"

echo -e "${YELLOW}[1/7] Updating system packages...${NC}"
apt-get update -qq
apt-get install -y wget curl ca-certificates >/dev/null 2>&1
echo -e "${GREEN}✓ System updated${NC}"

echo -e "${YELLOW}[2/7] Installing Go...${NC}"
if ! command -v go &> /dev/null; then
    ARCH=$(dpkg --print-architecture)
    case $ARCH in
        amd64) GO_ARCH="amd64" ;;
        arm64) GO_ARCH="arm64" ;;
        armhf) GO_ARCH="armv6l" ;;
        *) echo -e "${RED}Unsupported architecture: $ARCH${NC}"; exit 1 ;;
    esac
    
    cd /tmp
    GO_VERSION="1.23.5"
    echo -e "${BLUE}  Downloading Go ${GO_VERSION}...${NC}"
    wget -q https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz
    tar -C /usr/local -xzf go${GO_VERSION}.linux-${GO_ARCH}.tar.gz
    rm go${GO_VERSION}.linux-${GO_ARCH}.tar.gz
    
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
    export PATH=$PATH:/usr/local/go/bin
    
    echo -e "${GREEN}✓ Go $(go version | awk '{print $3}') installed${NC}"
else
    export PATH=$PATH:/usr/local/go/bin
    echo -e "${GREEN}✓ Go already installed: $(go version | awk '{print $3}')${NC}"
fi

echo -e "${YELLOW}[3/7] Creating installation directory...${NC}"
mkdir -p $INSTALL_DIR
cd $INSTALL_DIR
echo -e "${GREEN}✓ Directory created: $INSTALL_DIR${NC}"

echo -e "${YELLOW}[4/7] Downloading Newslettar...${NC}"
echo -e "${BLUE}  Downloading main.go...${NC}"
wget -q -O main.go "$REPO_URL/main.go" || {
    echo -e "${RED}Failed to download main.go${NC}"
    echo -e "${YELLOW}URL: $REPO_URL/main.go${NC}"
    exit 1
}

echo -e "${BLUE}  Downloading go.mod...${NC}"
wget -q -O go.mod "$REPO_URL/go.mod" || {
    echo -e "${RED}Failed to download go.mod${NC}"
    exit 1
}
echo -e "${GREEN}✓ Application downloaded${NC}"

echo -e "${YELLOW}[5/7] Building Newslettar...${NC}"
go mod tidy
go build -o newslettar main.go
chmod +x newslettar
echo -e "${GREEN}✓ Built successfully${NC}"

echo -e "${YELLOW}[6/7] Creating configuration...${NC}"
cat > .env << 'EOF'
# Sonarr Configuration
SONARR_URL=http://localhost:8989
SONARR_API_KEY=

# Radarr Configuration
RADARR_URL=http://localhost:7878
RADARR_API_KEY=

# Email Configuration (Mailgun)
MAILGUN_SMTP=smtp.mailgun.org
MAILGUN_PORT=587
MAILGUN_USER=
MAILGUN_PASS=
FROM_EMAIL=newsletter@yourdomain.com
TO_EMAILS=user@example.com

# Web UI Port
WEBUI_PORT=8080
EOF
echo -e "${GREEN}✓ Configuration file created${NC}"

echo -e "${YELLOW}[7/7] Setting up systemd services...${NC}"

# Web UI Service
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

# Newsletter Sender Service (called by timer)
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

# Timer (runs every Sunday at 9 AM)
cat > /etc/systemd/system/newslettar-send.timer << 'TIMEREOF'
[Unit]
Description=Newslettar Weekly Newsletter Timer
Requires=newslettar-send.service

[Timer]
OnCalendar=Sun *-*-* 09:00:00
AccuracySec=1s
Persistent=false

[Install]
WantedBy=timers.target
TIMEREOF

# Create log file
touch /var/log/newslettar.log

# Create management script
cat > /usr/local/bin/newslettar-ctl << 'CTLEOF'
#!/bin/bash

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

case "$1" in
    start)
        systemctl start newslettar.service
        echo -e "${GREEN}✓ Web UI started${NC}"
        ;;
    stop)
        systemctl stop newslettar.service
        echo -e "${YELLOW}Web UI stopped${NC}"
        ;;
    restart)
        systemctl restart newslettar.service
        echo -e "${GREEN}✓ Web UI restarted${NC}"
        ;;
    status)
        echo -e "${YELLOW}=== Web UI Service ===${NC}"
        systemctl status newslettar.service --no-pager
        echo ""
        echo -e "${YELLOW}=== Newsletter Timer ===${NC}"
        systemctl list-timers newslettar-send.timer --no-pager
        ;;
    logs)
        tail -f /var/log/newslettar.log
        ;;
    test)
        echo -e "${YELLOW}Sending test newsletter...${NC}"
        cd /opt/newslettar
        source .env
        ./newslettar
        ;;
    edit)
        ${EDITOR:-nano} /opt/newslettar/.env
        ;;
    web)
        IP=$(hostname -I | awk '{print $1}')
        echo -e "${GREEN}Web UI:${NC} http://${IP}:8080"
        ;;
    update)
        echo -e "${YELLOW}Updating Newslettar...${NC}"
        cd /opt/newslettar
        cp .env .env.backup
        wget -q -O main.go https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/main.go
        go build -o newslettar main.go
        mv .env.backup .env
        systemctl restart newslettar.service
        echo -e "${GREEN}✓ Updated successfully!${NC}"
        ;;
    *)
        echo "Newslettar Control Script"
        echo ""
        echo "Usage: newslettar-ctl {command}"
        echo ""
        echo "Commands:"
        echo "  start    - Start Web UI service"
        echo "  stop     - Stop Web UI service"
        echo "  restart  - Restart Web UI service"
        echo "  status   - Show status and next scheduled run"
        echo "  logs     - View newsletter logs (live)"
        echo "  test     - Send test newsletter now"
        echo "  edit     - Edit configuration (.env file)"
        echo "  web      - Show Web UI URL"
        echo "  update   - Update to latest version from GitHub"
        exit 1
        ;;
esac
CTLEOF

chmod +x /usr/local/bin/newslettar-ctl

# Enable and start services
systemctl daemon-reload
systemctl enable --now newslettar.service
systemctl enable --now newslettar-send.timer

echo -e "${GREEN}✓ Services configured and started${NC}"

# Get IP address
IP=$(hostname -I | awk '{print $1}')

echo ""
echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║     Installation Complete! 🎉          ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
echo ""
echo -e "${BLUE}┌─ Web UI Access ──────────────────────┐${NC}"
echo -e "${BLUE}│${NC} ${GREEN}http://${IP}:8080${NC}"
echo -e "${BLUE}└──────────────────────────────────────┘${NC}"
echo ""
echo -e "${YELLOW}Quick Start:${NC}"
echo "  1. Open http://${IP}:8080 in your browser"
echo "  2. Go to Configuration tab"
echo "  3. Enter your Sonarr/Radarr URLs and API keys"
echo "  4. Enter your email settings"
echo "  5. Click 'Test Connections' to verify"
echo "  6. Click 'Send Newsletter Now' to test"
echo ""
echo -e "${YELLOW}Command Line:${NC}"
echo "  newslettar-ctl web      - Show Web UI URL"
echo "  newslettar-ctl status   - Check service status"
echo "  newslettar-ctl test     - Send test newsletter"
echo "  newslettar-ctl logs     - View logs"
echo "  newslettar-ctl edit     - Edit config file"
echo "  newslettar-ctl update   - Update to latest version"
echo ""
echo -e "${YELLOW}Scheduled Newsletter:${NC}"
echo "  Automatically sends every Sunday at 9:00 AM"
echo ""
echo -e "${GREEN}Enjoy Newslettar! 📺${NC}"
echo ""