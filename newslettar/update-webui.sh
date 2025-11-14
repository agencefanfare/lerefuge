#!/bin/bash

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Newslettar Update & Web UI Setup${NC}"
echo -e "${GREEN}================================${NC}"
echo ""

if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}Please run as root (use sudo)${NC}"
    exit 1
fi

INSTALL_DIR="/opt/newslettar"
cd $INSTALL_DIR

# Backup .env
if [ -f ".env" ]; then
    echo -e "${YELLOW}[1/5] Backing up configuration...${NC}"
    cp .env .env.backup
    echo -e "${GREEN}✓ Configuration backed up${NC}"
fi

# Pull latest from GitHub
echo -e "${YELLOW}[2/5] Pulling latest code...${NC}"
git pull
echo -e "${GREEN}✓ Code updated from GitHub${NC}"

# Restore .env
if [ -f ".env.backup" ]; then
    mv .env.backup .env
    echo -e "${GREEN}✓ Configuration restored${NC}"
fi

# Build main newsletter service
echo -e "${YELLOW}[3/5] Building newsletter service...${NC}"
go build -o newslettar-service main.go
chmod +x newslettar-service
echo -e "${GREEN}✓ Newsletter service built${NC}"

# Build Web UI
echo -e "${YELLOW}[4/5] Building Web UI...${NC}"
if [ -f "webui.go" ]; then
    go build -o newslettar-webui webui.go
    chmod +x newslettar-webui
    echo -e "${GREEN}✓ Web UI built${NC}"
else
    echo -e "${RED}✗ webui.go not found - skipping Web UI build${NC}"
    echo -e "${YELLOW}Make sure webui.go is pushed to GitHub${NC}"
fi

# Setup Web UI service (only if webui binary exists)
if [ -f "newslettar-webui" ]; then
    echo -e "${YELLOW}[5/5] Setting up Web UI service...${NC}"

    cat > /etc/systemd/system/newslettar-webui.service << 'EOF'
[Unit]
Description=Newslettar Web UI
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/newslettar
EnvironmentFile=/opt/newslettar/.env
Environment="WEBUI_PORT=8080"
ExecStart=/opt/newslettar/newslettar-webui
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable newslettar-webui
    systemctl restart newslettar-webui

    echo -e "${GREEN}✓ Web UI service started${NC}"
else
    echo -e "${YELLOW}[5/5] Skipping Web UI service setup${NC}"
fi

echo ""
echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Update Complete!${NC}"
echo -e "${GREEN}================================${NC}"
echo ""

# Get IP address
IP=$(hostname -I | awk '{print $1}')

echo -e "${YELLOW}Newsletter service:${NC} Updated"

if systemctl is-active --quiet newslettar-webui; then
    echo -e "${YELLOW}Web UI service:${NC} Running"
    echo ""
    echo -e "${GREEN}Access Web UI at:${NC}"
    echo -e "  ${GREEN}http://${IP}:8080${NC}"
    echo -e "  ${GREEN}http://localhost:8080${NC} (from this machine)"
else
    echo -e "${YELLOW}Web UI service:${NC} Not installed (webui.go missing from repo)"
fi

echo ""
echo -e "${YELLOW}Useful commands:${NC}"
echo "  systemctl status newslettar-webui    - Check Web UI status"
echo "  systemctl restart newslettar-webui   - Restart Web UI"
echo "  newslettar-ctl test                  - Test newsletter"
echo "  newslettar-ctl status                - Check newsletter schedule"
echo ""
