# Newslettar Web UI

A simple web interface to manage your Newslettar configuration and operations.

## Features

✅ **Configuration Editor** - Edit all settings without SSH  
✅ **Connection Testing** - Test Sonarr/Radarr/Email connections  
✅ **Manual Trigger** - Send newsletter on demand  
✅ **Schedule Viewer** - See when next newsletter runs  
✅ **Log Viewer** - View recent logs in browser  

## Installation

### 1. Create the Web UI file

```bash
cd /opt/newslettar
nano webui.go
# Paste the webui.go code from the artifact
```

### 2. Create install script

```bash
nano install-webui.sh
# Paste the install-webui.sh code
chmod +x install-webui.sh
```

### 3. Install

```bash
sudo ./install-webui.sh
```

### 4. Access

Open your browser to:
```
http://YOUR_LXC_IP:8080
```

For example: `http://192.168.1.100:8080`

## Usage

### Configuration Tab
- Edit all Sonarr/Radarr URLs and API keys
- Configure email settings
- Save changes instantly

### Actions Tab
- **Test Connections** - Verify Sonarr/Radarr/Email are working
- **Send Newsletter Now** - Trigger manual newsletter send
- **Check Schedule** - View next scheduled run time

### Logs Tab
- View last 100 lines of logs
- Refresh to see latest
- Monitor newsletter execution

## Security Notes

⚠️ **Important:** The Web UI runs on port 8080 without authentication by default.

### Secure Access Options:

1. **Firewall** (Recommended for LAN):
```bash
# Only allow from your local network
ufw allow from 192.168.1.0/24 to any port 8080
```

2. **SSH Tunnel** (For remote access):
```bash
# From your local machine
ssh -L 8080:localhost:8080 user@your-lxc-ip

# Then access: http://localhost:8080
```

3. **Reverse Proxy** (Advanced):
Use nginx/Caddy with basic auth

## Troubleshooting

### Web UI won't start

```bash
# Check status
systemctl status newslettar-webui

# View logs
journalctl -u newslettar-webui -n 50

# Restart
systemctl restart newslettar-webui
```

### Can't access from browser

```bash
# Check if port is listening
netstat -tlnp | grep 8080

# Check firewall
ufw status

# Test locally
curl http://localhost:8080
```

### Configuration won't save

```bash
# Check file permissions
ls -la /opt/newslettar/.env

# Should be writable
chmod 644 /opt/newslettar/.env
```

## Customization

### Change Port

Edit the service file:
```bash
sudo nano /etc/systemd/system/newslettar-webui.service

# Change WEBUI_PORT value
Environment="WEBUI_PORT=3000"

# Restart
sudo systemctl daemon-reload
sudo systemctl restart newslettar-webui
```

### Change Theme Colors

Edit `webui.go`, find the `<style>` section and modify:
- `.header` background gradient
- Button colors (`.btn-primary`, etc.)
- Accent colors throughout

Rebuild after changes:
```bash
cd /opt/newslettar
go build -o newslettar-webui webui.go
sudo systemctl restart newslettar-webui
```

## Uninstall

```bash
# Stop and disable service
sudo systemctl stop newslettar-webui
sudo systemctl disable newslettar-webui

# Remove service file
sudo rm /etc/systemd/system/newslettar-webui.service

# Remove binary
sudo rm /opt/newslettar/newslettar-webui

# Reload systemd
sudo systemctl daemon-reload
```

## Future Enhancements

- [ ] Authentication (username/password)
- [ ] Newsletter preview before sending
- [ ] Custom schedule editor
- [ ] Statistics dashboard
- [ ] Dark mode toggle
