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
REPO_URL="https://github.com/agencefanfare/lerefuge.git"

cd $INSTALL_DIR

# Backup .env
if [ -f ".env" ]; then
    echo -e "${YELLOW}[1/5] Backing up configuration...${NC}"
    cp .env .env.backup
    echo -e "${GREEN}✓ Configuration backed up${NC}"
fi

# Update from GitHub
echo -e "${YELLOW}[2/5] Pulling latest code from GitHub...${NC}"

if [ -d ".git" ]; then
    # Already a git repo, just pull
    git pull origin main
else
    # Initialize git repo
    git init
    git remote add origin $REPO_URL || git remote set-url origin $REPO_URL
    git config core.sparseCheckout true
    echo "newslettar/*" > .git/info/sparse-checkout
    git pull origin main
    
    # Move files from subdirectory
    if [ -d "newslettar" ]; then
        # Don't overwrite .env if it exists
        if [ -f ".env.backup" ]; then
            mv newslettar/* . 2>/dev/null || true
            mv .env.backup .env
        else
            mv newslettar/* .
        fi
        rm -rf newslettar
    fi
fi

echo -e "${GREEN}✓ Code updated from GitHub${NC}"

# Restore .env
if [ -f ".env.backup" ]; then
    mv .env.backup .env
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
    echo -e "${RED}✗ webui.go not found in repository${NC}"
    exit 1
fi

# Setup Web UI service
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

echo ""
echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Update Complete!${NC}"
echo -e "${GREEN}================================${NC}"
echo ""

# Get IP address
IP=$(hostname -I | awk '{print $1}')

echo -e "${YELLOW}Newsletter service:${NC} Updated"
echo -e "${YELLOW}Web UI service:${NC} Running"
echo ""
echo -e "${GREEN}Access Web UI at:${NC}"
echo -e "  ${GREEN}http://${IP}:8080${NC}"
echo -e "  ${GREEN}http://localhost:8080${NC} (from this machine)"
echo ""
echo -e "${YELLOW}Useful commands:${NC}"
echo "  systemctl status newslettar-webui    - Check Web UI status"
echo "  systemctl restart newslettar-webui   - Restart Web UI"
echo "  newslettar-ctl test                  - Test newsletter"
echo "  newslettar-ctl status                - Check newsletter schedule"
echo ""
echo -e "${YELLOW}Configuration:${NC}"
echo "  Your .env file was preserved"
echo "  Edit settings via Web UI or: nano /opt/newslettar/.env"
echo ""
