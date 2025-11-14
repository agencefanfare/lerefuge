#!/bin/bash

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Newslettar Git Setup${NC}"
echo -e "${GREEN}================================${NC}"
echo ""

if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}Please run as root (use sudo)${NC}"
    exit 1
fi

INSTALL_DIR="/opt/newslettar"
REPO_URL="git@github.com:agencefanfare/lerefuge.git"

cd $INSTALL_DIR

# Step 1: Backup important files
echo -e "${YELLOW}[1/6] Backing up configuration and binaries...${NC}"
if [ -f ".env" ]; then
    cp .env .env.backup
    echo -e "${GREEN}✓ .env backed up${NC}"
fi

if [ -f "newslettar-service" ]; then
    cp newslettar-service newslettar-service.backup
fi

if [ -f "newslettar-webui" ]; then
    cp newslettar-webui newslettar-webui.backup
fi

# Step 2: Setup SSH key for GitHub
echo -e "${YELLOW}[2/6] Setting up SSH key for GitHub...${NC}"

SSH_DIR="/root/.ssh"
mkdir -p $SSH_DIR
chmod 700 $SSH_DIR

if [ ! -f "$SSH_DIR/newslettar-deploy" ]; then
    echo -e "${BLUE}Generating SSH deploy key...${NC}"
    ssh-keygen -t ed25519 -f "$SSH_DIR/newslettar-deploy" -N "" -C "newslettar-lxc"
    
    echo ""
    echo -e "${GREEN}✓ SSH key generated${NC}"
    echo ""
    echo -e "${YELLOW}════════════════════════════════════════${NC}"
    echo -e "${YELLOW}ADD THIS PUBLIC KEY TO GITHUB:${NC}"
    echo -e "${YELLOW}════════════════════════════════════════${NC}"
    cat "$SSH_DIR/newslettar-deploy.pub"
    echo -e "${YELLOW}════════════════════════════════════════${NC}"
    echo ""
    echo "1. Go to: https://github.com/agencefanfare/lerefuge/settings/keys"
    echo "2. Click 'Add deploy key'"
    echo "3. Title: newslettar-lxc"
    echo "4. Key: (paste the key above)"
    echo "5. Check 'Allow write access' if you want to push changes"
    echo ""
    read -p "Press Enter after adding the key to GitHub..."
else
    echo -e "${GREEN}✓ SSH key already exists${NC}"
fi

# Configure SSH to use the deploy key
cat > $SSH_DIR/config << EOF
Host github.com
    HostName github.com
    User git
    IdentityFile $SSH_DIR/newslettar-deploy
    StrictHostKeyChecking no
EOF

chmod 600 $SSH_DIR/config

# Step 3: Initialize git repository
echo -e "${YELLOW}[3/6] Initializing git repository...${NC}"

if [ -d ".git" ]; then
    echo -e "${YELLOW}Git repository already exists, cleaning up...${NC}"
    rm -rf .git
fi

git init
git remote add origin $REPO_URL

# Configure sparse checkout to only pull newslettar folder
git config core.sparseCheckout true
mkdir -p .git/info
echo "newslettar/*" > .git/info/sparse-checkout

echo -e "${GREEN}✓ Git initialized${NC}"

# Step 4: Pull from repository
echo -e "${YELLOW}[4/6] Pulling code from GitHub...${NC}"
git pull origin main

# Move files from newslettar subdirectory to root
if [ -d "newslettar" ]; then
    # Move all files except .env
    for file in newslettar/*; do
        filename=$(basename "$file")
        if [ "$filename" != ".env" ]; then
            mv "$file" . 2>/dev/null || true
        fi
    done
    rm -rf newslettar
fi

echo -e "${GREEN}✓ Code pulled from GitHub${NC}"

# Step 5: Restore config
echo -e "${YELLOW}[5/6] Restoring configuration...${NC}"
if [ -f ".env.backup" ]; then
    mv .env.backup .env
    echo -e "${GREEN}✓ Configuration restored${NC}"
else
    echo -e "${YELLOW}⚠ No backup found, using repository .env (if exists)${NC}"
fi

# Add .env to gitignore to never commit secrets
cat > .gitignore << EOF
# Configuration (contains secrets)
.env
.env.*

# Binaries
newslettar-service
newslettar-webui
*.exe
*.dll
*.so
*.dylib

# Backups
*.backup

# Go
*.test
*.out
go.sum

# Logs
*.log

# IDE
.vscode/
.idea/
*.swp
*.swo
*~

# OS
.DS_Store
Thumbs.db
EOF

git add .gitignore
git config user.email "newslettar@localhost"
git config user.name "Newslettar LXC"

echo -e "${GREEN}✓ .gitignore configured${NC}"

# Step 6: Build everything
echo -e "${YELLOW}[6/6] Building applications...${NC}"

if [ -f "main.go" ]; then
    go build -o newslettar-service main.go
    chmod +x newslettar-service
    echo -e "${GREEN}✓ newslettar-service built${NC}"
fi

if [ -f "webui.go" ]; then
    go build -o newslettar-webui webui.go
    chmod +x newslettar-webui
    echo -e "${GREEN}✓ newslettar-webui built${NC}"
fi

echo ""
echo -e "${GREEN}════════════════════════════════════════${NC}"
echo -e "${GREEN}Git Setup Complete!${NC}"
echo -e "${GREEN}════════════════════════════════════════${NC}"
echo ""
echo -e "${YELLOW}Your installation is now git-enabled!${NC}"
echo ""
echo -e "${YELLOW}To update in the future:${NC}"
echo -e "  ${GREEN}cd /opt/newslettar${NC}"
echo -e "  ${GREEN}git pull${NC}"
echo -e "  ${GREEN}go build -o newslettar-service main.go${NC}"
echo -e "  ${GREEN}go build -o newslettar-webui webui.go${NC}"
echo -e "  ${GREEN}systemctl restart newslettar-webui${NC}"
echo ""
echo -e "${YELLOW}Or use the update helper:${NC}"
echo -e "  ${GREEN}./update.sh${NC}"
echo ""
echo -e "${YELLOW}Protected files (never overwritten):${NC}"
echo "  • .env (your configuration)"
echo "  • Binaries (newslettar-service, newslettar-webui)"
echo ""
