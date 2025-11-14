#!/bin/bash

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${GREEN}╔══════════════════════════════════════╗${NC}"
echo -e "${GREEN}║   Newslettar Installation Script    ║${NC}"
echo -e "${GREEN}║        One-Click Setup v1.0          ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════╝${NC}"
echo ""

if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}Please run as root (use sudo)${NC}"
    exit 1
fi

INSTALL_DIR="/opt/newslettar"
REPO_URL="https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar"

echo -e "${YELLOW}[1/8] Updating system...${NC}"
apt-get update -qq
apt-get install -y wget curl ca-certificates >/dev/null 2>&1
echo -e "${GREEN}✓ System updated${NC}"

echo -e "${YELLOW}[2/8] Installing Go...${NC}"
if ! command -v go &> /dev/null; then
    ARCH=$(uname -m)
    case $ARCH in
        x86_64) GO_ARCH="amd64" ;;
        aarch64|arm64) GO_ARCH="arm64" ;;
        armv7l) GO_ARCH="armv6l" ;;
        *) echo -e "${RED}Unsupported architecture: $ARCH${NC}"; exit 1 ;;
    esac
    
    cd /tmp
    wget -q https://go.dev/dl/go1.23.5.linux-${GO_ARCH}.tar.gz
    tar -C /usr/local -xzf go1.23.5.linux-${GO_ARCH}.tar.gz
    rm go1.23.5.linux-${GO_ARCH}.tar.gz
    
    echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
    export PATH=$PATH:/usr/local/go/bin
    
    echo -e "${GREEN}✓ Go $(go version | awk '{print $3}') installed${NC}"
else
    echo -e "${GREEN}✓ Go already installed: $(go version | awk '{print $3}')${NC}"
fi

echo -e "${YELLOW}[3/8] Creating installation directory...${NC}"
mkdir -p $INSTALL_DIR
cd $INSTALL_DIR
echo -e "${GREEN}✓ Directory created: $INSTALL_DIR${NC}"

echo -e "${YELLOW}[4/8] Downloading application...${NC}"
wget -q -O main.go "$REPO_URL/main.go"
wget -q -O go.mod "$REPO_URL/go.mod"
echo -e "${GREEN}✓ Application downloaded${NC}"

echo -e "${YELLOW}[5/8] Building application...${NC}"
go mod tidy
go build -o newslettar main.go
chmod +x newslettar
echo -e "${GREEN}✓ Application built successfully${NC}"

echo -e "${YELLOW}[6/8] Creating configuration file...${NC}"
cat > .env << 'EOF'
# Sonarr Configuration
SONARR_URL=http://localhost:8989
SONARR_API_KEY=your_sonarr_api_key_here

# Radarr Configuration
RADARR_URL=http://localhost:7878
RADARR_API_KEY=your_radarr_api_key_here

# Email Configuration (Mailgun)
MAILGUN_SMTP=smtp.mailgun.org
MAILGUN_PORT=587
MAILGUN_USER=postmaster@your-domain.mailgun.org
MAILGUN_PASS=your_mailgun_password_here
FROM_EMAIL=newsletter@yourdomain.com
TO_EMAILS=your-email@example.com

# Web UI Port
WEBUI_PORT=8080
EOF

echo -e "${GREEN}✓ Configuration file created${NC}"

echo -e "${YELLOW}[7/8] Setting up systemd services...${NC}"

# Newsletter service (for cron sending)
cat > /etc/systemd/system/newslettar.service << EOF
[Unit]
Description=Newslettar Newsletter Service
After=network.target

[Service]
Type=oneshot
User=root
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$INSTALL_DIR/.env
ExecStart=$INSTALL_DIR/newslettar
StandardOutput=append:/var/log/newslettar.log
StandardError=append:/var/log/newslettar.log

[Install]
WantedBy=multi-user.target
EOF

# Timer (runs every Sunday at 9 AM)
cat > /etc/systemd/system/newslettar.timer << EOF
[Unit]
Description=Newslettar Weekly Timer
Requires=newslettar.service

[Timer]
OnCalendar=Sun *-*-* 09:00:00
Persistent=true

[Install]
WantedBy=timers.target
EOF

# Web UI service
cat > /etc/systemd/system/newslettar.service << EOF
[Unit]
Description=Newslettar Web UI and Newsletter Service
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$INSTALL_DIR/.env
ExecStart=$INSTALL_DIR/newslettar -web
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Create log file
touch /var/log/newslettar.log

# Enable and start services
systemctl daemon-reload
systemctl enable newslettar.timer
systemctl enable newslettar.service
systemctl start newslettar.timer
systemctl start newslettar.service

echo -e "${GREEN}✓ Services configured and started${NC}"

echo -e "${YELLOW}[8/8] Creating management scripts...${NC}"

# Helper script
cat > /usr/local/bin/newslettar-ctl << 'CTLEOF'
#!/bin/bash
case "$1" in
    start) systemctl start newslettar.service ;;
    stop) systemctl stop newslettar.service ;;
    restart) systemctl restart newslettar.service ;;
    status) 
        systemctl status newslettar.service --no-pager
        echo ""
        systemctl list-timers newslettar.timer --no-pager
        ;;
    logs) tail -f /var/log/newslettar.log ;;
    test) 
        cd /opt/newslettar
        source .env
        ./newslettar
        ;;
    edit) ${EDITOR:-nano} /opt/newslettar/.env ;;
    web) 
        IP=$(hostname -I | awk '{print $1}')
        echo "Web UI: http://${IP}:8080"
        ;;
    *)
        echo "Usage: $0 {start|stop|restart|status|logs|test|edit|web}"
        exit 1
        ;;
esac
CTLEOF

chmod +x /usr/local/bin/newslettar-ctl

echo -e "${GREEN}✓ Management scripts created${NC}"

# Get IP address
IP=$(hostname -I | awk '{print $1}')

echo ""
echo -e "${GREEN}╔══════════════════════════════════════╗${NC}"
echo -e "${GREEN}║     Installation Complete! 🎉       ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════╝${NC}"
echo ""
echo -e "${BLUE}┌─ Web UI Access ──────────────────────┐${NC}"
echo -e "${BLUE}│${NC} ${GREEN}http://${IP}:8080${NC}"
echo -e "${BLUE}│${NC} ${GREEN}http://localhost:8080${NC} (from this machine)"
echo -e "${BLUE}└──────────────────────────────────────┘${NC}"
echo ""
echo -e "${YELLOW}Quick Start:${NC}"
echo "  1. Open the Web UI in your browser"
echo "  2. Configure your Sonarr/Radarr/Email settings"
echo "  3. Test connections"
echo "  4. Send a test newsletter!"
echo ""
echo -e "${YELLOW}Command Line Tools:${NC}"
echo "  newslettar-ctl status   - Check service status"
echo "  newslettar-ctl test     - Send test newsletter"
echo "  newslettar-ctl logs     - View logs"
echo "  newslettar-ctl web      - Show Web UI URL"
echo "  newslettar-ctl edit     - Edit config file"
echo ""
echo -e "${YELLOW}Schedule:${NC}"
echo "  Newsletters automatically send every Sunday at 9:00 AM"
echo ""
echo -e "${YELLOW}Update:${NC}"
echo "  Use the 'Update' tab in the Web UI to update to the latest version"
echo ""
echo -e "${GREEN}Enjoy Newslettar! 📺${NC}"
echo ""
