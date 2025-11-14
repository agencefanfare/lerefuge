#!/bin/bash

# Cleanup script to remove Newslettar from Proxmox host

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${YELLOW}Removing Newslettar from Proxmox host...${NC}"
echo ""

# Stop and disable services
echo -e "${YELLOW}[1/5] Stopping services...${NC}"
systemctl stop newslettar.service 2>/dev/null || true
systemctl stop newslettar-send.timer 2>/dev/null || true
systemctl stop newslettar-send.service 2>/dev/null || true
systemctl disable newslettar.service 2>/dev/null || true
systemctl disable newslettar-send.timer 2>/dev/null || true
systemctl disable newslettar-send.service 2>/dev/null || true
echo -e "${GREEN}✓ Services stopped${NC}"

# Remove service files
echo -e "${YELLOW}[2/5] Removing service files...${NC}"
rm -f /etc/systemd/system/newslettar.service
rm -f /etc/systemd/system/newslettar-send.service
rm -f /etc/systemd/system/newslettar-send.timer
systemctl daemon-reload
echo -e "${GREEN}✓ Service files removed${NC}"

# Remove application directory
echo -e "${YELLOW}[3/5] Removing application directory...${NC}"
rm -rf /opt/newslettar
echo -e "${GREEN}✓ Application directory removed${NC}"

# Remove management script
echo -e "${YELLOW}[4/5] Removing management script...${NC}"
rm -f /usr/local/bin/newslettar-ctl
echo -e "${GREEN}✓ Management script removed${NC}"

# Remove log file
echo -e "${YELLOW}[5/5] Removing log file...${NC}"
rm -f /var/log/newslettar.log
echo -e "${GREEN}✓ Log file removed${NC}"

echo ""
echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║    Cleanup Complete!                   ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
echo ""
echo -e "${YELLOW}Newslettar has been completely removed from your Proxmox host.${NC}"
echo ""
echo -e "${YELLOW}Note: Go installation was left in place in case you need it.${NC}"
echo -e "${YELLOW}To remove Go as well, run:${NC}"
echo "  rm -rf /usr/local/go"
echo "  # Then remove from /etc/profile: export PATH=\$PATH:/usr/local/go/bin"
echo ""
echo -e "${GREEN}Now use the correct script to install in an LXC container!${NC}"
echo ""
