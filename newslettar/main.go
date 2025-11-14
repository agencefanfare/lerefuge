package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"sort"
	"strings"
	"time"
)

type Config struct {
	SonarrURL    string
	SonarrAPIKey string
	RadarrURL    string
	RadarrAPIKey string
	MailgunSMTP  string
	MailgunPort  string
	MailgunUser  string
	MailgunPass  string
	FromEmail    string
	ToEmails     []string
}

type Episode struct {
	SeriesTitle string
	SeasonNum   int
	EpisodeNum  int
	Title       string
	AirDate     string
	Downloaded  bool
	PosterURL   string
}

type Movie struct {
	Title       string
	Year        int
	ReleaseDate string
	Downloaded  bool
	PosterURL   string
}

type NewsletterData struct {
	WeekStart        string
	WeekEnd          string
	DownloadedShows  []Episode
	DownloadedMovies []Movie
	UpcomingShows    []Episode
	UpcomingMovies   []Movie
}

func loadConfig() Config {
	toEmails := strings.Split(getEnv("TO_EMAILS", "user@example.com"), ",")
	for i := range toEmails {
		toEmails[i] = strings.TrimSpace(toEmails[i])
	}

	return Config{
		SonarrURL:    getEnv("SONARR_URL", "http://localhost:8989"),
		SonarrAPIKey: getEnv("SONARR_API_KEY", ""),
		RadarrURL:    getEnv("RADARR_URL", "http://localhost:7878"),
		RadarrAPIKey: getEnv("RADARR_API_KEY", ""),
		MailgunSMTP:  getEnv("MAILGUN_SMTP", "smtp.mailgun.org"),
		MailgunPort:  getEnv("MAILGUN_PORT", "587"),
		MailgunUser:  getEnv("MAILGUN_USER", ""),
		MailgunPass:  getEnv("MAILGUN_PASS", ""),
		FromEmail:    getEnv("FROM_EMAIL", "newsletter@example.com"),
		ToEmails:     toEmails,
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fetchSonarrHistory(cfg Config, since time.Time) ([]Episode, error) {
	url := fmt.Sprintf("%s/api/v3/history?pageSize=1000&sortKey=date&sortDirection=descending&includeEpisode=true&includeSeries=true", cfg.SonarrURL)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Api-Key", cfg.SonarrAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sonarr history request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sonarr history returned status %d: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	
	var result struct {
		Records []struct {
			SeriesID    int       `json:"seriesId"`
			EpisodeID   int       `json:"episodeId"`
			SourceTitle string    `json:"sourceTitle"`
			Date        time.Time `json:"date"`
			EventType   string    `json:"eventType"`
			Series      struct {
				Title  string `json:"title"`
				Images []struct {
					CoverType string `json:"coverType"`
					URL       string `json:"url"`
					RemoteURL string `json:"remoteUrl"`
				} `json:"images"`
			} `json:"series"`
			Episode struct {
				SeasonNumber  int    `json:"seasonNumber"`
				EpisodeNumber int    `json:"episodeNumber"`
				Title         string `json:"title"`
				AirDate       string `json:"airDate"`
			} `json:"episode"`
		} `json:"records"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse sonarr history: %w", err)
	}

	var episodes []Episode
	seen := make(map[string]bool)

	for _, record := range result.Records {
		if record.EventType == "downloadFolderImported" && record.Date.After(since) {
			key := fmt.Sprintf("%d-%d-%d", record.SeriesID, record.Episode.SeasonNumber, record.Episode.EpisodeNumber)
			if !seen[key] {
				posterURL := ""
				for _, img := range record.Series.Images {
					if img.CoverType == "poster" {
						if img.RemoteURL != "" {
							posterURL = img.RemoteURL
						} else if img.URL != "" {
							posterURL = cfg.SonarrURL + img.URL
						}
						break
					}
				}

				episodes = append(episodes, Episode{
					SeriesTitle: record.Series.Title,
					SeasonNum:   record.Episode.SeasonNumber,
					EpisodeNum:  record.Episode.EpisodeNumber,
					Title:       record.Episode.Title,
					AirDate:     record.Episode.AirDate,
					Downloaded:  true,
					PosterURL:   posterURL,
				})
				seen[key] = true
			}
		}
	}

	return episodes, nil
}

func fetchSonarrCalendar(cfg Config, start, end time.Time) ([]Episode, error) {
	url := fmt.Sprintf("%s/api/v3/calendar?start=%s&end=%s&includeSeries=true",
		cfg.SonarrURL,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"))

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Api-Key", cfg.SonarrAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sonarr calendar request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("sonarr calendar returned status %d: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	
	var result []struct {
		Series struct {
			Title  string `json:"title"`
			Images []struct {
				CoverType string `json:"coverType"`
				URL       string `json:"url"`
				RemoteURL string `json:"remoteUrl"`
			} `json:"images"`
		} `json:"series"`
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
		Title         string `json:"title"`
		AirDate       string `json:"airDate"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse sonarr calendar: %w", err)
	}

	var episodes []Episode
	for _, ep := range result {
		posterURL := ""
		for _, img := range ep.Series.Images {
			if img.CoverType == "poster" {
				if img.RemoteURL != "" {
					posterURL = img.RemoteURL
				} else if img.URL != "" {
					posterURL = cfg.SonarrURL + img.URL
				}
				break
			}
		}

		episodes = append(episodes, Episode{
			SeriesTitle: ep.Series.Title,
			SeasonNum:   ep.SeasonNumber,
			EpisodeNum:  ep.EpisodeNumber,
			Title:       ep.Title,
			AirDate:     ep.AirDate,
			Downloaded:  false,
			PosterURL:   posterURL,
		})
	}

	return episodes, nil
}

func fetchRadarrHistory(cfg Config, since time.Time) ([]Movie, error) {
	url := fmt.Sprintf("%s/api/v3/history?pageSize=1000&sortKey=date&sortDirection=descending&includeMovie=true", cfg.RadarrURL)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Api-Key", cfg.RadarrAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("radarr history request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("radarr history returned status %d: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	
	var result struct {
		Records []struct {
			MovieID   int       `json:"movieId"`
			Date      time.Time `json:"date"`
			EventType string    `json:"eventType"`
			Movie     struct {
				Title  string `json:"title"`
				Year   int    `json:"year"`
				Images []struct {
					CoverType string `json:"coverType"`
					URL       string `json:"url"`
					RemoteURL string `json:"remoteUrl"`
				} `json:"images"`
			} `json:"movie"`
		} `json:"records"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse radarr history: %w", err)
	}

	var movies []Movie
	seen := make(map[int]bool)

	for _, record := range result.Records {
		if record.EventType == "downloadFolderImported" && record.Date.After(since) && !seen[record.MovieID] {
			posterURL := ""
			for _, img := range record.Movie.Images {
				if img.CoverType == "poster" {
					if img.RemoteURL != "" {
						posterURL = img.RemoteURL
					} else if img.URL != "" {
						posterURL = cfg.RadarrURL + img.URL
					}
					break
				}
			}

			movies = append(movies, Movie{
				Title:      record.Movie.Title,
				Year:       record.Movie.Year,
				Downloaded: true,
				PosterURL:  posterURL,
			})
			seen[record.MovieID] = true
		}
	}

	return movies, nil
}

func fetchRadarrCalendar(cfg Config, start, end time.Time) ([]Movie, error) {
	url := fmt.Sprintf("%s/api/v3/calendar?start=%s&end=%s",
		cfg.RadarrURL,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"))

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Api-Key", cfg.RadarrAPIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("radarr calendar request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("radarr calendar returned status %d: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	
	var result []struct {
		Title           string `json:"title"`
		Year            int    `json:"year"`
		PhysicalRelease string `json:"physicalRelease"`
		DigitalRelease  string `json:"digitalRelease"`
		Images          []struct {
			CoverType string `json:"coverType"`
			URL       string `json:"url"`
			RemoteURL string `json:"remoteUrl"`
		} `json:"images"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse radarr calendar: %w", err)
	}

	var movies []Movie
	for _, m := range result {
		releaseDate := m.DigitalRelease
		if releaseDate == "" {
			releaseDate = m.PhysicalRelease
		}

		posterURL := ""
		for _, img := range m.Images {
			if img.CoverType == "poster" {
				if img.RemoteURL != "" {
					posterURL = img.RemoteURL
				} else if img.URL != "" {
					posterURL = cfg.RadarrURL + img.URL
				}
				break
			}
		}

		movies = append(movies, Movie{
			Title:       m.Title,
			Year:        m.Year,
			ReleaseDate: releaseDate,
			Downloaded:  false,
			PosterURL:   posterURL,
		})
	}

	return movies, nil
}

func generateHTML(data NewsletterData) (string, error) {
	tmpl := `
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style>
        body { 
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; 
            max-width: 800px; 
            margin: 0 auto; 
            padding: 20px; 
            background-color: #f5f5f5; 
        }
        .container { 
            background-color: white; 
            padding: 30px; 
            border-radius: 12px; 
            box-shadow: 0 2px 8px rgba(0,0,0,0.1);
        }
        h1 { 
            color: #2c3e50; 
            border-bottom: 3px solid #3498db; 
            padding-bottom: 10px; 
            margin-bottom: 10px;
        }
        h2 { 
            color: #34495e; 
            margin-top: 30px; 
            border-left: 4px solid #3498db; 
            padding-left: 15px; 
        }
        h3 { 
            color: #2c3e50; 
            margin-top: 20px; 
            margin-bottom: 15px; 
            font-size: 1.2em; 
        }
        .section { 
            margin-bottom: 30px; 
        }
        .item { 
            display: flex;
            padding: 15px; 
            margin: 12px 0; 
            background-color: #f8f9fa; 
            border-left: 3px solid #3498db; 
            border-radius: 8px;
            align-items: flex-start;
            transition: transform 0.2s;
        }
        .item:hover {
            transform: translateX(5px);
            background-color: #e9ecef;
        }
        .poster {
            width: 80px;
            height: 120px;
            object-fit: cover;
            border-radius: 6px;
            margin-right: 15px;
            flex-shrink: 0;
            box-shadow: 0 2px 4px rgba(0,0,0,0.2);
        }
        .poster-placeholder {
            width: 80px;
            height: 120px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            border-radius: 6px;
            margin-right: 15px;
            flex-shrink: 0;
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 36px;
            color: white;
        }
        .content {
            flex: 1;
        }
        .show-title { 
            font-weight: bold; 
            color: #2c3e50; 
            font-size: 1.1em;
            margin-bottom: 5px;
        }
        .episode-info { 
            color: #7f8c8d; 
            font-size: 0.95em; 
            margin-top: 3px;
        }
        .movie-title { 
            font-weight: bold; 
            color: #2c3e50; 
            font-size: 1.1em;
        }
        .movie-year { 
            color: #7f8c8d; 
            font-size: 0.95em;
        }
        .date-range { 
            color: #7f8c8d; 
            font-size: 0.95em; 
            margin-bottom: 20px; 
        }
        .empty { 
            color: #95a5a6; 
            font-style: italic; 
            padding: 15px; 
            text-align: center;
            background-color: #f8f9fa;
            border-radius: 6px;
        }
        .footer {
            margin-top: 40px;
            padding-top: 20px;
            border-top: 1px solid #e0e0e0;
            color: #7f8c8d;
            font-size: 0.85em;
            text-align: center;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>📺 Weekly Media Newsletter</h1>
        <div class="date-range">Week of {{ .WeekStart }} - {{ .WeekEnd }}</div>

        <div class="section">
            <h2>📥 Downloaded This Week</h2>
            
            <h3>TV Shows ({{ len .DownloadedShows }})</h3>
            {{ if .DownloadedShows }}
                {{ range .DownloadedShows }}
                <div class="item">
                    {{ if .PosterURL }}
                        <img src="{{ .PosterURL }}" alt="{{ .SeriesTitle }}" class="poster" />
                    {{ else }}
                        <div class="poster-placeholder">📺</div>
                    {{ end }}
                    <div class="content">
                        <div class="show-title">{{ .SeriesTitle }}</div>
                        <div class="episode-info">S{{ printf "%02d" .SeasonNum }}E{{ printf "%02d" .EpisodeNum }}{{ if .Title }} - {{ .Title }}{{ end }}</div>
                    </div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No shows downloaded this week</div>
            {{ end }}

            <h3>Movies ({{ len .DownloadedMovies }})</h3>
            {{ if .DownloadedMovies }}
                {{ range .DownloadedMovies }}
                <div class="item">
                    {{ if .PosterURL }}
                        <img src="{{ .PosterURL }}" alt="{{ .Title }}" class="poster" />
                    {{ else }}
                        <div class="poster-placeholder">🎬</div>
                    {{ end }}
                    <div class="content">
                        <div class="movie-title">{{ .Title }}</div>
                        <div class="movie-year">({{ .Year }})</div>
                    </div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No movies downloaded this week</div>
            {{ end }}
        </div>

        <div class="section">
            <h2>📅 Coming Next Week</h2>
            
            <h3>TV Shows ({{ len .UpcomingShows }})</h3>
            {{ if .UpcomingShows }}
                {{ range .UpcomingShows }}
                <div class="item">
                    {{ if .PosterURL }}
                        <img src="{{ .PosterURL }}" alt="{{ .SeriesTitle }}" class="poster" />
                    {{ else }}
                        <div class="poster-placeholder">📺</div>
                    {{ end }}
                    <div class="content">
                        <div class="show-title">{{ .SeriesTitle }}</div>
                        <div class="episode-info">S{{ printf "%02d" .SeasonNum }}E{{ printf "%02d" .EpisodeNum }}{{ if .Title }} - {{ .Title }}{{ end }}{{ if .AirDate }} ({{ .AirDate }}){{ end }}</div>
                    </div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No shows scheduled for next week</div>
            {{ end }}

            <h3>Movies ({{ len .UpcomingMovies }})</h3>
            {{ if .UpcomingMovies }}
                {{ range .UpcomingMovies }}
                <div class="item">
                    {{ if .PosterURL }}
                        <img src="{{ .PosterURL }}" alt="{{ .Title }}" class="poster" />
                    {{ else }}
                        <div class="poster-placeholder">🎬</div>
                    {{ end }}
                    <div class="content">
                        <div class="movie-title">{{ .Title }}</div>
                        <div class="movie-year">({{ .Year }}){{ if .ReleaseDate }} - Release: {{ .ReleaseDate }}{{ end }}</div>
                    </div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No movies scheduled for next week</div>
            {{ end }}
        </div>

        <div class="footer">
            Generated by Newslettar • {{ .WeekEnd }}
        </div>
    </div>
</body>
</html>
`
	t, err := template.New("newsletter").Parse(tmpl)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func sendEmail(cfg Config, subject, htmlBody string) error {
	auth := smtp.PlainAuth("", cfg.MailgunUser, cfg.MailgunPass, cfg.MailgunSMTP)

	for _, toEmail := range cfg.ToEmails {
		headers := make(map[string]string)
		headers["From"] = cfg.FromEmail
		headers["To"] = toEmail
		headers["Subject"] = subject
		headers["MIME-Version"] = "1.0"
		headers["Content-Type"] = "text/html; charset=\"utf-8\""

		message := ""
		for k, v := range headers {
			message += fmt.Sprintf("%s: %s\r\n", k, v)
		}
		message += "\r\n" + htmlBody

		addr := fmt.Sprintf("%s:%s", cfg.MailgunSMTP, cfg.MailgunPort)
		err := smtp.SendMail(addr, auth, cfg.FromEmail, []string{toEmail}, []byte(message))
		if err != nil {
			return fmt.Errorf("failed to send email to %s: %w", toEmail, err)
		}
		log.Printf("✓ Email sent successfully to %s", toEmail)
	}

	return nil
}

func main() {
	cfg := loadConfig()

	log.Println("🚀 Starting Newslettar - Weekly newsletter generation...")
	log.Printf("Config: Sonarr=%s, Radarr=%s", cfg.SonarrURL, cfg.RadarrURL)

	now := time.Now()
	weekAgo := now.AddDate(0, 0, -7)
	nextWeek := now.AddDate(0, 0, 7)

	log.Println("📡 Fetching Sonarr data...")
	downloadedShows, err := fetchSonarrHistory(cfg, weekAgo)
	if err != nil {
		log.Printf("⚠️  Error fetching Sonarr history: %v", err)
		downloadedShows = []Episode{}
	} else {
		log.Printf("✓ Found %d downloaded shows", len(downloadedShows))
	}

	upcomingShows, err := fetchSonarrCalendar(cfg, now, nextWeek)
	if err != nil {
		log.Printf("⚠️  Error fetching Sonarr calendar: %v", err)
		upcomingShows = []Episode{}
	} else {
		log.Printf("✓ Found %d upcoming shows", len(upcomingShows))
	}

	log.Println("🎬 Fetching Radarr data...")
	downloadedMovies, err := fetchRadarrHistory(cfg, weekAgo)
	if err != nil {
		log.Printf("⚠️  Error fetching Radarr history: %v", err)
		downloadedMovies = []Movie{}
	} else {
		log.Printf("✓ Found %d downloaded movies", len(downloadedMovies))
	}

	upcomingMovies, err := fetchRadarrCalendar(cfg, now, nextWeek)
	if err != nil {
		log.Printf("⚠️  Error fetching Radarr calendar: %v", err)
		upcomingMovies = []Movie{}
	} else {
		log.Printf("✓ Found %d upcoming movies", len(upcomingMovies))
	}

	// Sort by title
	sort.Slice(downloadedShows, func(i, j int) bool {
		return downloadedShows[i].SeriesTitle < downloadedShows[j].SeriesTitle
	})
	sort.Slice(upcomingShows, func(i, j int) bool {
		if upcomingShows[i].AirDate == upcomingShows[j].AirDate {
			return upcomingShows[i].SeriesTitle < upcomingShows[j].SeriesTitle
		}
		return upcomingShows[i].AirDate < upcomingShows[j].AirDate
	})
	sort.Slice(downloadedMovies, func(i, j int) bool {
		return downloadedMovies[i].Title < downloadedMovies[j].Title
	})
	sort.Slice(upcomingMovies, func(i, j int) bool {
		if upcomingMovies[i].ReleaseDate == upcomingMovies[j].ReleaseDate {
			return upcomingMovies[i].Title < upcomingMovies[j].Title
		}
		return upcomingMovies[i].ReleaseDate < upcomingMovies[j].ReleaseDate
	})

	data := NewsletterData{
		WeekStart:        weekAgo.Format("Jan 2"),
		WeekEnd:          now.Format("Jan 2, 2006"),
		DownloadedShows:  downloadedShows,
		DownloadedMovies: downloadedMovies,
		UpcomingShows:    upcomingShows,
		UpcomingMovies:   upcomingMovies,
	}

	log.Println("📝 Generating HTML...")
	html, err := generateHTML(data)
	if err != nil {
		log.Fatalf("❌ Error generating HTML: %v", err)
	}

	subject := fmt.Sprintf("Newslettar - Week of %s", now.Format("Jan 2, 2006"))

	log.Println("📧 Sending email...")
	if err := sendEmail(cfg, subject, html); err != nil {
		log.Fatalf("❌ Error sending email: %v", err)
	}

	log.Println("✅ Newsletter sent successfully!")
}
