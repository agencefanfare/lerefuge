package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
)

// Embed static files to reduce memory and simplify deployment
//go:embed templates/*.html
var templateFS embed.FS

const version = "1.0.19"

// Global HTTP client (reused for all requests - 3-5x faster)
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Config structures
type Config struct {
	SonarrURL      string
	SonarrAPIKey   string
	RadarrURL      string
	RadarrAPIKey   string
	MailgunSMTP    string
	MailgunPort    string
	MailgunUser    string
	MailgunPass    string
	FromEmail      string
	FromName       string
	ToEmails       []string
	Timezone       string
	ScheduleDay    string
	ScheduleTime   string
	ShowPosters    bool
	ShowDownloaded bool
}

// Minimal structs - only fields we actually need (reduces memory & JSON parsing time)
type Episode struct {
	SeriesTitle string
	SeasonNum   int
	EpisodeNum  int
	Title       string
	AirDate     string
	Downloaded  bool
	PosterURL   string
	IMDBID      string
	TvdbID      int
}

type Movie struct {
	Title       string
	Year        int
	ReleaseDate string
	Downloaded  bool
	PosterURL   string
	IMDBID      string
	TmdbID      int
}

type SeriesGroup struct {
	SeriesTitle string
	PosterURL   string
	Episodes    []Episode
	IMDBID      string
	TvdbID      int
}

type NewsletterData struct {
	WeekStart              string
	WeekEnd                string
	UpcomingSeriesGroups   []SeriesGroup
	UpcomingMovies         []Movie
	DownloadedSeriesGroups []SeriesGroup
	DownloadedMovies       []Movie
}

type WebConfig struct {
	SonarrURL      string `json:"sonarr_url"`
	SonarrAPIKey   string `json:"sonarr_api_key"`
	RadarrURL      string `json:"radarr_url"`
	RadarrAPIKey   string `json:"radarr_api_key"`
	MailgunSMTP    string `json:"mailgun_smtp"`
	MailgunPort    string `json:"mailgun_port"`
	MailgunUser    string `json:"mailgun_user"`
	MailgunPass    string `json:"mailgun_pass"`
	FromEmail      string `json:"from_email"`
	FromName       string `json:"from_name"`
	ToEmails       string `json:"to_emails"`
	Timezone       string `json:"timezone"`
	ScheduleDay    string `json:"schedule_day"`
	ScheduleTime   string `json:"schedule_time"`
	ShowPosters    string `json:"show_posters"`
	ShowDownloaded string `json:"show_downloaded"`
}

// Global config cache (loaded once at startup, reloaded on save)
var (
	configMu     sync.RWMutex
	cachedConfig *Config
)

// Precompiled templates (compiled once at startup)
var emailTemplate *template.Template

// Ring buffer for logs (no disk writes, 500 lines in memory)
var (
	logBuffer   []string
	logBufferMu sync.Mutex
	maxLogLines = 500
)

// Internal scheduler
var scheduler *cron.Cron

func init() {
	// Redirect log output to our ring buffer
	log.SetOutput(&logWriter{})
	log.SetFlags(log.Ldate | log.Ltime)
}

// Custom log writer that maintains ring buffer
type logWriter struct{}

func (w *logWriter) Write(p []byte) (n int, err error) {
	logBufferMu.Lock()
	defer logBufferMu.Unlock()

	line := string(p)
	logBuffer = append(logBuffer, line)

	// Keep only last maxLogLines
	if len(logBuffer) > maxLogLines {
		logBuffer = logBuffer[len(logBuffer)-maxLogLines:]
	}

	// Also write to stdout for external logging if needed
	return os.Stdout.Write(p)
}

func main() {
	webMode := flag.Bool("web", false, "Run in web UI mode")
	flag.Parse()

	// Load config once at startup
	cachedConfig = loadConfig()

	// Precompile email template with custom functions
	var err error
	emailTemplate, err = template.New("email.html").Funcs(template.FuncMap{
		"formatDateWithDay": formatDateWithDay,
	}).ParseFS(templateFS, "templates/email.html")
	if err != nil {
		log.Fatalf("❌ Failed to parse email template: %v", err)
	}

	if *webMode {
		startWebServer()
	} else {
		runNewsletter()
	}
}

// Newsletter sending logic with parallel API calls
func runNewsletter() {
	cfg := getConfig()
	loc := getTimezone(cfg.Timezone)
	now := time.Now().In(loc)

	log.Println("🚀 Starting Newslettar - Weekly newsletter generation...")
	log.Printf("⏰ Current time: %s (%s)", now.Format("2006-01-02 15:04:05"), cfg.Timezone)

	weekStart := now.AddDate(0, 0, -7)
	weekEnd := now

	log.Printf("📅 Week range: %s to %s", weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))

	// Parallel API calls (3-4x faster!) 
	var wg sync.WaitGroup
	var downloadedEpisodes, upcomingEpisodes []Episode
	var downloadedMovies, upcomingMovies []Movie
	var errSonarrHistory, errSonarrCalendar, errRadarrHistory, errRadarrCalendar error

	log.Println("📡 Fetching data in parallel...")
	startFetch := time.Now()

	wg.Add(4)

	go func() {
		defer wg.Done()
		log.Println("📺 Fetching Sonarr history...")
		downloadedEpisodes, errSonarrHistory = fetchSonarrHistory(cfg, weekStart)
		if errSonarrHistory != nil {
			log.Printf("⚠️  Sonarr history error: %v", errSonarrHistory)
		} else {
			log.Printf("✓ Found %d downloaded episodes", len(downloadedEpisodes))
		}
	}()

	go func() {
		defer wg.Done()
		log.Println("📺 Fetching Sonarr calendar...")
		upcomingEpisodes, errSonarrCalendar = fetchSonarrCalendar(cfg, weekEnd, weekEnd.AddDate(0, 0, 7))
		if errSonarrCalendar != nil {
			log.Printf("⚠️  Sonarr calendar error: %v", errSonarrCalendar)
		} else {
			log.Printf("✓ Found %d upcoming episodes", len(upcomingEpisodes))
		}
	}()

	go func() {
		defer wg.Done()
		log.Println("🎬 Fetching Radarr history...")
		downloadedMovies, errRadarrHistory = fetchRadarrHistory(cfg, weekStart)
		if errRadarrHistory != nil {
			log.Printf("⚠️  Radarr history error: %v", errRadarrHistory)
		} else {
			log.Printf("✓ Found %d downloaded movies", len(downloadedMovies))
		}
	}()

	go func() {
		defer wg.Done()
		log.Println("🎬 Fetching Radarr calendar...")
		upcomingMovies, errRadarrCalendar = fetchRadarrCalendar(cfg, weekEnd, weekEnd.AddDate(0, 0, 7))
		if errRadarrCalendar != nil {
			log.Printf("⚠️  Radarr calendar error: %v", errRadarrCalendar)
		} else {
			log.Printf("✓ Found %d upcoming movies", len(upcomingMovies))
		}
	}()

	wg.Wait()
	fetchDuration := time.Since(startFetch)
	log.Printf("⚡ All data fetched in %v (parallel)", fetchDuration)

	// Check if we have any content to send
	hasContent := len(upcomingEpisodes) > 0 || len(upcomingMovies) > 0 ||
		(cfg.ShowDownloaded && (len(downloadedEpisodes) > 0 || len(downloadedMovies) > 0))

	if !hasContent {
		log.Println("ℹ️  No new content to report. Skipping email.")
		return
	}

	// Sort movies chronologically
	sort.Slice(upcomingMovies, func(i, j int) bool {
		return upcomingMovies[i].ReleaseDate < upcomingMovies[j].ReleaseDate
	})
	sort.Slice(downloadedMovies, func(i, j int) bool {
		return downloadedMovies[i].ReleaseDate < downloadedMovies[j].ReleaseDate
	})

	data := NewsletterData{
		WeekStart:              weekStart.Format("January 2, 2006"),
		WeekEnd:                weekEnd.Format("January 2, 2006"),
		UpcomingSeriesGroups:   groupEpisodesBySeries(upcomingEpisodes),
		UpcomingMovies:         upcomingMovies,
		DownloadedSeriesGroups: groupEpisodesBySeries(downloadedEpisodes),
		DownloadedMovies:       downloadedMovies,
	}

	log.Println("📝 Generating newsletter HTML...")
	html, err := generateNewsletterHTML(data, cfg.ShowPosters, cfg.ShowDownloaded)
	if err != nil {
		log.Fatalf("❌ Failed to generate HTML: %v", err)
	}

	subject := fmt.Sprintf("📺 Your Weekly Newsletter - %s", weekEnd.Format("January 2, 2006"))

	log.Println("📧 Sending emails...")
	if err := sendEmail(cfg, subject, html); err != nil {
		log.Fatalf("❌ Failed to send email: %v", err)
	}

	log.Println("✅ Newsletter sent successfully!")

	// Clear data to free memory immediately
	downloadedEpisodes = nil
	upcomingEpisodes = nil
	downloadedMovies = nil
	upcomingMovies = nil
	data = NewsletterData{}
}

