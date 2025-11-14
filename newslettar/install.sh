#!/bin/bash

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
INSTALL_DIR="/opt/newslettar"
SERVICE_NAME="newslettar"
GO_VERSION="1.23.5"

echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Newslettar Installer${NC}"
echo -e "${GREEN}================================${NC}"
echo ""

# Check if running as root
if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}Please run as root (use sudo)${NC}"
    exit 1
fi

# Detect architecture
ARCH=$(uname -m)
case $ARCH in
    x86_64)
        GO_ARCH="amd64"
        ;;
    aarch64|arm64)
        GO_ARCH="arm64"
        ;;
    armv7l)
        GO_ARCH="armv6l"
        ;;
    *)
        echo -e "${RED}Unsupported architecture: $ARCH${NC}"
        exit 1
        ;;
esac

echo -e "${YELLOW}Detected architecture: $ARCH (Go: $GO_ARCH)${NC}"

# Update system
echo -e "${YELLOW}[1/7] Updating system packages...${NC}"
apt-get update -qq
apt-get install -y wget curl git ca-certificates >/dev/null 2>&1

# Install Go if not present
if ! command -v go &> /dev/null; then
    echo -e "${YELLOW}[2/7] Installing Go ${GO_VERSION}...${NC}"
    cd /tmp
    wget -q https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz
    tar -C /usr/local -xzf go${GO_VERSION}.linux-${GO_ARCH}.tar.gz
    rm go${GO_VERSION}.linux-${GO_ARCH}.tar.gz
    
    # Add Go to PATH
    if ! grep -q "/usr/local/go/bin" /etc/profile; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
    fi
    export PATH=$PATH:/usr/local/go/bin
    
    echo -e "${GREEN}✓ Go installed: $(go version)${NC}"
else
    echo -e "${GREEN}✓ Go already installed: $(go version)${NC}"
fi

# Create installation directory
echo -e "${YELLOW}[3/7] Creating installation directory...${NC}"
mkdir -p $INSTALL_DIR
cd $INSTALL_DIR

# Clone or download source
echo -e "${YELLOW}[4/7] Downloading source code...${NC}"
read -p "Enter GitHub repository URL (or press Enter to skip if files are already here): " REPO_URL

