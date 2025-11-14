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

type SeriesGroup struct {
	SeriesTitle string
	PosterURL   string
	Episodes    []Episode
}

type NewsletterData struct {
	WeekStart              string
	WeekEnd                string
	UpcomingSeriesGroups   []SeriesGroup
	UpcomingMovies         []Movie
	DownloadedSeriesGroups []SeriesGroup
	DownloadedMovies       []Movie
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

func groupEpisodesBySeries(episodes []Episode) []SeriesGroup {
	seriesMap := make(map[string]*SeriesGroup)

	for _, ep := range episodes {
		if _, exists := seriesMap[ep.SeriesTitle]; !exists {
			seriesMap[ep.SeriesTitle] = &SeriesGroup{
				SeriesTitle: ep.SeriesTitle,
				PosterURL:   ep.PosterURL,
				Episodes:    []Episode{},
			}
		}
		seriesMap[ep.SeriesTitle].Episodes = append(seriesMap[ep.SeriesTitle].Episodes, ep)
	}

	var groups []SeriesGroup
	for _, group := range seriesMap {
		sort.Slice(group.Episodes, func(i, j int) bool {
			if group.Episodes[i].SeasonNum != group.Episodes[j].SeasonNum {
				return group.Episodes[i].SeasonNum < group.Episodes[j].SeasonNum
			}
			return group.Episodes[i].EpisodeNum < group.Episodes[j].EpisodeNum
		})
		groups = append(groups, *group)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].SeriesTitle < groups[j].SeriesTitle
	})

	return groups
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
            margin-top: 40px; 
            border-left: 4px solid #3498db; 
            padding-left: 15px; 
        }
        h3 { 
            color: #2c3e50; 
            margin-top: 25px; 
            margin-bottom: 15px; 
            font-size: 1.2em; 
        }
        .section { 
            margin-bottom: 30px; 
        }
        .series-group {
            margin-bottom: 25px;
            border: 1px solid #e0e0e0;
            border-radius: 8px;
            overflow: hidden;
            background-color: #fafafa;
        }
        .series-header {
            display: flex;
            align-items: center;
            padding: 15px;
            background-color: #f0f0f0;
            border-bottom: 2px solid #3498db;
        }
        .poster {
            width: 60px;
            height: 90px;
            object-fit: cover;
            border-radius: 4px;
            margin-right: 15px;
            flex-shrink: 0;
            box-shadow: 0 2px 4px rgba(0,0,0,0.2);
        }
        .poster-placeholder {
            width: 60px;
            height: 90px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            border-radius: 4px;
            margin-right: 15px;
            flex-shrink: 0;
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 28px;
            color: white;
        }
        .series-title {
            font-weight: bold;
            font-size: 1.3em;
            color: #2c3e50;
        }
        .episode-list {
            padding: 10px 15px;
        }
        .episode-item {
            padding: 10px;
            margin: 5px 0;
            background-color: white;
            border-left: 3px solid #3498db;
            border-radius: 4px;
        }
        .episode-number {
            font-weight: 600;
            color: #3498db;
            display: inline-block;
            min-width: 70px;
        }
        .episode-title {
            color: #2c3e50;
        }
        .episode-date {
            color: #7f8c8d;
            font-size: 0.9em;
            margin-left: 10px;
        }
        .movie-item { 
            display: flex;
            padding: 15px; 
            margin: 12px 0; 
            background-color: #f8f9fa; 
            border-left: 3px solid #e74c3c; 
            border-radius: 8px;
            align-items: flex-start;
            transition: transform 0.2s;
        }
        .movie-item:hover {
            transform: translateX(5px);
            background-color: #e9ecef;
        }
        .movie-poster {
            width: 80px;
            height: 120px;
            object-fit: cover;
            border-radius: 6px;
            margin-right: 15px;
            flex-shrink: 0;
            box-shadow: 0 2px 4px rgba(0,0,0,0.2);
        }
        .movie-poster-placeholder {
            width: 80px;
            height: 120px;
            background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
            border-radius: 6px;
            margin-right: 15px;
            flex-shrink: 0;
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 36px;
            color: white;
        }
        .movie-content {
            flex: 1;
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
        .count-badge {
            background-color: #3498db;
            color: white;
            padding: 4px 10px;
            border-radius: 12px;
            font-size: 0.85em;
            margin-left: 10px;
            font-weight: normal;
        }
        .downloaded-section {
            margin-top: 50px;
            padding-top: 30px;
            border-top: 2px dashed #e0e0e0;
        }
        .downloaded-section h2 {
            color: #7f8c8d;
            border-left-color: #95a5a6;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>📺 Newslettar</h1>
        <div class="date-range">Week of {{ .WeekStart }} - {{ .WeekEnd }}</div>

        <div class="section">
            <h2>📅 Coming Next Week</h2>
            
            <h3>TV Shows <span class="count-badge">{{ len .UpcomingSeriesGroups }}</span></h3>
            {{ if .UpcomingSeriesGroups }}
                {{ range .UpcomingSeriesGroups }}
                <div class="series-group">
                    <div class="series-header">
                        {{ if .PosterURL }}
                            <img src="{{ .PosterURL }}" alt="{{ .SeriesTitle }}" class="poster" />
                        {{ else }}
                            <div class="poster-placeholder">📺</div>
                        {{ end }}
                        <div class="series-title">{{ .SeriesTitle }} <span style="color: #7f8c8d; font-size: 0.8em; font-weight: normal;">({{ len .Episodes }} episode{{ if gt (len .Episodes) 1 }}s{{ end }})</span></div>
                    </div>
                    <div class="episode-list">
                        {{ range .Episodes }}
                        <div class="episode-item">
                            <span class="episode-number">S{{ printf "%02d" .SeasonNum }}E{{ printf "%02d" .EpisodeNum }}</span>
                            <span class="episode-title">{{ if .Title }}{{ .Title }}{{ else }}TBA{{ end }}</span>
                            {{ if .AirDate }}<span class="episode-date">{{ .AirDate }}</span>{{ end }}
                        </div>
                        {{ end }}
                    </div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No shows scheduled for next week</div>
            {{ end }}

            <h3>Movies <span class="count-badge">{{ len .UpcomingMovies }}</span></h3>
            {{ if .UpcomingMovies }}
                {{ range .UpcomingMovies }}
                <div class="movie-item">
                    {{ if .PosterURL }}
                        <img src="{{ .PosterURL }}" alt="{{ .Title }}" class="movie-poster" />
                    {{ else }}
                        <div class="movie-poster-placeholder">🎬</div>
                    {{ end }}
                    <div class="movie-content">
                        <div class="movie-title">{{ .Title }}</div>
                        <div class="movie-year">({{ .Year }}){{ if .ReleaseDate }} • {{ .ReleaseDate }}{{ end }}</div>
                    </div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No movies scheduled for next week</div>
            {{ end }}
        </div>

        <div class="section downloaded-section">
            <h2>📥 Downloaded Last Week</h2>
            
            <h3>TV Shows <span class="count-badge">{{ len .DownloadedSeriesGroups }}</span></h3>
            {{ if .DownloadedSeriesGroups }}
                {{ range .DownloadedSeriesGroups }}
                <div class="series-group">
                    <div class="series-header">
                        {{ if .PosterURL }}
                            <img src="{{ .PosterURL }}" alt="{{ .SeriesTitle }}" class="poster" />
                        {{ else }}
                            <div class="poster-placeholder">📺</div>
                        {{ end }}
                        <div class="series-title">{{ .SeriesTitle }} <span style="color: #7f8c8d; font-size: 0.8em; font-weight: normal;">({{ len .Episodes }} episode{{ if gt (len .Episodes) 1 }}s{{ end }})</span></div>
                    </div>
                    <div class="episode-list">
                        {{ range .Episodes }}
                        <div class="episode-item">
                            <span class="episode-number">S{{ printf "%02d" .SeasonNum }}E{{ printf "%02d" .EpisodeNum }}</span>
                            <span class="episode-title">{{ if .Title }}{{ .Title }}{{ else }}Episode {{ .EpisodeNum }}{{ end }}</span>
                        </div>
                        {{ end }}
                    </div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No shows downloaded this week</div>
            {{ end }}

            <h3>Movies <span class="count-badge">{{ len .DownloadedMovies }}</span></h3>
            {{ if .DownloadedMovies }}
                {{ range .DownloadedMovies }}
                <div class="movie-item">
                    {{ if .PosterURL }}
                        <img src="{{ .PosterURL }}" alt="{{ .Title }}" class="movie-poster" />
                    {{ else }}
                        <div class="movie-poster-placeholder">🎬</div>
                    {{ end }}
                    <div class="movie-content">
                        <div class="movie-title">{{ .Title }}</div>
                        <div class="movie-year">({{ .Year }})</div>
                    </div>
                </div>
                {{ end }}
            {{ else }}
                <div class="empty">No movies downloaded this week</div>
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
		log.Printf("✓ Found %d downloaded episodes", len(downloadedShows))
	}

	upcomingShows, err := fetchSonarrCalendar(cfg, now, nextWeek)
	if err != nil {
		log.Printf("⚠️  Error fetching Sonarr calendar: %v", err)
		upcomingShows = []Episode{}
	} else {
		log.Printf("✓ Found %d upcoming episodes", len(upcomingShows))
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

	log.Println("📊 Grouping episodes by series...")
	downloadedSeriesGroups := groupEpisodesBySeries(downloadedShows)
	upcomingSeriesGroups := groupEpisodesBySeries(upcomingShows)

	log.Printf("✓ Grouped into %d downloaded series and %d upcoming series",
		len(downloadedSeriesGroups), len(upcomingSeriesGroups))

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
		WeekStart:              weekAgo.Format("Jan 2"),
		WeekEnd:                now.Format("Jan 2, 2006"),
		UpcomingSeriesGroups:   upcomingSeriesGroups,
		UpcomingMovies:         upcomingMovies,
		DownloadedSeriesGroups: downloadedSeriesGroups,
		DownloadedMovies:       downloadedMovies,
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