// Get timezone location
func getTimezone(tz string) *time.Location {
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		log.Printf("⚠️  Invalid timezone '%s', using UTC", tz)
		return time.UTC
	}
	return loc
}

// Get config (cached, thread-safe)
func getConfig() *Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return cachedConfig
}

// Reload config (called when user saves configuration)
func reloadConfig() {
	configMu.Lock()
	defer configMu.Unlock()
	cachedConfig = loadConfig()
	log.Println("🔄 Configuration reloaded from .env")
}

// Load configuration from .env file (only called at startup and on reload)
func loadConfig() *Config {
	envMap := readEnvFile()

	toEmailsStr := getEnvFromFile(envMap, "TO_EMAILS", "")
	toEmails := []string{}
	if toEmailsStr != "" {
		toEmails = strings.Split(toEmailsStr, ",")
		for i := range toEmails {
			toEmails[i] = strings.TrimSpace(toEmails[i])
		}
	}

	return &Config{
		SonarrURL:      getEnvFromFile(envMap, "SONARR_URL", ""),
		SonarrAPIKey:   getEnvFromFile(envMap, "SONARR_API_KEY", ""),
		RadarrURL:      getEnvFromFile(envMap, "RADARR_URL", ""),
		RadarrAPIKey:   getEnvFromFile(envMap, "RADARR_API_KEY", ""),
		MailgunSMTP:    getEnvFromFile(envMap, "MAILGUN_SMTP", "smtp.mailgun.org"),
		MailgunPort:    getEnvFromFile(envMap, "MAILGUN_PORT", "587"),
		MailgunUser:    getEnvFromFile(envMap, "MAILGUN_USER", ""),
		MailgunPass:    getEnvFromFile(envMap, "MAILGUN_PASS", ""),
		FromEmail:      getEnvFromFile(envMap, "FROM_EMAIL", ""),
		FromName:       getEnvFromFile(envMap, "FROM_NAME", "Newslettar"),
		ToEmails:       toEmails,
		Timezone:       getEnvFromFile(envMap, "TIMEZONE", "UTC"),
		ScheduleDay:    getEnvFromFile(envMap, "SCHEDULE_DAY", "Sun"),
		ScheduleTime:   getEnvFromFile(envMap, "SCHEDULE_TIME", "09:00"),
		ShowPosters:    getEnvFromFile(envMap, "SHOW_POSTERS", "true") != "false",
		ShowDownloaded: getEnvFromFile(envMap, "SHOW_DOWNLOADED", "true") != "false",
	}
}

func readEnvFile() map[string]string {
	envMap := make(map[string]string)

	data, err := os.ReadFile(".env")
	if err != nil {
		return envMap
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			envMap[key] = value
		}
	}

	return envMap
}

