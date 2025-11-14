# Newslettar

A lightweight Go service that automatically sends weekly email newsletters with your downloaded media and upcoming releases from Sonarr and Radarr.

## Features

- 📥 Lists all shows and movies downloaded in the past week
- 📅 Shows upcoming releases for the next week
- 📧 Beautiful HTML email via Mailgun SMTP
- ⏰ Automatically runs every Sunday at 9 AM
- 🐳 Docker support
- 🔧 Simple configuration via environment variables

## Quick Start

### Prerequisites

- Go 1.23+ (for local development)
- Docker & Docker Compose (for containerized deployment)
- Sonarr and Radarr with API access
- Mailgun account (or any SMTP service)

### Installation

1. **Clone or create the project structure:**

```bash
mkdir newslettar && cd newslettar
# Copy all the files (main.go, go.mod, Dockerfile, docker-compose.yml)
```

2. **Initialize Go modules:**

```bash
go mod init newslettar
go mod tidy
```

3. **Configure your environment:**

Edit `docker-compose.yml` and update:
- Sonarr/Radarr URLs and API keys
- Mailgun SMTP credentials
- Email addresses
- Timezone

### Docker Deployment

```bash
# Build and start the service
docker-compose up -d

# View logs
docker-compose logs -f

# Stop the service
docker-compose down
```

### LXC on Proxmox Deployment

1. **Create an LXC container** (Ubuntu 22.04 or Debian 12 recommended)

2. **Install Go in the LXC:**

```bash
wget https://go.dev/dl/go1.23.5.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.23.5.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
```

3. **Copy project files to LXC:**

```bash
# From your host
scp -r ./* user@lxc-ip:/opt/newslettar/
```

4. **Build the binary:**

```bash
cd /opt/newslettar
go build -o newslettar
```

5. **Create environment file:**

```bash
sudo nano /opt/newslettar/.env
```

Add your configuration:
```env
SONARR_URL=http://192.168.1.x:8989
SONARR_API_KEY=your_key
RADARR_URL=http://192.168.1.x:7878
RADARR_API_KEY=your_key
MAILGUN_SMTP=smtp.mailgun.org
MAILGUN_PORT=587
MAILGUN_USER=postmaster@your-domain.mailgun.org
MAILGUN_PASS=your_password
FROM_EMAIL=newsletter@yourdomain.com
TO_EMAILS=your@email.com
```

6. **Set up cron job:**

```bash
sudo crontab -e
```

Add this line (runs every Sunday at 9 AM):
```
0 9 * * 0 cd /opt/newslettar && /usr/local/go/bin/go run . >> /var/log/newslettar.log 2>&1
```

Or use the compiled binary:
```
0 9 * * 0 /opt/newslettar/newslettar >> /var/log/newslettar.log 2>&1
```

### Manual Run (Testing)

```bash
# Set environment variables
export SONARR_URL=http://localhost:8989
export SONARR_API_KEY=your_key
# ... set other variables

# Run
go run main.go
```

## Configuration

### Environment Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `SONARR_URL` | Sonarr base URL | `http://sonarr:8989` |
| `SONARR_API_KEY` | Sonarr API key | Found in Sonarr Settings > General |
| `RADARR_URL` | Radarr base URL | `http://radarr:7878` |
| `RADARR_API_KEY` | Radarr API key | Found in Radarr Settings > General |
| `MAILGUN_SMTP` | Mailgun SMTP server | `smtp.mailgun.org` |
| `MAILGUN_PORT` | SMTP port | `587` |
| `MAILGUN_USER` | Mailgun username | `postmaster@yourdomain.mailgun.org` |
| `MAILGUN_PASS` | Mailgun password | Your Mailgun SMTP password |
| `FROM_EMAIL` | Sender email address | `newsletter@yourdomain.com` |
| `TO_EMAILS` | Recipient email | `user@example.com` |
| `TZ` | Timezone | `America/New_York` |

### Getting API Keys

**Sonarr:**
1. Open Sonarr web interface
2. Settings > General > Security
3. Copy the API Key

**Radarr:**
1. Open Radarr web interface
2. Settings > General > Security
3. Copy the API Key

**Mailgun:**
1. Sign up at mailgun.com
2. Verify your domain
3. Get SMTP credentials from Sending > Domain Settings > SMTP credentials

### Changing the Schedule

Edit the cron expression in `Dockerfile` or your crontab:

```
# Format: minute hour day month weekday
0 9 * * 0     # Sunday at 9 AM
0 20 * * 5    # Friday at 8 PM
0 9 * * 1-5   # Monday-Friday at 9 AM
```

## Customization

### Email Template

The HTML template is in `main.go`. Look for the `generateHTML` function to customize:
- Colors and styling
- Layout
- Additional information
- Footer content

### Multiple Recipients

Modify the code to support multiple recipients:

```go
// In Config struct
ToEmails: strings.Split(getEnv("TO_EMAILS", ""), ",")

// In sendEmail function
for _, to := range cfg.ToEmails {
    // Send to each recipient
}
```

## Troubleshooting

### Test the service manually:

```bash
docker exec -it newslettar /app/newslettar
```

### Check logs:

```bash
# Docker
docker logs newslettar

# LXC
tail -f /var/log/newslettar.log
```

### Common Issues:

1. **API Connection Failed**: Check URLs and firewall rules
2. **Authentication Failed**: Verify API keys are correct
3. **Email Not Sending**: Check Mailgun credentials and domain verification
4. **Empty Newsletter**: Verify date ranges and that content exists

## Architecture

- **Single binary**: No dependencies, easy to deploy
- **Stateless**: Reads from APIs, sends email, exits
- **Lightweight**: ~10-15MB binary, minimal CPU usage
- **Cron-based**: Scheduled execution, not a long-running service

## Future Enhancements

- [ ] Add Emby watch statistics
- [ ] Support multiple email recipients
- [ ] Include poster images
- [ ] Add weekly statistics
- [ ] Web interface for configuration
- [ ] Support for other media servers (Plex, Jellyfin)

## License

MIT License - Feel free to modify and distribute!