if [ ! -z "$REPO_URL" ]; then
    # Clean directory if cloning
    rm -rf $INSTALL_DIR/*
    git clone $REPO_URL $INSTALL_DIR
    echo -e "${GREEN}✓ Source code cloned${NC}"
else
    # Check if files exist
    if [ ! -f "main.go" ] || [ ! -f "go.mod" ]; then
        echo -e "${RED}Error: main.go and go.mod not found in $INSTALL_DIR${NC}"
        echo -e "${YELLOW}Please either:${NC}"
        echo "  1. Copy your files to $INSTALL_DIR first, or"
        echo "  2. Run this script again with a GitHub repository URL"
        exit 1
    fi
    echo -e "${GREEN}✓ Using existing source files${NC}"
fi

# Build the application
echo -e "${YELLOW}[5/7] Building application...${NC}"
go mod tidy
go build -o newslettar-service main.go
chmod +x newslettar-service
echo -e "${GREEN}✓ Application built successfully${NC}"

# Create configuration file
echo -e "${YELLOW}[6/7] Setting up configuration...${NC}"

if [ ! -f ".env" ]; then
    cat > .env << 'EOF'
# Sonarr Configuration
SONARR_URL=http://localhost:8989
SONARR_API_KEY=your_sonarr_api_key_here

# Radarr Configuration
RADARR_URL=http://localhost:7878
RADARR_API_KEY=your_radarr_api_key_here

# Mailgun Configuration
MAILGUN_SMTP=smtp.mailgun.org
MAILGUN_PORT=587
MAILGUN_USER=postmaster@your-domain.mailgun.org
MAILGUN_PASS=your_mailgun_password

# Email Configuration
FROM_EMAIL=newsletter@yourdomain.com
TO_EMAILS=your-email@example.com

# Timezone (optional)
TZ=America/New_York
EOF
    echo -e "${YELLOW}⚠ Created default .env file${NC}"
    echo -e "${YELLOW}⚠ Please edit $INSTALL_DIR/.env with your actual credentials${NC}"
else
    echo -e "${GREEN}✓ Using existing .env file${NC}"
fi

# Create systemd service
echo -e "${YELLOW}[7/7] Setting up systemd service...${NC}"

cat > /etc/systemd/system/${SERVICE_NAME}.service << EOF
[Unit]
Description=Newslettar - Media Newsletter Service
After=network.target

[Service]
Type=oneshot
User=root
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$INSTALL_DIR/.env
ExecStart=$INSTALL_DIR/newslettar-service
StandardOutput=append:/var/log/${SERVICE_NAME}.log
StandardError=append:/var/log/${SERVICE_NAME}.log

[Install]
WantedBy=multi-user.target
EOF

# Create systemd timer (runs every Sunday at 9 AM)
cat > /etc/systemd/system/${SERVICE_NAME}.timer << EOF
[Unit]
Description=Newslettar Timer
Requires=${SERVICE_NAME}.service

[Timer]
OnCalendar=Sun *-*-* 09:00:00
Persistent=true

[Install]
WantedBy=timers.target
EOF

# Create log file
touch /var/log/${SERVICE_NAME}.log

# Enable and start timer
systemctl daemon-reload
systemctl enable ${SERVICE_NAME}.timer
systemctl start ${SERVICE_NAME}.timer

echo -e "${GREEN}✓ Systemd service and timer configured${NC}"

# Create helper script
cat > $INSTALL_DIR/newslettar-ctl << 'EOF'
#!/bin/bash

SERVICE_NAME="newslettar"

case "$1" in
    start)
        echo "Starting newsletter timer..."
        systemctl start ${SERVICE_NAME}.timer
        ;;
    stop)
        echo "Stopping newsletter timer..."
        systemctl stop ${SERVICE_NAME}.timer
        ;;
    restart)
        echo "Restarting newsletter timer..."
        systemctl restart ${SERVICE_NAME}.timer
        ;;
    status)
        echo "=== Timer Status ==="
        systemctl status ${SERVICE_NAME}.timer --no-pager
        echo ""
        echo "=== Service Status ==="
        systemctl status ${SERVICE_NAME}.service --no-pager
        echo ""
        echo "=== Next Run ==="
        systemctl list-timers ${SERVICE_NAME}.timer --no-pager
        ;;
    logs)
        tail -f /var/log/${SERVICE_NAME}.log
        ;;
    test)
        echo "Running newsletter manually..."
        cd /opt/newslettar
        set -a
        source .env
        set +a
        ./newslettar-service
        ;;
    edit)
        ${EDITOR:-nano} /opt/newslettar/.env
        ;;
    *)
        echo "Usage: $0 {start|stop|restart|status|logs|test|edit}"
        echo ""
        echo "Commands:"
        echo "  start   - Start the scheduled newsletter"
        echo "  stop    - Stop the scheduled newsletter"
        echo "  restart - Restart the timer"
        echo "  status  - Show service status and next run time"
        echo "  logs    - View newsletter logs"
        echo "  test    - Run newsletter immediately (for testing)"
        echo "  edit    - Edit configuration file"
        exit 1
        ;;
esac
EOF

chmod +x $INSTALL_DIR/newslettar-ctl
ln -sf $INSTALL_DIR/newslettar-ctl /usr/local/bin/newslettar-ctl

echo ""
echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Installation Complete!${NC}"
echo -e "${GREEN}================================${NC}"
echo ""
echo -e "${YELLOW}Next Steps:${NC}"
echo ""
echo "1. Edit your configuration:"
echo -e "   ${GREEN}newslettar-ctl edit${NC}"
echo ""
echo "2. Add your API keys and email settings to the .env file"
echo ""
echo "3. Test the newsletter:"
echo -e "   ${GREEN}newslettar-ctl test${NC}"
echo ""
echo "4. Check status and next run time:"
echo -e "   ${GREEN}newslettar-ctl status${NC}"
echo ""
echo -e "${YELLOW}Scheduled Time:${NC} Every Sunday at 9:00 AM"
echo ""
echo -e "${YELLOW}Useful Commands:${NC}"
echo "  newslettar-ctl status   - Check timer status"
echo "  newslettar-ctl logs     - View logs"
echo "  newslettar-ctl test     - Test immediately"
echo "  newslettar-ctl edit     - Edit configuration"
echo ""
echo -e "${YELLOW}Log file:${NC} /var/log/${SERVICE_NAME}.log"
echo ""
