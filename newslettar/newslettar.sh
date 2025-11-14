#!/usr/bin/env bash

# Copyright (c) 2021-2025 community-scripts ORG
# Author: Your Name
# License: MIT
# https://github.com/agencefanfare/lerefuge

source /dev/stdin <<< "$FUNCTIONS_FILE_PATH"
color
verb_ip6
catch_errors
setting_up_container
network_check
update_os

msg_info "Installing Dependencies"
$STD apt-get install -y curl
$STD apt-get install -y sudo
$STD apt-get install -y mc
$STD apt-get install -y wget
msg_ok "Installed Dependencies"

msg_info "Installing Go"
ARCH=$(dpkg --print-architecture)
case $ARCH in
  amd64) GO_ARCH="amd64" ;;
  arm64) GO_ARCH="arm64" ;;
  armhf) GO_ARCH="armv6l" ;;
esac

cd /tmp
LATEST_GO=$(curl -s https://go.dev/VERSION?m=text | head -n1)
wget -q https://go.dev/dl/${LATEST_GO}.linux-${GO_ARCH}.tar.gz
tar -C /usr/local -xzf ${LATEST_GO}.linux-${GO_ARCH}.tar.gz
rm ${LATEST_GO}.linux-${GO_ARCH}.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile
export PATH=$PATH:/usr/local/go/bin
msg_ok "Installed Go $(go version | awk '{print $3}')"

msg_info "Setting up Newslettar"
INSTALL_DIR="/opt/newslettar"
mkdir -p $INSTALL_DIR
cd $INSTALL_DIR

REPO_URL="https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar"
wget -q -O main.go "$REPO_URL/main.go"
wget -q -O go.mod "$REPO_URL/go.mod"

go mod tidy
go build -o newslettar main.go
chmod +x newslettar
msg_ok "Built Newslettar"

msg_info "Creating Configuration"
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
msg_ok "Created Configuration"

msg_info "Creating Services"
cat > /etc/systemd/system/newslettar.service << 'SVCEOF'
[Unit]
Description=Newslettar Web UI and Newsletter Service
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

cat > /etc/systemd/system/newslettar-send.timer << 'TIMEREOF'
[Unit]
Description=Newslettar Weekly Newsletter Timer
Requires=newslettar-send.service

[Timer]
OnCalendar=Sun *-*-* 09:00:00
Persistent=true

[Install]
WantedBy=timers.target
TIMEREOF

touch /var/log/newslettar.log

systemctl daemon-reload
systemctl enable --now newslettar.service
systemctl enable --now newslettar-send.timer
msg_ok "Created Services"

msg_info "Creating Management Script"
cat > /usr/local/bin/newslettar-ctl << 'CTLEOF'
#!/bin/bash
case "$1" in
    start) systemctl start newslettar.service ;;
    stop) systemctl stop newslettar.service ;;
    restart) systemctl restart newslettar.service ;;
    status) 
        echo "=== Web UI Service ==="
        systemctl status newslettar.service --no-pager
        echo ""
        echo "=== Newsletter Timer ==="
        systemctl list-timers newslettar-send.timer --no-pager
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
    update)
        echo "Updating Newslettar..."
        cd /opt/newslettar
        cp .env .env.backup
        wget -q -O main.go https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/main.go
        go build -o newslettar main.go
        mv .env.backup .env
        systemctl restart newslettar.service
        echo "✓ Update complete!"
        ;;
    *)
        echo "Newslettar Control Script"
        echo ""
        echo "Usage: newslettar-ctl {command}"
        echo ""
        echo "Commands:"
        echo "  start    - Start the Web UI service"
        echo "  stop     - Stop the Web UI service"
        echo "  restart  - Restart the Web UI service"
        echo "  status   - Show service status and next newsletter time"
        echo "  logs     - View newsletter logs (live)"
        echo "  test     - Send a test newsletter now"
        echo "  edit     - Edit configuration"
        echo "  web      - Show Web UI URL"
        echo "  update   - Update to latest version from GitHub"
        exit 1
        ;;
esac
CTLEOF

chmod +x /usr/local/bin/newslettar-ctl
msg_ok "Created Management Script"

motd_ssh
customize

msg_info "Cleaning up"
$STD apt-get -y autoremove
$STD apt-get -y autoclean
msg_ok "Cleaned"

msg_info "Setting up Newslettar Complete"
IP=$(hostname -I | awk '{print $1}')
echo ""
echo "Newslettar is now installed and running!"
echo ""
echo "Web UI: http://${IP}:8080"
echo ""
echo "Next Steps:"
echo "  1. Open the Web UI in your browser"
echo "  2. Configure Sonarr, Radarr, and Email settings"
echo "  3. Test connections and send a test newsletter"
echo ""
echo "Command line: newslettar-ctl status|logs|web|update"
echo ""
msg_ok "Setup Complete"
