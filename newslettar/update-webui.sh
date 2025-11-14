#!/bin/bash

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Newslettar Update${NC}"
echo -e "${GREEN}================================${NC}"
echo ""

if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}Please run as root (use sudo)${NC}"
    exit 1
fi

INSTALL_DIR="/opt/newslettar"
cd $INSTALL_DIR

# Check if git repo
if [ ! -d ".git" ]; then
    echo -e "${RED}✗ Not a git repository!${NC}"
    echo -e "${YELLOW}Run ./setup-git.sh first to enable git updates${NC}"
    exit 1
fi

# Backup .env
echo -e "${YELLOW}[1/5] Backing up configuration...${NC}"
if [ -f ".env" ]; then
    cp .env .env.backup
    echo -e "${GREEN}✓ Configuration backed up${NC}"
fi

# Pull latest
echo -e "${YELLOW}[2/5] Pulling latest code...${NC}"
git stash --include-untracked 2>/dev/null || true
git pull origin main

# Restore .env
if [ -f ".env.backup" ]; then
    mv .env.backup .env
    echo -e "${GREEN}✓ Configuration restored${NC}"
fi

# Build newsletter service
echo -e "${YELLOW}[3/5] Building newsletter service...${NC}"
if [ -f "main.go" ]; then
    go build -o newslettar-service main.go
    chmod +x newslettar-service
    echo -e "${GREEN}✓ newslettar-service built${NC}"
else
    echo -e "${YELLOW}⚠ main.go not found, skipping${NC}"
fi

# Build Web UI
echo -e "${YELLOW}[4/5] Building Web UI...${NC}"
if [ -f "webui.go" ]; then
    go build -o newslettar-webui webui.go
    chmod +x newslettar-webui
    echo -e "${GREEN}✓ newslettar-webui built${NC}"
else
    echo -e "${YELLOW}⚠ webui.go not found, skipping${NC}"
fi

# Restart services
echo -e "${YELLOW}[5/5] Restarting services...${NC}"

if systemctl is-active --quiet newslettar-webui 2>/dev/null; then
    systemctl restart newslettar-webui
    echo -e "${GREEN}✓ Web UI restarted${NC}"
else
    # First time setup for Web UI
    if [ -f "newslettar-webui" ]; then
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
        systemctl start newslettar-webui
        echo -e "${GREEN}✓ Web UI service created and started${NC}"
    fi
fi

echo ""
echo -e "${GREEN}════════════════════════════════════════${NC}"
echo -e "${GREEN}Update Complete!${NC}"
echo -e "${GREEN}════════════════════════════════════════${NC}"
echo ""

IP=$(hostname -I | awk '{print $1}')

echo -e "${YELLOW}Services:${NC}"
echo "  Newsletter: $(systemctl is-active newslettar.timer 2>/dev/null || echo 'active')"
if systemctl is-active --quiet newslettar-webui 2>/dev/null; then
    echo "  Web UI: active"
    echo ""
    echo -e "${GREEN}Access Web UI:${NC}"
    echo "  http://${IP}:8080"
else
    echo "  Web UI: not installed"
fi

echo ""
echo -e "${YELLOW}Quick commands:${NC}"
echo "  newslettar-ctl test   - Test newsletter"
echo "  newslettar-ctl status - Check schedule"
echo ""