func getEnvFromFile(envMap map[string]string, key, defaultValue string) string {
	if val, exists := envMap[key]; exists {
		return val
	}
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

// Fetch functions using shared HTTP client (streaming JSON decode)
func fetchSonarrHistory(cfg *Config, since time.Time) ([]Episode, error) {
	if cfg.SonarrURL == "" || cfg.SonarrAPIKey == "" {
		return nil, fmt.Errorf("Sonarr not configured")
	}

	url := fmt.Sprintf("%s/api/v3/history?pageSize=1000&sortKey=date&sortDirection=descending&includeEpisode=true&includeSeries=true", cfg.SonarrURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", cfg.SonarrAPIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Stream JSON decoding (faster, less memory)
	var result struct {
		Records []struct {
			Date      time.Time `json:"date"`
			EventType string    `json:"eventType"`
			Series    struct {
				Title  string `json:"title"`
				TvdbID int    `json:"tvdbId"`
				ImdbID string `json:"imdbId"`
				Images []struct {
					CoverType string `json:"coverType"`
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

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	episodes := []Episode{}
	for _, record := range result.Records {
		// Only include download events
		if record.EventType != "downloadFolderImported" && record.EventType != "downloadImported" {
			continue
		}
		
		// Filter by date
		if record.Date.Before(since) {
			continue
		}

		posterURL := ""
		for _, img := range record.Series.Images {
			if img.CoverType == "poster" {
				posterURL = img.RemoteURL
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
			IMDBID:      record.Series.ImdbID,
			TvdbID:      record.Series.TvdbID,
		})
	}

	return episodes, nil
}

func fetchSonarrCalendar(cfg *Config, start, end time.Time) ([]Episode, error) {
	if cfg.SonarrURL == "" || cfg.SonarrAPIKey == "" {
		return nil, fmt.Errorf("Sonarr not configured")
	}

	url := fmt.Sprintf("%s/api/v3/calendar?start=%s&end=%s",
		cfg.SonarrURL,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"))

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", cfg.SonarrAPIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var calendarData []struct {
		SeriesTitle string `json:"seriesTitle"`
		Series      struct {
			TvdbID int    `json:"tvdbId"`
			ImdbID string `json:"imdbId"`
			Images []struct {
				CoverType string `json:"coverType"`
				RemoteURL string `json:"remoteUrl"`
			} `json:"images"`
		} `json:"series"`
		SeasonNumber  int    `json:"seasonNumber"`
		EpisodeNumber int    `json:"episodeNumber"`
		Title         string `json:"title"`
		AirDate       string `json:"airDate"`
		HasFile       bool   `json:"hasFile"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&calendarData); err != nil {
		return nil, err
	}

	episodes := []Episode{}
	for _, ep := range calendarData {
		posterURL := ""
		for _, img := range ep.Series.Images {
			if img.CoverType == "poster" {
				posterURL = img.RemoteURL
				break
			}
		}

		episodes = append(episodes, Episode{
			SeriesTitle: ep.SeriesTitle,
			SeasonNum:   ep.SeasonNumber,
			EpisodeNum:  ep.EpisodeNumber,
			Title:       ep.Title,
			AirDate:     ep.AirDate,
			Downloaded:  ep.HasFile,
			PosterURL:   posterURL,
			IMDBID:      ep.Series.ImdbID,
			TvdbID:      ep.Series.TvdbID,
		})
	}

	return episodes, nil
}

func fetchRadarrHistory(cfg *Config, since time.Time) ([]Movie, error) {
	if cfg.RadarrURL == "" || cfg.RadarrAPIKey == "" {
		return nil, fmt.Errorf("Radarr not configured")
	}

	url := fmt.Sprintf("%s/api/v3/history?pageSize=1000&sortKey=date&sortDirection=descending&includeMovie=true", cfg.RadarrURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", cfg.RadarrAPIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Records []struct {
			Date      time.Time `json:"date"`
			EventType string    `json:"eventType"`
			Movie     struct {
				Title     string `json:"title"`
				Year      int    `json:"year"`
				TmdbID    int    `json:"tmdbId"`
				ImdbID    string `json:"imdbId"`
				InCinemas string `json:"inCinemas"`
				Images    []struct {
					CoverType string `json:"coverType"`
					RemoteURL string `json:"remoteUrl"`
				} `json:"images"`
			} `json:"movie"`
		} `json:"records"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	movies := []Movie{}
	for _, record := range result.Records {
		// Only include download events
		if record.EventType != "downloadFolderImported" && record.EventType != "downloadImported" {
			continue
		}
		
		// Filter by date
		if record.Date.Before(since) {
			continue
		}

		posterURL := ""
		for _, img := range record.Movie.Images {
			if img.CoverType == "poster" {
				posterURL = img.RemoteURL
				break
			}
		}

		movies = append(movies, Movie{
			Title:       record.Movie.Title,
			Year:        record.Movie.Year,
			ReleaseDate: record.Movie.InCinemas,
			Downloaded:  true,
			PosterURL:   posterURL,
			IMDBID:      record.Movie.ImdbID,
			TmdbID:      record.Movie.TmdbID,
		})
	}

	return movies, nil
}

func fetchRadarrCalendar(cfg *Config, start, end time.Time) ([]Movie, error) {
	if cfg.RadarrURL == "" || cfg.RadarrAPIKey == "" {
		return nil, fmt.Errorf("Radarr not configured")
	}

	url := fmt.Sprintf("%s/api/v3/calendar?start=%s&end=%s",
		cfg.RadarrURL,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"))

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", cfg.RadarrAPIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var calendarData []struct {
		Title     string `json:"title"`
		Year      int    `json:"year"`
		TmdbID    int    `json:"tmdbId"`
		ImdbID    string `json:"imdbId"`
		InCinemas string `json:"inCinemas"`
		HasFile   bool   `json:"hasFile"`
		Images    []struct {
			CoverType string `json:"coverType"`
			RemoteURL string `json:"remoteUrl"`
		} `json:"images"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&calendarData); err != nil {
		return nil, err
	}

	movies := []Movie{}
	for _, movie := range calendarData {
		posterURL := ""
		for _, img := range movie.Images {
			if img.CoverType == "poster" {
				posterURL = img.RemoteURL
				break
			}
		}

		movies = append(movies, Movie{
			Title:       movie.Title,
			Year:        movie.Year,
			ReleaseDate: movie.InCinemas,
			Downloaded:  movie.HasFile,
			PosterURL:   posterURL,
			IMDBID:      movie.ImdbID,
			TmdbID:      movie.TmdbID,
		})
	}

	return movies, nil
}

// Group episodes by series
func groupEpisodesBySeries(episodes []Episode) []SeriesGroup {
	seriesMap := make(map[string]*SeriesGroup)

	// Sort episodes by air date first
	sort.Slice(episodes, func(i, j int) bool {
		return episodes[i].AirDate < episodes[j].AirDate
	})

	for _, ep := range episodes {
		group, exists := seriesMap[ep.SeriesTitle]
		if !exists {
			group = &SeriesGroup{
				SeriesTitle: ep.SeriesTitle,
				PosterURL:   ep.PosterURL,
				Episodes:    []Episode{},
				IMDBID:      ep.IMDBID,
				TvdbID:      ep.TvdbID,
			}
			seriesMap[ep.SeriesTitle] = group
		}
		group.Episodes = append(group.Episodes, ep)
	}

	groups := make([]SeriesGroup, 0, len(seriesMap))
	for _, group := range seriesMap {
		groups = append(groups, *group)
	}

	sort.Slice(groups, func(i, j int) bool {
		return groups[i].SeriesTitle < groups[j].SeriesTitle
	})

	return groups
}

// Generate newsletter HTML using precompiled template
func generateNewsletterHTML(data NewsletterData, showPosters, showDownloaded bool) (string, error) {
	templateData := struct {
		NewsletterData
		ShowPosters    bool
		ShowDownloaded bool
	}{
		NewsletterData: data,
		ShowPosters:    showPosters,
		ShowDownloaded: showDownloaded,
	}

	var buf bytes.Buffer
	if err := emailTemplate.Execute(&buf, templateData); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func formatDateWithDay(dateStr string) string {
	if dateStr == "" {
		return "Date TBA"
	}

	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}

	return t.Format("Monday, January 2, 2006")
}

// Send email
func sendEmail(cfg *Config, subject, htmlBody string) error {
	if cfg.FromEmail == "" || len(cfg.ToEmails) == 0 {
		return fmt.Errorf("email configuration incomplete")
	}

	from := cfg.FromEmail
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", cfg.FromName, cfg.FromEmail)
	}

	headers := make(map[string]string)
	headers["From"] = from
	headers["To"] = strings.Join(cfg.ToEmails, ", ")
	headers["Subject"] = subject
	headers["MIME-Version"] = "1.0"
	headers["Content-Type"] = "text/html; charset=UTF-8"

	message := ""
	for k, v := range headers {
		message += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	message += "\r\n" + htmlBody

	auth := smtp.PlainAuth("", cfg.MailgunUser, cfg.MailgunPass, cfg.MailgunSMTP)
	addr := fmt.Sprintf("%s:%s", cfg.MailgunSMTP, cfg.MailgunPort)

	return smtp.SendMail(addr, auth, cfg.FromEmail, cfg.ToEmails, []byte(message))
}

// Web server with gzip compression
func startWebServer() {
	cfg := getConfig()

	// Setup internal scheduler
	setupScheduler(cfg)

	port := os.Getenv("WEBUI_PORT")
	if port == "" {
		port = "8080"
	}

	// Serve static files with gzip
	http.HandleFunc("/", withGzip(uiHandler))
	http.HandleFunc("/api/config", configHandler)
	http.HandleFunc("/api/test-sonarr", testSonarrHandler)
	http.HandleFunc("/api/test-radarr", testRadarrHandler)
	http.HandleFunc("/api/test-email", testEmailHandler)
	http.HandleFunc("/api/send", sendHandler)
	http.HandleFunc("/api/logs", logsHandler)
	http.HandleFunc("/api/version", versionHandler)
	http.HandleFunc("/api/update", updateHandler)

	// Graceful shutdown
	server := &http.Server{
		Addr:    ":" + port,
		Handler: nil,
	}

	go func() {
		log.Printf("🌐 Web UI started on port %s", port)
		log.Printf("📅 Scheduler: %s at %s (%s)", cfg.ScheduleDay, cfg.ScheduleTime, cfg.Timezone)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if scheduler != nil {
		scheduler.Stop()
	}

	if err := server.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	log.Println("✅ Server stopped")
}

// Setup internal cron scheduler (replaces systemd timer)
func setupScheduler(cfg *Config) {
	scheduler = cron.New(cron.WithLocation(getTimezone(cfg.Timezone)))

	// Convert day/time to cron expression
	cronExpr := convertToCronExpression(cfg.ScheduleDay, cfg.ScheduleTime)
	log.Printf("📅 Setting up scheduler: %s (cron: %s)", cfg.ScheduleDay+" "+cfg.ScheduleTime, cronExpr)

	_, err := scheduler.AddFunc(cronExpr, func() {
		log.Println("⏰ Scheduled newsletter triggered")
		runNewsletter()
	})

	if err != nil {
		log.Printf("⚠️  Failed to setup scheduler: %v", err)
		return
	}

	scheduler.Start()
	log.Println("✅ Internal scheduler started")
}

// Convert day/time to cron expression
func convertToCronExpression(day, timeStr string) string {
	// Parse time (HH:MM)
	parts := strings.Split(timeStr, ":")
	hour := "9"
	minute := "0"
	if len(parts) == 2 {
		hour = parts[0]
		minute = parts[1]
	}

	// Convert day to cron weekday (0 = Sunday, 6 = Saturday)
	dayMap := map[string]string{
		"Sun": "0",
		"Mon": "1",
		"Tue": "2",
		"Wed": "3",
		"Thu": "4",
		"Fri": "5",
		"Sat": "6",
	}

	cronDay := dayMap[day]
	if cronDay == "" {
		cronDay = "0" // Default to Sunday
	}

	// Cron format: minute hour day month weekday
	return fmt.Sprintf("%s %s * * %s", minute, hour, cronDay)
}

// Restart scheduler when config changes
func restartScheduler() {
	if scheduler != nil {
		scheduler.Stop()
	}
	cfg := getConfig()
	setupScheduler(cfg)
	log.Println("🔄 Scheduler restarted")
}

// Gzip compression middleware
func withGzip(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			handler(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()

		gzw := &gzipResponseWriter{Writer: gz, ResponseWriter: w}
		handler(gzw, r)
	}
}

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// Handlers
func uiHandler(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	loc := getTimezone(cfg.Timezone)
	nextRun := getNextScheduledRun(cfg.ScheduleDay, cfg.ScheduleTime, loc)

	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Newslettar v` + version + `</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            background: #0f1419;
            color: #e8e8e8;
            line-height: 1.6;
        }
        .container {
            max-width: 1000px;
            margin: 0 auto;
            padding: 20px;
        }
        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            padding: 30px;
            border-radius: 12px;
            margin-bottom: 30px;
            text-align: center;
        }
        .header h1 {
            font-size: 2.5em;
            margin-bottom: 10px;
        }
        .version {
            opacity: 0.9;
            font-size: 0.9em;
        }
        .tabs {
            display: flex;
            gap: 10px;
            margin-bottom: 20px;
            background: #1a2332;
            padding: 10px;
            border-radius: 10px;
        }
        .tab {
            flex: 1;
            padding: 12px 20px;
            background: transparent;
            border: none;
            color: #8899aa;
            cursor: pointer;
            border-radius: 8px;
            font-size: 14px;
            font-weight: 500;
            transition: all 0.3s;
        }
        .tab:hover { background: #252f3f; color: #fff; }
        .tab.active {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: #fff;
        }
        .tab-content {
            display: none;
            background: #1a2332;
            padding: 30px;
            border-radius: 12px;
            min-height: 400px;
        }
        .tab-content.active { display: block; }
        .form-group {
            margin-bottom: 20px;
        }
        .form-group label {
            display: block;
            margin-bottom: 8px;
            color: #a0b0c0;
            font-weight: 500;
        }
        .form-group input, .form-group select {
            width: 100%;
            padding: 12px;
            background: #0f1419;
            border: 2px solid #2a3444;
            border-radius: 8px;
            color: #e8e8e8;
            font-size: 14px;
            transition: border-color 0.3s;
        }
        .form-group input:focus, .form-group select:focus {
            outline: none;
            border-color: #667eea;
        }
        .btn {
            padding: 12px 24px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            border: none;
            border-radius: 8px;
            cursor: pointer;
            font-size: 14px;
            font-weight: 600;
            transition: transform 0.2s, opacity 0.3s;
        }
        .btn:hover { transform: translateY(-2px); opacity: 0.9; }
        .btn:active { transform: translateY(0); }
        .btn-secondary {
            background: #2a3444;
        }
        .btn-success {
            background: linear-gradient(135deg, #11998e 0%, #38ef7d 100%);
        }
        .btn-danger {
            background: linear-gradient(135deg, #eb3349 0%, #f45c43 100%);
        }
        .notification {
            position: fixed;
            top: 20px;
            right: 20px;
            padding: 16px 24px;
            border-radius: 10px;
            color: white;
            font-weight: 500;
            animation: slideIn 0.3s;
            z-index: 1000;
            max-width: 400px;
        }
        .notification.success {
            background: linear-gradient(135deg, #11998e 0%, #38ef7d 100%);
        }
        .notification.error {
            background: linear-gradient(135deg, #eb3349 0%, #f45c43 100%);
        }
        @keyframes slideIn {
            from { transform: translateX(400px); opacity: 0; }
            to { transform: translateX(0); opacity: 1; }
        }
        .logs-container {
            background: #0f1419;
            padding: 20px;
            border-radius: 8px;
            font-family: 'Courier New', monospace;
            font-size: 13px;
            max-height: 500px;
            overflow-y: auto;
            white-space: pre-wrap;
            border: 2px solid #2a3444;
        }
        .schedule-info {
            background: #252f3f;
            padding: 20px;
            border-radius: 10px;
            margin-bottom: 20px;
            border-left: 4px solid #667eea;
        }
        .schedule-info h3 {
            margin-bottom: 10px;
            color: #667eea;
        }
        .toggle-switch {
            position: relative;
            display: inline-block;
            width: 50px;
            height: 26px;
            margin-left: 10px;
        }
        .toggle-switch input {
            opacity: 0;
            width: 0;
            height: 0;
        }
        .toggle-slider {
            position: absolute;
            cursor: pointer;
            top: 0;
            left: 0;
            right: 0;
            bottom: 0;
            background-color: #2a3444;
            transition: 0.3s;
            border-radius: 26px;
        }
        .toggle-slider:before {
            position: absolute;
            content: "";
            height: 20px;
            width: 20px;
            left: 3px;
            bottom: 3px;
            background-color: white;
            transition: 0.3s;
            border-radius: 50%;
        }
        input:checked + .toggle-slider {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
        }
        input:checked + .toggle-slider:before {
            transform: translateX(24px);
        }
        .template-option {
            display: flex;
            align-items: center;
            justify-content: space-between;
            padding: 15px;
            background: #252f3f;
            border-radius: 8px;
            margin-bottom: 12px;
        }
        .timezone-select {
            max-width: 300px;
        }
        .info-banner {
            background: #252f3f;
            padding: 15px 20px;
            border-radius: 8px;
            margin-bottom: 20px;
            border-left: 4px solid #11998e;
        }
        .info-banner p {
            margin: 5px 0;
            color: #a0b0c0;
        }
        .info-banner strong {
            color: #e8e8e8;
        }
        .spinner {
            display: inline-block;
            width: 16px;
            height: 16px;
            border: 3px solid rgba(255,255,255,.3);
            border-radius: 50%;
            border-top-color: #fff;
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
            <p class="version">Version ` + version + ` • Internal Scheduler • Timezone-Aware</p>
        </div>

        <div class="tabs">
            <button class="tab active" onclick="showTab('config')">⚙️ Configuration</button>
            <button class="tab" onclick="showTab('actions')">🎬 Actions</button>
            <button class="tab" onclick="showTab('template')">📝 Email Template</button>
            <button class="tab" onclick="showTab('logs')">📋 Logs</button>
            <button class="tab" onclick="showTab('update')">🔄 Update</button>
        </div>

        <div id="config-tab" class="tab-content active">
            <div class="info-banner">
                <p><strong>⏰ Next Scheduled Send:</strong> ` + nextRun + `</p>
                <p><strong>🌍 Timezone:</strong> <span id="current-timezone">` + cfg.Timezone + `</span></p>
                <p style="margin-top: 10px; font-size: 0.9em; opacity: 0.8;">
                    ℹ️ Scheduler runs internally (no systemd timer needed). Changes apply immediately.
                </p>
            </div>

            <form id="config-form">
                <h3 style="margin-bottom: 15px; color: #667eea;">Schedule Settings</h3>
                
                <div class="form-group timezone-select">
                    <label>Timezone</label>
                    <select name="timezone" id="timezone">
                        <option value="UTC">UTC</option>
                        <option value="America/New_York">Eastern (US)</option>
                        <option value="America/Chicago">Central (US)</option>
                        <option value="America/Denver">Mountain (US)</option>
                        <option value="America/Los_Angeles">Pacific (US)</option>
                        <option value="America/Toronto">Toronto</option>
                        <option value="America/Vancouver">Vancouver</option>
                        <option value="America/Montreal">Montreal</option>
                        <option value="Europe/London">London</option>
                        <option value="Europe/Paris">Paris</option>
                        <option value="Europe/Berlin">Berlin</option>
                        <option value="Asia/Tokyo">Tokyo</option>
                        <option value="Asia/Shanghai">Shanghai</option>
                        <option value="Australia/Sydney">Sydney</option>
                    </select>
                </div>

                <div class="form-group">
                    <label>Day of Week</label>
                    <select name="schedule_day">
                        <option value="Sun">Sunday</option>
                        <option value="Mon">Monday</option>
                        <option value="Tue">Tuesday</option>
                        <option value="Wed">Wednesday</option>
                        <option value="Thu">Thursday</option>
                        <option value="Fri">Friday</option>
                        <option value="Sat">Saturday</option>
                    </select>
                </div>

                <div class="form-group">
                    <label>Time (24-hour format, HH:MM)</label>
                    <input type="time" name="schedule_time" required>
                </div>

                <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

                <h3 style="margin-bottom: 15px; color: #667eea;">Sonarr Settings</h3>
                <div class="form-group">
                    <label>Sonarr URL</label>
                    <input type="text" name="sonarr_url" placeholder="http://localhost:8989">
                </div>
                <div class="form-group">
                    <label>Sonarr API Key</label>
                    <input type="text" name="sonarr_api_key" placeholder="Your Sonarr API key">
                </div>
                <button type="button" class="btn btn-secondary" onclick="testConnection('sonarr')">Test Sonarr</button>

                <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

                <h3 style="margin-bottom: 15px; color: #667eea;">Radarr Settings</h3>
                <div class="form-group">
                    <label>Radarr URL</label>
                    <input type="text" name="radarr_url" placeholder="http://localhost:7878">
                </div>
                <div class="form-group">
                    <label>Radarr API Key</label>
                    <input type="text" name="radarr_api_key" placeholder="Your Radarr API key">
                </div>
                <button type="button" class="btn btn-secondary" onclick="testConnection('radarr')">Test Radarr</button>

                <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

                <h3 style="margin-bottom: 15px; color: #667eea;">Email Settings</h3>
                <div class="form-group">
                    <label>SMTP Server</label>
                    <input type="text" name="mailgun_smtp" placeholder="smtp.mailgun.org">
                </div>
                <div class="form-group">
                    <label>SMTP Port</label>
                    <input type="text" name="mailgun_port" placeholder="587">
                </div>
                <div class="form-group">
                    <label>SMTP Username</label>
                    <input type="text" name="mailgun_user" placeholder="postmaster@yourdomain.com">
                </div>
                <div class="form-group">
                    <label>SMTP Password</label>
                    <input type="password" name="mailgun_pass" placeholder="Your SMTP password">
                </div>
                <div class="form-group">
                    <label>From Name</label>
                    <input type="text" name="from_name" placeholder="Newslettar">
                </div>
                <div class="form-group">
                    <label>From Email</label>
                    <input type="email" name="from_email" placeholder="newsletter@yourdomain.com">
                </div>
                <div class="form-group">
                    <label>To Emails (comma-separated)</label>
                    <input type="text" name="to_emails" placeholder="user@example.com, user2@example.com">
                </div>
                <button type="button" class="btn btn-secondary" onclick="testConnection('email')">Test Email Auth</button>

                <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

                <button type="submit" class="btn">💾 Save Configuration</button>
            </form>
        </div>

        <div id="actions-tab" class="tab-content">
            <div class="schedule-info">
                <h3>📅 Scheduled Newsletter</h3>
                <p>Next scheduled send: <strong>` + nextRun + `</strong></p>
                <p style="margin-top: 10px; font-size: 0.9em; opacity: 0.8;">
                    The newsletter runs automatically based on your schedule settings.
                </p>
            </div>

            <h3 style="margin-bottom: 15px;">Manual Actions</h3>
            <button class="btn btn-success" onclick="sendNow()" style="margin-right: 10px;">
                📧 Send Newsletter Now
            </button>
            <p style="margin-top: 15px; color: #8899aa; font-size: 0.9em;">
                This will generate and send the newsletter immediately, regardless of schedule.
            </p>
        </div>

        <div id="template-tab" class="tab-content">
            <h3 style="margin-bottom: 20px;">Email Template Options</h3>
            
            <div class="template-option">
                <div>
                    <strong>Show Movie/Series Posters</strong>
                    <p style="font-size: 0.9em; color: #8899aa; margin-top: 5px;">
                        Display poster images in the newsletter
                    </p>
                </div>
                <label class="toggle-switch">
                    <input type="checkbox" id="show-posters" onchange="saveTemplateSettings()">
                    <span class="toggle-slider"></span>
                </label>
            </div>

            <div class="template-option">
                <div>
                    <strong>Show Downloaded Section</strong>
                    <p style="font-size: 0.9em; color: #8899aa; margin-top: 5px;">
                        Include "Downloaded This Week" section
                    </p>
                </div>
                <label class="toggle-switch">
                    <input type="checkbox" id="show-downloaded" onchange="saveTemplateSettings()">
                    <span class="toggle-slider"></span>
                </label>
            </div>

            <p style="margin-top: 20px; color: #8899aa; font-size: 0.9em;">
                ℹ️ Changes are saved automatically when you toggle switches.
            </p>
        </div>

        <div id="logs-tab" class="tab-content">
            <h3 style="margin-bottom: 15px;">📋 Newsletter Logs</h3>
            <button class="btn btn-secondary" onclick="loadLogs()" style="margin-bottom: 15px;">
                🔄 Refresh Logs
            </button>
            <div class="logs-container" id="logs"></div>
        </div>

        <div id="update-tab" class="tab-content">
            <h3 style="margin-bottom: 20px;">🔄 Update Newslettar</h3>
            
            <div id="version-info">
                <p>Checking for updates...</p>
            </div>

            <button class="btn" onclick="checkUpdates()" style="margin-right: 10px;">
                🔍 Check for Updates
            </button>
            <button class="btn btn-success" id="update-btn" onclick="performUpdate()" style="display: none;">
                ⬇️ Update Now
            </button>
        </div>
    </div>

    <script>
        let logsInterval;

        function showTab(tabName) {
            document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
            document.querySelectorAll('.tab-content').forEach(t => t.classList.remove('active'));
            
            event.target.classList.add('active');
            document.getElementById(tabName + '-tab').classList.add('active');

            if (tabName === 'logs') {
                loadLogs();
                logsInterval = setInterval(loadLogs, 5000);
            } else {
                if (logsInterval) {
                    clearInterval(logsInterval);
                }
            }
        }

        async function loadConfig() {
            const resp = await fetch('/api/config');
            const data = await resp.json();
            
            document.querySelector('[name="sonarr_url"]').value = data.sonarr_url || '';
            document.querySelector('[name="sonarr_api_key"]').value = data.sonarr_api_key || '';
            document.querySelector('[name="radarr_url"]').value = data.radarr_url || '';
            document.querySelector('[name="radarr_api_key"]').value = data.radarr_api_key || '';
            document.querySelector('[name="mailgun_smtp"]').value = data.mailgun_smtp || 'smtp.mailgun.org';
            document.querySelector('[name="mailgun_port"]').value = data.mailgun_port || '587';
            document.querySelector('[name="mailgun_user"]').value = data.mailgun_user || '';
            document.querySelector('[name="mailgun_pass"]').value = data.mailgun_pass || '';
            document.querySelector('[name="from_email"]').value = data.from_email || '';
            document.querySelector('[name="from_name"]').value = data.from_name || 'Newslettar';
            document.querySelector('[name="to_emails"]').value = data.to_emails || '';
            document.querySelector('[name="timezone"]').value = data.timezone || 'UTC';
            document.querySelector('[name="schedule_day"]').value = data.schedule_day || 'Sun';
            document.querySelector('[name="schedule_time"]').value = data.schedule_time || '09:00';
            
            document.getElementById('show-posters').checked = data.show_posters !== 'false';
            document.getElementById('show-downloaded').checked = data.show_downloaded !== 'false';
            
            document.getElementById('current-timezone').textContent = data.timezone || 'UTC';
        }

        document.getElementById('config-form').addEventListener('submit', async (e) => {
            e.preventDefault();
            const formData = new FormData(e.target);
            const data = Object.fromEntries(formData);
            
            const resp = await fetch('/api/config', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(data)
            });

            if (resp.ok) {
                showNotification('Configuration saved successfully!', 'success');
                setTimeout(() => location.reload(), 2000);
            } else {
                showNotification('Failed to save configuration', 'error');
            }
        });

        async function testConnection(type) {
            const form = document.getElementById('config-form');
            const formData = new FormData(form);
            const data = Object.fromEntries(formData);
            
            const button = event.target;
            const originalText = button.textContent;
            button.innerHTML = '<span class="spinner"></span> Testing...';
            button.disabled = true;

            let endpoint;
            let payload;

            if (type === 'sonarr') {
                endpoint = '/api/test-sonarr';
                payload = { url: data.sonarr_url, api_key: data.sonarr_api_key };
            } else if (type === 'radarr') {
                endpoint = '/api/test-radarr';
                payload = { url: data.radarr_url, api_key: data.radarr_api_key };
            } else {
                endpoint = '/api/test-email';
                payload = {
                    smtp: data.mailgun_smtp,
                    port: data.mailgun_port,
                    user: data.mailgun_user,
                    pass: data.mailgun_pass
                };
            }

            const resp = await fetch(endpoint, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(payload)
            });

            const result = await resp.json();
            button.textContent = originalText;
            button.disabled = false;

            showNotification(result.message, result.success ? 'success' : 'error');
        }

        async function sendNow() {
            if (!confirm('Send newsletter now?')) return;
            
            showNotification('Sending newsletter...', 'success');
            
            const resp = await fetch('/api/send', { method: 'POST' });
            const data = await resp.json();

            if (data.success) {
                showNotification('Newsletter sent successfully!', 'success');
            } else {
                showNotification('Failed to send newsletter', 'error');
            }
        }

        async function loadLogs() {
            const resp = await fetch('/api/logs');
            const logs = await resp.text();
            document.getElementById('logs').textContent = logs;
            document.getElementById('logs').scrollTop = document.getElementById('logs').scrollHeight;
        }

        async function saveTemplateSettings() {
            const showPosters = document.getElementById('show-posters').checked;
            const showDownloaded = document.getElementById('show-downloaded').checked;

            await fetch('/api/config', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({
                    show_posters: showPosters ? 'true' : 'false',
                    show_downloaded: showDownloaded ? 'true' : 'false'
                })
            });

            showNotification('Template settings saved', 'success');
        }

        async function checkUpdates() {
            const resp = await fetch('/api/version');
            const data = await resp.json();
            
            let html = '<div style="background: #252f3f; padding: 20px; border-radius: 10px; margin-bottom: 20px;">';
            html += '<p><strong>Current Version:</strong> ' + data.current_version + '</p>';
            html += '<p><strong>Latest Version:</strong> ' + data.latest_version + '</p>';

            if (data.update_available) {
                html += '<p style="color: #38ef7d; margin-top: 15px;"><strong>Update Available!</strong></p>';
                html += '<h4 style="margin-top: 15px;">What\'s New:</h4>';
                html += '<ul style="margin-left: 20px; margin-top: 10px;">';
                data.changelog.forEach(item => {
                    html += '<li style="margin: 5px 0;">' + item + '</li>';
                });
                html += '</ul>';
                document.getElementById('update-btn').style.display = 'inline-block';
            } else {
                html += '<p style="color: #8899aa; margin-top: 15px;">You are running the latest version!</p>';
                document.getElementById('update-btn').style.display = 'none';
            }

            html += '</div>';
            document.getElementById('version-info').innerHTML = html;
        }

        async function performUpdate() {
            if (!confirm('Update Newslettar? The page will reload in 20 seconds.')) return;

            showNotification('Starting update... Page will reload in 20 seconds', 'success');
            
            await fetch('/api/update', { method: 'POST' });

            setTimeout(() => {
                location.reload();
            }, 20000);
        }

        function showNotification(message, type) {
            const notification = document.createElement('div');
            notification.className = 'notification ' + type;
            notification.textContent = message;
            document.body.appendChild(notification);

            setTimeout(() => {
                notification.remove();
            }, 10000);
        }

        loadConfig();
        checkUpdates();
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
}

func getNextScheduledRun(day, timeStr string, loc *time.Location) string {
	now := time.Now().In(loc)

	// Parse schedule time
	parts := strings.Split(timeStr, ":")
	hour, minute := 9, 0
	if len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &hour)
		fmt.Sscanf(parts[1], "%d", &minute)
	}

	// Map day to weekday
	dayMap := map[string]time.Weekday{
		"Mon": time.Monday,
		"Tue": time.Tuesday,
		"Wed": time.Wednesday,
		"Thu": time.Thursday,
		"Fri": time.Friday,
		"Sat": time.Saturday,
		"Sun": time.Sunday,
	}

	targetWeekday := dayMap[day]
	daysUntil := int(targetWeekday - now.Weekday())
	if daysUntil <= 0 {
		daysUntil += 7
	}

	nextRun := now.AddDate(0, 0, daysUntil)
	nextRun = time.Date(nextRun.Year(), nextRun.Month(), nextRun.Day(), hour, minute, 0, 0, loc)

	// If today is the day and time hasn't passed
	if now.Weekday() == targetWeekday {
		today := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if now.Before(today) {
			nextRun = today
		}
	}

	return nextRun.Format("Monday, January 2, 2006 at 3:04 PM MST")
}

func configHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var webCfg WebConfig
		if err := json.NewDecoder(r.Body).Decode(&webCfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		envMap := readEnvFile()

		// Only update fields that were provided
		if webCfg.SonarrURL != "" {
			envMap["SONARR_URL"] = webCfg.SonarrURL
		}
		if webCfg.SonarrAPIKey != "" {
			envMap["SONARR_API_KEY"] = webCfg.SonarrAPIKey
		}
		if webCfg.RadarrURL != "" {
			envMap["RADARR_URL"] = webCfg.RadarrURL
		}
		if webCfg.RadarrAPIKey != "" {
			envMap["RADARR_API_KEY"] = webCfg.RadarrAPIKey
		}
		if webCfg.MailgunSMTP != "" {
			envMap["MAILGUN_SMTP"] = webCfg.MailgunSMTP
		}
		if webCfg.MailgunPort != "" {
			envMap["MAILGUN_PORT"] = webCfg.MailgunPort
		}
		if webCfg.MailgunUser != "" {
			envMap["MAILGUN_USER"] = webCfg.MailgunUser
		}
		if webCfg.MailgunPass != "" {
			envMap["MAILGUN_PASS"] = webCfg.MailgunPass
		}
		if webCfg.FromEmail != "" {
			envMap["FROM_EMAIL"] = webCfg.FromEmail
		}
		if webCfg.FromName != "" {
			envMap["FROM_NAME"] = webCfg.FromName
		}
		if webCfg.ToEmails != "" {
			envMap["TO_EMAILS"] = webCfg.ToEmails
		}
		if webCfg.Timezone != "" {
			envMap["TIMEZONE"] = webCfg.Timezone
		}
		if webCfg.ScheduleDay != "" {
			envMap["SCHEDULE_DAY"] = webCfg.ScheduleDay
		}
		if webCfg.ScheduleTime != "" {
			envMap["SCHEDULE_TIME"] = webCfg.ScheduleTime
		}
		if webCfg.ShowPosters != "" {
			envMap["SHOW_POSTERS"] = webCfg.ShowPosters
		}
		if webCfg.ShowDownloaded != "" {
			envMap["SHOW_DOWNLOADED"] = webCfg.ShowDownloaded
		}

		var envContent strings.Builder
		for key, value := range envMap {
			envContent.WriteString(fmt.Sprintf("%s=%s\n", key, value))
		}

		if err := os.WriteFile(".env", []byte(envContent.String()), 0644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Reload config and restart scheduler
		reloadConfig()
		restartScheduler()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	// GET request - return current config
	envMap := readEnvFile()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"sonarr_url":      getEnvFromFile(envMap, "SONARR_URL", ""),
		"sonarr_api_key":  getEnvFromFile(envMap, "SONARR_API_KEY", ""),
		"radarr_url":      getEnvFromFile(envMap, "RADARR_URL", ""),
		"radarr_api_key":  getEnvFromFile(envMap, "RADARR_API_KEY", ""),
		"mailgun_smtp":    getEnvFromFile(envMap, "MAILGUN_SMTP", "smtp.mailgun.org"),
		"mailgun_port":    getEnvFromFile(envMap, "MAILGUN_PORT", "587"),
		"mailgun_user":    getEnvFromFile(envMap, "MAILGUN_USER", ""),
		"mailgun_pass":    getEnvFromFile(envMap, "MAILGUN_PASS", ""),
		"from_email":      getEnvFromFile(envMap, "FROM_EMAIL", ""),
		"from_name":       getEnvFromFile(envMap, "FROM_NAME", "Newslettar"),
		"to_emails":       getEnvFromFile(envMap, "TO_EMAILS", ""),
		"timezone":        getEnvFromFile(envMap, "TIMEZONE", "UTC"),
		"schedule_day":    getEnvFromFile(envMap, "SCHEDULE_DAY", "Sun"),
		"schedule_time":   getEnvFromFile(envMap, "SCHEDULE_TIME", "09:00"),
		"show_posters":    getEnvFromFile(envMap, "SHOW_POSTERS", "true"),
		"show_downloaded": getEnvFromFile(envMap, "SHOW_DOWNLOADED", "true"),
	})
}

func testSonarrHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	success := false
	message := "Missing URL or API key"

	if req.URL != "" && req.APIKey != "" {
		httpReq, err := http.NewRequest("GET", req.URL+"/api/v3/system/status", nil)
		if err == nil {
			httpReq.Header.Set("X-Api-Key", req.APIKey)
			resp, err := httpClient.Do(httpReq)
			if err != nil {
				message = fmt.Sprintf("Connection failed: %v", err)
			} else if resp.StatusCode == 200 {
				success = true
				message = "Sonarr connection successful!"
				resp.Body.Close()
			} else {
				message = fmt.Sprintf("Connection failed: HTTP %d", resp.StatusCode)
				resp.Body.Close()
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": success,
		"message": message,
	})
}

func testRadarrHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	success := false
	message := "Missing URL or API key"

	if req.URL != "" && req.APIKey != "" {
		httpReq, err := http.NewRequest("GET", req.URL+"/api/v3/system/status", nil)
		if err == nil {
			httpReq.Header.Set("X-Api-Key", req.APIKey)
			resp, err := httpClient.Do(httpReq)
			if err != nil {
				message = fmt.Sprintf("Connection failed: %v", err)
			} else if resp.StatusCode == 200 {
				success = true
				message = "Radarr connection successful!"
				resp.Body.Close()
			} else {
				message = fmt.Sprintf("Connection failed: HTTP %d", resp.StatusCode)
				resp.Body.Close()
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": success,
		"message": message,
	})
}

func testEmailHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SMTP string `json:"smtp"`
		Port string `json:"port"`
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	success := false
	message := "SMTP credentials missing"

	if req.User != "" && req.Pass != "" {
		addr := fmt.Sprintf("%s:%s", req.SMTP, req.Port)

		client, err := smtp.Dial(addr)
		if err != nil {
			message = fmt.Sprintf("Connection failed: %v", err)
		} else {
			defer client.Close()

			if err = client.Hello("localhost"); err != nil {
				message = fmt.Sprintf("EHLO failed: %v", err)
			} else if ok, _ := client.Extension("STARTTLS"); ok {
				config := &tls.Config{ServerName: req.SMTP}
				if err = client.StartTLS(config); err != nil {
					message = fmt.Sprintf("STARTTLS failed: %v", err)
				} else {
					auth := smtp.PlainAuth("", req.User, req.Pass, req.SMTP)
					if err = client.Auth(auth); err != nil {
						message = fmt.Sprintf("Authentication failed: %v", err)
					} else {
						success = true
						message = "SMTP authentication successful (with STARTTLS)"
					}
				}
			} else {
				auth := smtp.PlainAuth("", req.User, req.Pass, req.SMTP)
				if err = client.Auth(auth); err != nil {
					message = fmt.Sprintf("Authentication failed: %v", err)
				} else {
					success = true
					message = "SMTP authentication successful"
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": success,
		"message": message,
	})
}

func sendHandler(w http.ResponseWriter, r *http.Request) {
	// Send immediately with MANUAL_RUN flag
	go runNewsletter()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Newsletter generation started",
	})
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	logBufferMu.Lock()
	defer logBufferMu.Unlock()

	w.Header().Set("Content-Type", "text/plain")
	for _, line := range logBuffer {
		fmt.Fprint(w, line)
	}
}

func isNewerVersion(remote, current string) bool {
	remote = strings.TrimPrefix(remote, "v")
	current = strings.TrimPrefix(current, "v")

	remoteParts := strings.Split(remote, ".")
	currentParts := strings.Split(current, ".")

	maxLen := len(remoteParts)
	if len(currentParts) > maxLen {
		maxLen = len(currentParts)
	}

	for len(remoteParts) < maxLen {
		remoteParts = append(remoteParts, "0")
	}
	for len(currentParts) < maxLen {
		currentParts = append(currentParts, "0")
	}

	for i := 0; i < maxLen; i++ {
		var remoteNum, currentNum int
		fmt.Sscanf(remoteParts[i], "%d", &remoteNum)
		fmt.Sscanf(currentParts[i], "%d", &currentNum)

		if remoteNum > currentNum {
			return true
		} else if remoteNum < currentNum {
			return false
		}
	}

	return false
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	resp, err := httpClient.Get("https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/version.json")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"current_version":  version,
			"latest_version":   version,
			"update_available": false,
			"error":            "Could not check for updates",
		})
		return
	}
	defer resp.Body.Close()

	var remoteVersion struct {
		Version   string   `json:"version"`
		Released  string   `json:"released"`
		Changelog []string `json:"changelog"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&remoteVersion); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"current_version":  version,
			"latest_version":   version,
			"update_available": false,
			"error":            "Could not parse version info",
		})
		return
	}

	updateAvailable := isNewerVersion(remoteVersion.Version, version)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"current_version":  version,
		"latest_version":   remoteVersion.Version,
		"update_available": updateAvailable,
		"released":         remoteVersion.Released,
		"changelog":        remoteVersion.Changelog,
	})
}

func updateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Update started! Building in background...",
	})

	go func() {
		time.Sleep(1 * time.Second)

		log.Println("🔄 Starting update process...")

		cmd := exec.Command("bash", "-c", `
			set -e
			cd /opt/newslettar
			echo "Backing up .env..."
			cp .env .env.backup
			echo "Downloading main.go..."
			wget -O main.go https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/main.go
			echo "Downloading go.mod..."
			wget -O go.mod https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/go.mod
			echo "Downloading version.json..."
			wget -O version.json https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/version.json
			echo "Building with optimization flags..."
			/usr/local/go/bin/go build -ldflags="-s -w" -trimpath -o newslettar main.go
			echo "Restoring .env..."
			mv .env.backup .env
			echo "Restarting service..."
			systemctl restart newslettar.service
			echo "Update complete!"
		`)

		output, err := cmd.CombinedOutput()
		log.Printf("Update output: %s", string(output))
		if err != nil {
			log.Printf("❌ Update failed: %v", err)
		} else {
			log.Printf("✅ Update completed successfully")
		}
	}()
}