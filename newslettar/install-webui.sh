#!/bin/bash

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Newslettar Web UI Installer${NC}"
echo -e "${GREEN}================================${NC}"
echo ""

if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}Please run as root (use sudo)${NC}"
    exit 1
fi

INSTALL_DIR="/opt/newslettar"
cd $INSTALL_DIR

echo -e "${YELLOW}[1/3] Building Web UI...${NC}"
go build -o newslettar-webui webui.go
chmod +x newslettar-webui

echo -e "${YELLOW}[2/3] Creating systemd service...${NC}"
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

echo -e "${YELLOW}[3/3] Starting Web UI...${NC}"
systemctl daemon-reload
systemctl enable newslettar-webui
systemctl restart newslettar-webui

echo ""
echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Web UI Installed!${NC}"
echo -e "${GREEN}================================${NC}"
echo ""
echo -e "${YELLOW}Access the Web UI at:${NC}"
echo -e "  ${GREEN}http://$(hostname -I | awk '{print $1}'):8080${NC}"
echo ""
echo -e "${YELLOW}Or from this machine:${NC}"
echo -e "  ${GREEN}http://localhost:8080${NC}"
echo ""
echo -e "${YELLOW}Useful commands:${NC}"
echo "  systemctl status newslettar-webui   - Check status"
echo "  systemctl restart newslettar-webui  - Restart"
echo "  systemctl stop newslettar-webui     - Stop"
echo "  journalctl -u newslettar-webui -f   - View logs"
echo ""
