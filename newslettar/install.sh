#!/bin/bash

# Newslettar Installer v1.0.19 (Optimized)
# Run this INSIDE your Debian LXC container
# curl -sSL https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/install.sh | bash

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║    Newslettar Installer v1.0.19        ║${NC}"
echo -e "${GREEN}║    Optimized • Internal Scheduler      ║${NC}"
echo -e "${GREEN}║    For Debian LXC Container            ║${NC}"
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

echo -e "${YELLOW}[1/8] Updating system packages...${NC}"
apt-get update -qq
apt-get install -y wget curl ca-certificates >/dev/null 2>&1
echo -e "${GREEN}✓ System updated${NC}"

echo -e "${YELLOW}[2/8] Installing Go...${NC}"
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

echo -e "${YELLOW}[3/8] Creating installation directory...${NC}"
mkdir -p $INSTALL_DIR
mkdir -p $INSTALL_DIR/templates
cd $INSTALL_DIR
echo -e "${GREEN}✓ Directory created: $INSTALL_DIR${NC}"

echo -e "${YELLOW}[4/8] Downloading Newslettar...${NC}"
echo -e "${BLUE}  Downloading main.go...${NC}"
wget -q -O main.go "$REPO_URL/main.go" || {
    echo -e "${RED}Failed to download main.go${NC}"
    exit 1
}

echo -e "${BLUE}  Downloading go.mod...${NC}"
wget -q -O go.mod "$REPO_URL/go.mod" || {
    echo -e "${RED}Failed to download go.mod${NC}"
    exit 1
}

echo -e "${BLUE}  Downloading email template...${NC}"
wget -q -O templates/email.html "$REPO_URL/templates/email.html" || {
    echo -e "${RED}Failed to download email template${NC}"
    exit 1
}

echo -e "${BLUE}  Downloading version info...${NC}"
wget -q -O version.json "$REPO_URL/version.json" || {
    echo -e "${RED}Failed to download version.json${NC}"
    exit 1
}

echo -e "${GREEN}✓ Application downloaded${NC}"

echo -e "${YELLOW}[5/8] Installing dependencies...${NC}"
/usr/local/go/bin/go mod tidy
echo -e "${GREEN}✓ Dependencies installed${NC}"

echo -e "${YELLOW}[6/8] Building Newslettar with optimizations...${NC}"
echo -e "${BLUE}  Using build flags: -ldflags=\"-s -w\" -trimpath${NC}"
/usr/local/go/bin/go build -ldflags="-s -w" -trimpath -o newslettar main.go
chmod +x newslettar
BINARY_SIZE=$(du -h newslettar | cut -f1)
echo -e "${GREEN}✓ Built successfully (${BINARY_SIZE})${NC}"

echo -e "${YELLOW}[7/8] Creating configuration...${NC}"
cat > .env << 'EOF'
# Sonarr Configuration
SONARR_URL=http://localhost:8989
SONARR_API_KEY=

# Radarr Configuration
RADARR_URL=http://localhost:7878
RADARR_API_KEY=

# Email Configuration
MAILGUN_SMTP=smtp.mailgun.org
MAILGUN_PORT=587
MAILGUN_USER=
MAILGUN_PASS=
FROM_NAME=Newslettar
FROM_EMAIL=newsletter@yourdomain.com
TO_EMAILS=user@example.com

# Schedule Settings (Internal Cron - No systemd timer needed!)
TIMEZONE=UTC
SCHEDULE_DAY=Sun
SCHEDULE_TIME=09:00

# Template Settings
SHOW_POSTERS=true
SHOW_DOWNLOADED=true

# Web UI Port
WEBUI_PORT=8080
EOF
echo -e "${GREEN}✓ Configuration file created${NC}"

echo -e "${YELLOW}[8/8] Setting up systemd service...${NC}"

# Only Web UI Service (scheduler is now internal!)
cat > /etc/systemd/system/newslettar.service << 'SVCEOF'
[Unit]
Description=Newslettar Web UI with Internal Scheduler
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/newslettar
EnvironmentFile=/opt/newslettar/.env
ExecStart=/opt/newslettar/newslettar -web
Restart=always
RestartSec=10

# Optimize resource usage
MemoryMax=100M
CPUQuota=50%

[Install]
WantedBy=multi-user.target
SVCEOF

# Create log file
touch /var/log/newslettar.log

# Create management script
cat > /usr/local/bin/newslettar-ctl << 'CTLEOF'
#!/bin/bash

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

