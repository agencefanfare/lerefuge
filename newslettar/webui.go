package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type WebConfig struct {
	SonarrURL    string `json:"sonarr_url"`
	SonarrAPIKey string `json:"sonarr_api_key"`
	RadarrURL    string `json:"radarr_url"`
	RadarrAPIKey string `json:"radarr_api_key"`
	MailgunSMTP  string `json:"mailgun_smtp"`
	MailgunPort  string `json:"mailgun_port"`
	MailgunUser  string `json:"mailgun_user"`
	MailgunPass  string `json:"mailgun_pass"`
	FromEmail    string `json:"from_email"`
	ToEmails     string `json:"to_emails"`
}

func loadWebConfig() WebConfig {
	return WebConfig{
		SonarrURL:    getEnv("SONARR_URL", "http://localhost:8989"),
		SonarrAPIKey: getEnv("SONARR_API_KEY", ""),
		RadarrURL:    getEnv("RADARR_URL", "http://localhost:7878"),
		RadarrAPIKey: getEnv("RADARR_API_KEY", ""),
		MailgunSMTP:  getEnv("MAILGUN_SMTP", "smtp.mailgun.org"),
		MailgunPort:  getEnv("MAILGUN_PORT", "587"),
		MailgunUser:  getEnv("MAILGUN_USER", ""),
		MailgunPass:  getEnv("MAILGUN_PASS", ""),
		FromEmail:    getEnv("FROM_EMAIL", "newsletter@example.com"),
		ToEmails:     getEnv("TO_EMAILS", "user@example.com"),
	}
}

