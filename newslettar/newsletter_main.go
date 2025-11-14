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
}

type Movie struct {
	Title       string
	Year        int
	ReleaseDate string
	Downloaded  bool
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
		ToEmails:     []string{getEnv("TO_EMAILS", "user@example.com")},
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fetchSonarrHistory(cfg Config, since time.Time) ([]Episode, error) {
	url := fmt.Sprintf("%s/api/v3/history?pageSize=1000&sortKey=date&sortDirection=descending", cfg.SonarrURL)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Api-Key", cfg.SonarrAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Records []struct {
			SeriesID      int       `json:"seriesId"`
			EpisodeID     int       `json:"episodeId"`
			SourceTitle   string    `json:"sourceTitle"`
			Date          time.Time `json:"date"`
			EventType     string    `json:"eventType"`
			Series        struct {
				Title string `json:"title"`
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
		return nil, err
	}

	var episodes []Episode
	seen := make(map[string]bool)

	for _, record := range result.Records {
		if record.EventType == "downloadFolderImported" && record.Date.After(since) {
			key := fmt.Sprintf("%d-%d-%d", record.SeriesID, record.Episode.SeasonNumber, record.Episode.EpisodeNumber)
			if !seen[key] {
				episodes = append(episodes, Episode{
					SeriesTitle: record.Series.Title,
					SeasonNum:   record.Episode.SeasonNumber,
					EpisodeNum:  record.Episode.EpisodeNumber,
					Title:       record.Episode.Title,
					AirDate:     record.Episode.AirDate,
					Downloaded:  true,
				})
				seen[key] = true
			}
		}
	}

	return episodes, nil
}

func fetchSonarrCalendar(cfg Config, start, end time.Time) ([]Episode, error) {
	url := fmt.Sprintf("%s/api/v3/calendar?start=%s&end=%s",
		cfg.SonarrURL,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"))

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Api-Key", cfg.SonarrAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result []struct {
		Series struct {
			Title string `json:"title"`
		} `json:"series"`
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
		Title         string `json:"title"`
		AirDate       string `json:"airDate"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	var episodes []Episode
	for _, ep := range result {
		episodes = append(episodes, Episode{
			SeriesTitle: ep.Series.Title,
			SeasonNum:   ep.SeasonNumber,
			EpisodeNum:  ep.EpisodeNumber,
			Title:       ep.Title,
			AirDate:     ep.AirDate,
			Downloaded:  false,
		})
	}

	return episodes, nil
}

func fetchRadarrHistory(cfg Config, since time.Time) ([]Movie, error) {
	url := fmt.Sprintf("%s/api/v3/history?pageSize=1000&sortKey=date&sortDirection=descending", cfg.RadarrURL)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Api-Key", cfg.RadarrAPIKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Records []struct {
			MovieID   int       `json:"movieId"`
			Date      time.Time `json:"date"`
			EventType string    `json:"eventType"`
			Movie     struct {
				Title string `json:"title"`
				Year  int    `json:"year"`
			} `json:"movie"`
		} `json:"records"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	var movies []Movie
	seen := make(map[int]bool)

	for _, record := range result.Records {
		if record.EventType == "downloadFolderImported" && record.Date.After(since) && !seen[record.MovieID] {
			movies = append(movies, Movie{
				Title:      record.Movie.Title,
				Year:       record.Movie.Year,
				Downloaded: true,
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

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result []struct {
		Title           string `json:"title"`
		Year            int    `json:"year"`
		PhysicalRelease string `json:"physicalRelease"`
		DigitalRelease  string `json:"digitalRelease"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	var movies []Movie
	for _, m := range result {
		releaseDate := m.DigitalRelease
		if releaseDate == "" {
			releaseDate = m.PhysicalRelease
		}
		movies = append(movies, Movie{
			Title:       m.Title,
			Year:        m.Year,
			ReleaseDate: releaseDate,
			Downloaded:  false,
		})
	}

	return movies, nil
}

func generateHTML(data NewsletterData) (string, error) {
	tmpl := `
<!DOCTYPE html>
<html>
<head>
    <style>
        body { font-family: Arial, sans-serif; max-width: 800px; margin: 0 auto; padding: 20px; background-color: #f5f5f5; }
        .container { background-color: white; padding: 30px; border-radius: 8px; }
        h1 { color: #2c3e50; border-bottom: 3px solid #3498db; padding-bottom: 10px; }
        h2 { color: #34495e; margin-top: 30px; border-left: 4px solid #3498db; padding-left: 15px; }
        .section { margin-bottom: 30px; }
        .item { padding: 12px; margin: 8px 0; background-color: #f8f9fa; border-left: 3px solid #3498db; border-radius: 4px; }
        .show-title { font-weight: bold; color: #2c3e50; }
        .episode-info { color: #7f8c8d; font-size: 0.9em; }
        .movie-title { font-weight: bold; color: #2c3e50; }
        .movie-year { color: #7f8c8d; }
        .date-range { color: #7f8c8d; font-size: 0.9em; margin-bottom: 20px; }
        .empty { color: #95a5a6; font-style: italic; padding: 10px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>📺 Weekly Media Newsletter</h1>
        <div class="date-range">Week of {{ .WeekStart }} - {{ .WeekEnd }}</div>

        <div class="section">
            <h2>📥 Downloaded This Week</h2>
            
            <h3>TV Shows</h3>
            {{ if .DownloadedShows }}
                {{ range .DownloadedShows }}
                <div class="item">
                    <div class="show-title">{{ .SeriesTitle }}</div>
                    <div class="episode-info">S{{ printf "%02d" .SeasonNum }}E{{ printf "%02d" .EpisodeNum }} - {{ .Title }}</div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No shows downloaded this week</div>
            {{ end }}

            <h3>Movies</h3>
            {{ if .DownloadedMovies }}
                {{ range .DownloadedMovies }}
                <div class="item">
                    <span class="movie-title">{{ .Title }}</span>
                    <span class="movie-year">({{ .Year }})</span>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No movies downloaded this week</div>
            {{ end }}
        </div>

        <div class="section">
            <h2>📅 Coming Next Week</h2>
            
            <h3>TV Shows</h3>
            {{ if .UpcomingShows }}
                {{ range .UpcomingShows }}
                <div class="item">
                    <div class="show-title">{{ .SeriesTitle }}</div>
                    <div class="episode-info">S{{ printf "%02d" .SeasonNum }}E{{ printf "%02d" .EpisodeNum }} - {{ .Title }} ({{ .AirDate }})</div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No shows scheduled for next week</div>
            {{ end }}

            <h3>Movies</h3>
            {{ if .UpcomingMovies }}
                {{ range .UpcomingMovies }}
                <div class="item">
                    <span class="movie-title">{{ .Title }}</span>
                    <span class="movie-year">({{ .Year }})</span>
                    {{ if .ReleaseDate }}
                    <div class="episode-info">Release: {{ .ReleaseDate }}</div>
                    {{ end }}
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No movies scheduled for next week</div>
            {{ end }}
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

	headers := make(map[string]string)
	headers["From"] = cfg.FromEmail
	headers["To"] = cfg.ToEmails[0]
	headers["Subject"] = subject
	headers["MIME-Version"] = "1.0"
	headers["Content-Type"] = "text/html; charset=\"utf-8\""

	message := ""
	for k, v := range headers {
		message += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	message += "\r\n" + htmlBody

	addr := fmt.Sprintf("%s:%s", cfg.MailgunSMTP, cfg.MailgunPort)
	return smtp.SendMail(addr, auth, cfg.FromEmail, cfg.ToEmails, []byte(message))
}

func main() {
	cfg := loadConfig()

	log.Println("Starting weekly newsletter generation...")

	now := time.Now()
	weekAgo := now.AddDate(0, 0, -7)
	nextWeek := now.AddDate(0, 0, 7)

	log.Println("Fetching Sonarr data...")
	downloadedShows, err := fetchSonarrHistory(cfg, weekAgo)
	if err != nil {
		log.Printf("Error fetching Sonarr history: %v", err)
	}

	upcomingShows, err := fetchSonarrCalendar(cfg, now, nextWeek)
	if err != nil {
		log.Printf("Error fetching Sonarr calendar: %v", err)
	}

	log.Println("Fetching Radarr data...")
	downloadedMovies, err := fetchRadarrHistory(cfg, weekAgo)
	if err != nil {
		log.Printf("Error fetching Radarr history: %v", err)
	}

	upcomingMovies, err := fetchRadarrCalendar(cfg, now, nextWeek)
	if err != nil {
		log.Printf("Error fetching Radarr calendar: %v", err)
	}

	sort.Slice(downloadedShows, func(i, j int) bool {
		return downloadedShows[i].SeriesTitle < downloadedShows[j].SeriesTitle
	})
	sort.Slice(upcomingShows, func(i, j int) bool {
		return upcomingShows[i].AirDate < upcomingShows[j].AirDate
	})

	data := NewsletterData{
		WeekStart:        weekAgo.Format("Jan 2"),
		WeekEnd:          now.Format("Jan 2, 2006"),
		DownloadedShows:  downloadedShows,
		DownloadedMovies: downloadedMovies,
		UpcomingShows:    upcomingShows,
		UpcomingMovies:   upcomingMovies,
	}

	log.Println("Generating HTML...")
	html, err := generateHTML(data)
	if err != nil {
		log.Fatalf("Error generating HTML: %v", err)
	}

	subject := fmt.Sprintf("Newslettar - Week of %s", now.Format("Jan 2, 2006"))

	log.Println("Sending email...")
	if err := sendEmail(cfg, subject, html); err != nil {
		log.Fatalf("Error sending email: %v", err)
	}

	log.Println("✓ Newsletter sent successfully!")
}