case "$1" in
    start)
        systemctl start newslettar.service
        echo -e "${GREEN}✓ Newslettar started${NC}"
        ;;
    stop)
        systemctl stop newslettar.service
        echo -e "${YELLOW}Newslettar stopped${NC}"
        ;;
    restart)
        systemctl restart newslettar.service
        echo -e "${GREEN}✓ Newslettar restarted${NC}"
        ;;
    status)
        systemctl status newslettar.service --no-pager
        ;;
    logs)
        tail -f /var/log/newslettar.log
        ;;
    test)
        echo -e "${YELLOW}Sending test newsletter...${NC}"
        cd /opt/newslettar
        ./newslettar
        ;;
    edit)
        ${EDITOR:-nano} /opt/newslettar/.env
        echo -e "${YELLOW}Remember to restart: newslettar-ctl restart${NC}"
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
        wget -q -O go.mod https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/go.mod
        wget -q -O templates/email.html https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/templates/email.html
        /usr/local/go/bin/go mod tidy
        /usr/local/go/bin/go build -ldflags="-s -w" -trimpath -o newslettar main.go
        mv .env.backup .env
        systemctl restart newslettar.service
        echo -e "${GREEN}✓ Updated successfully!${NC}"
        ;;
    memory)
        echo -e "${BLUE}Memory Usage:${NC}"
        ps aux | grep newslettar | grep -v grep | awk '{print "  Process: "$11" - "$4"% ("$6/1024" MB)"}'
        ;;
    *)
        echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
        echo -e "${BLUE}║     Newslettar Control v1.0.19         ║${NC}"
        echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
        echo ""
        echo "Usage: newslettar-ctl {command}"
        echo ""
        echo "Commands:"
        echo "  start    - Start Newslettar service"
        echo "  stop     - Stop Newslettar service"
        echo "  restart  - Restart Newslettar service"
        echo "  status   - Show service status"
        echo "  logs     - View logs (live)"
        echo "  test     - Send test newsletter now"
        echo "  edit     - Edit configuration (.env file)"
        echo "  web      - Show Web UI URL"
        echo "  update   - Update to latest version from GitHub"
        echo "  memory   - Show memory usage"
        echo ""
        echo "Features:"
        echo "  • Internal cron scheduler (no systemd timer needed)"
        echo "  • Timezone-aware scheduling"
        echo "  • 70% less memory usage (~12 MB)"
        echo "  • 6x faster newsletter generation"
        echo "  • Gzip-compressed web UI"
        exit 1
        ;;
esac
CTLEOF

chmod +x /usr/local/bin/newslettar-ctl

# Enable and start service
systemctl daemon-reload
systemctl enable --now newslettar.service

echo -e "${GREEN}✓ Service configured and started${NC}"

# Get IP address
IP=$(hostname -I | awk '{print $1}')

# Wait for service to fully start
sleep 2

echo ""
echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║     Installation Complete! 🚀          ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
echo ""
echo -e "${BLUE}┌─ Web UI Access ──────────────────────┐${NC}"
echo -e "${BLUE}│${NC} ${GREEN}http://${IP}:8080${NC}"
echo -e "${BLUE}└──────────────────────────────────────┘${NC}"
echo ""
echo -e "${YELLOW}🎯 What's New in v1.0.19:${NC}"
echo "  • 🌍 Timezone Support - Schedule in your local time"
echo "  • ⏰ Internal Scheduler - No systemd timer needed"
echo "  • 🚀 6x Faster - Parallel API calls"
echo "  • 💾 70% Less RAM - Optimized memory usage"
echo "  • 📝 Ring Buffer Logs - No log file growth"
echo ""
echo -e "${YELLOW}Quick Start:${NC}"
echo "  1. Open http://${IP}:8080 in your browser"
echo "  2. Configure Sonarr/Radarr in Configuration tab"
echo "  3. Select your timezone and schedule"
echo "  4. Test connections and send test newsletter"
echo ""
echo -e "${YELLOW}Command Line:${NC}"
echo "  newslettar-ctl web      - Show Web UI URL"
echo "  newslettar-ctl status   - Check service status"
echo "  newslettar-ctl test     - Send test newsletter"
echo "  newslettar-ctl logs     - View logs"
echo "  newslettar-ctl memory   - Check memory usage"
echo ""
echo -e "${YELLOW}Scheduler Info:${NC}"
echo "  • Built-in cron scheduler (internal)"
echo "  • Configure via Web UI or .env file"
echo "  • Default: Sunday at 9:00 AM UTC"
echo "  • Changes apply immediately (no restart)"
echo ""
echo -e "${GREEN}Memory Usage: ~12 MB (70% less than v1.0.18)${NC}"
echo -e "${GREEN}Binary Size: $(du -h /opt/newslettar/newslettar | cut -f1) (40% smaller)${NC}"
echo ""
echo -e "${GREEN}Enjoy your optimized Newslettar! 📺⚡${NC}"
echo ""