func saveWebConfig(cfg WebConfig) error {
	envContent := fmt.Sprintf(`# Sonarr Configuration
SONARR_URL=%s
SONARR_API_KEY=%s

# Radarr Configuration
RADARR_URL=%s
RADARR_API_KEY=%s

# Mailgun Configuration
MAILGUN_SMTP=%s
MAILGUN_PORT=%s
MAILGUN_USER=%s
MAILGUN_PASS=%s

# Email Configuration
FROM_EMAIL=%s
TO_EMAILS=%s
`,
		cfg.SonarrURL, cfg.SonarrAPIKey,
		cfg.RadarrURL, cfg.RadarrAPIKey,
		cfg.MailgunSMTP, cfg.MailgunPort,
		cfg.MailgunUser, cfg.MailgunPass,
		cfg.FromEmail, cfg.ToEmails,
	)

	return os.WriteFile("/opt/newslettar/.env", []byte(envContent), 0644)
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Newslettar - Configuration</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
        }
        .container {
            max-width: 900px;
            margin: 0 auto;
            background: white;
            border-radius: 16px;
            box-shadow: 0 20px 60px rgba(0,0,0,0.3);
            overflow: hidden;
        }
        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 30px;
            text-align: center;
        }
        .header h1 {
            font-size: 2.5em;
            margin-bottom: 10px;
        }
        .header p {
            opacity: 0.9;
            font-size: 1.1em;
        }
        .nav {
            display: flex;
            background: #f8f9fa;
            border-bottom: 2px solid #e9ecef;
        }
        .nav-item {
            flex: 1;
            padding: 15px;
            text-align: center;
            cursor: pointer;
            border: none;
            background: none;
            font-size: 1em;
            font-weight: 500;
            color: #6c757d;
            transition: all 0.3s;
        }
        .nav-item:hover {
            background: #e9ecef;
            color: #495057;
        }
        .nav-item.active {
            background: white;
            color: #667eea;
            border-bottom: 3px solid #667eea;
        }
        .content {
            padding: 30px;
        }
        .section {
            display: none;
        }
        .section.active {
            display: block;
        }
        .form-group {
            margin-bottom: 25px;
        }
        .form-group label {
            display: block;
            margin-bottom: 8px;
            font-weight: 600;
            color: #2c3e50;
        }
        .form-group input {
            width: 100%;
            padding: 12px 15px;
            border: 2px solid #e9ecef;
            border-radius: 8px;
            font-size: 1em;
            transition: border-color 0.3s;
        }
        .form-group input:focus {
            outline: none;
            border-color: #667eea;
        }
        .form-section {
            background: #f8f9fa;
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 25px;
        }
        .form-section h3 {
            color: #495057;
            margin-bottom: 15px;
            padding-bottom: 10px;
            border-bottom: 2px solid #dee2e6;
        }
        .btn {
            padding: 12px 30px;
            border: none;
            border-radius: 8px;
            font-size: 1em;
            font-weight: 600;
            cursor: pointer;
            transition: all 0.3s;
            margin-right: 10px;
        }
        .btn-primary {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
        }
        .btn-primary:hover {
            transform: translateY(-2px);
            box-shadow: 0 5px 15px rgba(102, 126, 234, 0.4);
        }
        .btn-success {
            background: #28a745;
            color: white;
        }
        .btn-success:hover {
            background: #218838;
        }
        .btn-danger {
            background: #dc3545;
            color: white;
        }
        .btn-danger:hover {
            background: #c82333;
        }
        .btn-secondary {
            background: #6c757d;
            color: white;
        }
        .btn-secondary:hover {
            background: #5a6268;
        }
        .status-box {
            padding: 15px;
            border-radius: 8px;
            margin-bottom: 15px;
            display: none;
        }
        .status-box.success {
            background: #d4edda;
            color: #155724;
            border: 1px solid #c3e6cb;
            display: block;
        }
        .status-box.error {
            background: #f8d7da;
            color: #721c24;
            border: 1px solid #f5c6cb;
            display: block;
        }
        .status-box.info {
            background: #d1ecf1;
            color: #0c5460;
            border: 1px solid #bee5eb;
            display: block;
        }
        .test-results {
            margin-top: 20px;
        }
        .test-item {
            padding: 12px;
            margin: 8px 0;
            border-radius: 6px;
            background: #f8f9fa;
            border-left: 4px solid #6c757d;
        }
        .test-item.success {
            border-left-color: #28a745;
            background: #d4edda;
        }
        .test-item.error {
            border-left-color: #dc3545;
            background: #f8d7da;
        }
        .logs {
            background: #1e1e1e;
            color: #d4d4d4;
            padding: 20px;
            border-radius: 8px;
            font-family: 'Courier New', monospace;
            font-size: 0.9em;
            max-height: 500px;
            overflow-y: auto;
            white-space: pre-wrap;
        }
        .action-buttons {
            display: flex;
            gap: 10px;
            flex-wrap: wrap;
        }
        .spinner {
            display: inline-block;
            width: 20px;
            height: 20px;
            border: 3px solid rgba(255,255,255,.3);
            border-radius: 50%;
            border-top-color: white;
            animation: spin 1s ease-in-out infinite;
        }
        @keyframes spin {
            to { transform: rotate(360deg); }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>📺 Newslettar</h1>
            <p>Configuration & Management</p>
        </div>

        <div class="nav">
            <button class="nav-item active" onclick="showSection('config')">⚙️ Configuration</button>
            <button class="nav-item" onclick="showSection('actions')">🚀 Actions</button>
            <button class="nav-item" onclick="showSection('logs')">📋 Logs</button>
        </div>

        <div class="content">
            <!-- Configuration Section -->
            <div id="config" class="section active">
                <div id="configStatus" class="status-box"></div>
                
                <form id="configForm" onsubmit="saveConfig(event)">
                    <div class="form-section">
                        <h3>🎬 Sonarr</h3>
                        <div class="form-group">
                            <label>Sonarr URL</label>
                            <input type="text" id="sonarr_url" placeholder="http://192.168.1.100:8989" required>
                        </div>
                        <div class="form-group">
                            <label>Sonarr API Key</label>
                            <input type="text" id="sonarr_api_key" placeholder="Your Sonarr API Key" required>
                        </div>
                    </div>

                    <div class="form-section">
                        <h3>🎥 Radarr</h3>
                        <div class="form-group">
                            <label>Radarr URL</label>
                            <input type="text" id="radarr_url" placeholder="http://192.168.1.100:7878" required>
                        </div>
                        <div class="form-group">
                            <label>Radarr API Key</label>
                            <input type="text" id="radarr_api_key" placeholder="Your Radarr API Key" required>
                        </div>
                    </div>

                    <div class="form-section">
                        <h3>📧 Email Settings</h3>
                        <div class="form-group">
                            <label>SMTP Server</label>
                            <input type="text" id="mailgun_smtp" placeholder="smtp.mailgun.org" required>
                        </div>
                        <div class="form-group">
                            <label>SMTP Port</label>
                            <input type="text" id="mailgun_port" placeholder="587" required>
                        </div>
                        <div class="form-group">
                            <label>SMTP Username</label>
                            <input type="text" id="mailgun_user" placeholder="postmaster@yourdomain.mailgun.org" required>
                        </div>
                        <div class="form-group">
                            <label>SMTP Password</label>
                            <input type="password" id="mailgun_pass" placeholder="Your SMTP Password" required>
                        </div>
                        <div class="form-group">
                            <label>From Email</label>
                            <input type="email" id="from_email" placeholder="newsletter@yourdomain.com" required>
                        </div>
                        <div class="form-group">
                            <label>To Email(s) (comma-separated)</label>
                            <input type="text" id="to_emails" placeholder="user1@example.com, user2@example.com" required>
                        </div>
                    </div>

                    <button type="submit" class="btn btn-primary">💾 Save Configuration</button>
                </form>
            </div>

            <!-- Actions Section -->
            <div id="actions" class="section">
                <div id="actionStatus" class="status-box"></div>
                
                <h2 style="margin-bottom: 20px;">Quick Actions</h2>
                
                <div class="action-buttons">
                    <button class="btn btn-success" onclick="testConnections()">🔍 Test Connections</button>
                    <button class="btn btn-primary" onclick="sendNewsletter()">📧 Send Newsletter Now</button>
                    <button class="btn btn-secondary" onclick="checkSchedule()">⏰ Check Schedule</button>
                </div>

                <div id="testResults" class="test-results"></div>
            </div>

            <!-- Logs Section -->
            <div id="logs" class="section">
                <h2 style="margin-bottom: 20px;">Recent Logs</h2>
                <button class="btn btn-secondary" onclick="loadLogs()" style="margin-bottom: 15px;">🔄 Refresh Logs</button>
                <div id="logsContent" class="logs">Loading logs...</div>
            </div>
        </div>
    </div>

    <script>
        // Load config on page load
        window.onload = function() {
            loadConfig();
        };

        function showSection(section) {
            document.querySelectorAll('.section').forEach(s => s.classList.remove('active'));
            document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
            document.getElementById(section).classList.add('active');
            event.target.classList.add('active');

            if (section === 'logs') {
                loadLogs();
            }
        }

        function loadConfig() {
            fetch('/api/config')
                .then(r => r.json())
                .then(data => {
                    document.getElementById('sonarr_url').value = data.sonarr_url;
                    document.getElementById('sonarr_api_key').value = data.sonarr_api_key;
                    document.getElementById('radarr_url').value = data.radarr_url;
                    document.getElementById('radarr_api_key').value = data.radarr_api_key;
                    document.getElementById('mailgun_smtp').value = data.mailgun_smtp;
                    document.getElementById('mailgun_port').value = data.mailgun_port;
                    document.getElementById('mailgun_user').value = data.mailgun_user;
                    document.getElementById('mailgun_pass').value = data.mailgun_pass;
                    document.getElementById('from_email').value = data.from_email;
                    document.getElementById('to_emails').value = data.to_emails;
                })
                .catch(err => showStatus('configStatus', 'Error loading configuration', 'error'));
        }

        function saveConfig(e) {
            e.preventDefault();
            
            const config = {
                sonarr_url: document.getElementById('sonarr_url').value,
                sonarr_api_key: document.getElementById('sonarr_api_key').value,
                radarr_url: document.getElementById('radarr_url').value,
                radarr_api_key: document.getElementById('radarr_api_key').value,
                mailgun_smtp: document.getElementById('mailgun_smtp').value,
                mailgun_port: document.getElementById('mailgun_port').value,
                mailgun_user: document.getElementById('mailgun_user').value,
                mailgun_pass: document.getElementById('mailgun_pass').value,
                from_email: document.getElementById('from_email').value,
                to_emails: document.getElementById('to_emails').value,
            };

            fetch('/api/config', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(config)
            })
            .then(r => r.json())
            .then(data => {
                showStatus('configStatus', '✓ Configuration saved successfully!', 'success');
            })
            .catch(err => {
                showStatus('configStatus', '✗ Error saving configuration', 'error');
            });
        }

        function testConnections() {
            showStatus('actionStatus', '🔍 Testing connections...', 'info');
            document.getElementById('testResults').innerHTML = '';

            fetch('/api/test')
                .then(r => r.json())
                .then(data => {
                    let html = '';
                    data.results.forEach(result => {
                        const status = result.success ? 'success' : 'error';
                        const icon = result.success ? '✓' : '✗';
                        html += \`<div class="test-item \${status}">\${icon} \${result.name}: \${result.message}</div>\`;
                    });
                    document.getElementById('testResults').innerHTML = html;
                    showStatus('actionStatus', data.overall_success ? '✓ All tests passed!' : '⚠ Some tests failed', data.overall_success ? 'success' : 'error');
                })
                .catch(err => {
                    showStatus('actionStatus', '✗ Error testing connections', 'error');
                });
        }

        function sendNewsletter() {
            if (!confirm('Send newsletter now?')) return;
            
            showStatus('actionStatus', '📧 Sending newsletter...', 'info');

            fetch('/api/send', { method: 'POST' })
                .then(r => r.json())
                .then(data => {
                    showStatus('actionStatus', data.success ? '✓ Newsletter sent successfully!' : '✗ ' + data.message, data.success ? 'success' : 'error');
                })
                .catch(err => {
                    showStatus('actionStatus', '✗ Error sending newsletter', 'error');
                });
        }

        function checkSchedule() {
            showStatus('actionStatus', '⏰ Checking schedule...', 'info');

            fetch('/api/schedule')
                .then(r => r.json())
                .then(data => {
                    showStatus('actionStatus', \`Next run: \${data.next_run}\`, 'success');
                })
                .catch(err => {
                    showStatus('actionStatus', '✗ Error checking schedule', 'error');
                });
        }

        function loadLogs() {
            fetch('/api/logs')
                .then(r => r.text())
                .then(data => {
                    document.getElementById('logsContent').textContent = data || 'No logs available';
                })
                .catch(err => {
                    document.getElementById('logsContent').textContent = 'Error loading logs';
                });
        }

        function showStatus(elementId, message, type) {
            const el = document.getElementById(elementId);
            el.textContent = message;
            el.className = 'status-box ' + type;
            setTimeout(() => {
                if (type !== 'error') {
                    el.className = 'status-box';
                }
            }, 5000);
        }
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, tmpl)
}

func configGetHandler(w http.ResponseWriter, r *http.Request) {
	cfg := loadWebConfig()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

func configPostHandler(w http.ResponseWriter, r *http.Request) {
	var cfg WebConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := saveWebConfig(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

func testHandler(w http.ResponseWriter, r *http.Request) {
	cfg := loadWebConfig()
	results := []map[string]interface{}{}
	overallSuccess := true

	// Test Sonarr
	sonarrReq, _ := http.NewRequest("GET", cfg.SonarrURL+"/api/v3/system/status", nil)
	sonarrReq.Header.Set("X-Api-Key", cfg.SonarrAPIKey)
	client := &http.Client{Timeout: 10 * time.Second}
	sonarrResp, err := client.Do(sonarrReq)
	if err != nil || sonarrResp.StatusCode != 200 {
		results = append(results, map[string]interface{}{
			"name":    "Sonarr",
			"success": false,
			"message": "Connection failed",
		})
		overallSuccess = false
	} else {
		results = append(results, map[string]interface{}{
			"name":    "Sonarr",
			"success": true,
			"message": "Connected successfully",
		})
		sonarrResp.Body.Close()
	}

	// Test Radarr
	radarrReq, _ := http.NewRequest("GET", cfg.RadarrURL+"/api/v3/system/status", nil)
	radarrReq.Header.Set("X-Api-Key", cfg.RadarrAPIKey)
	radarrResp, err := client.Do(radarrReq)
	if err != nil || radarrResp.StatusCode != 200 {
		results = append(results, map[string]interface{}{
			"name":    "Radarr",
			"success": false,
			"message": "Connection failed",
		})
		overallSuccess = false
	} else {
		results = append(results, map[string]interface{}{
			"name":    "Radarr",
			"success": true,
			"message": "Connected successfully",
		})
		radarrResp.Body.Close()
	}

	// Email test (just validate format for now)
	if cfg.MailgunUser == "" || cfg.MailgunPass == "" {
		results = append(results, map[string]interface{}{
			"name":    "Email",
			"success": false,
			"message": "SMTP credentials missing",
		})
		overallSuccess = false
	} else {
		results = append(results, map[string]interface{}{
			"name":    "Email",
			"success": true,
			"message": "SMTP credentials configured",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"overall_success": overallSuccess,
		"results":         results,
	})
}

func sendHandler(w http.ResponseWriter, r *http.Request) {
	cmd := exec.Command("/opt/newslettar/newslettar-service")
	output, err := cmd.CombinedOutput()

	success := err == nil
	message := "Newsletter sent successfully"
	if !success {
		message = string(output)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": success,
		"message": message,
	})
}

func scheduleHandler(w http.ResponseWriter, r *http.Request) {
	cmd := exec.Command("systemctl", "list-timers", "newslettar.timer", "--no-pager")
	output, _ := cmd.CombinedOutput()

	lines := strings.Split(string(output), "\n")
	nextRun := "Unknown"
	if len(lines) > 1 {
		fields := strings.Fields(lines[1])
		if len(fields) >= 2 {
			nextRun = fields[0] + " " + fields[1]
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"next_run": nextRun,
	})
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("/var/log/newslettar.log")
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "No logs available")
		return
	}

	// Get last 100 lines
	lines := strings.Split(string(data), "\n")
	start := len(lines) - 100
	if start < 0 {
		start = 0
	}
	
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, strings.Join(lines[start:], "\n"))
}

func main() {
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			configGetHandler(w, r)
		} else if r.Method == "POST" {
			configPostHandler(w, r)
		}
	})
	http.HandleFunc("/api/test", testHandler)
	http.HandleFunc("/api/send", sendHandler)
	http.HandleFunc("/api/schedule", scheduleHandler)
	http.HandleFunc("/api/logs", logsHandler)

	port := getEnv("WEBUI_PORT", "8080")
	log.Printf("🌐 Newslettar Web UI starting on http://0.0.0.0:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
