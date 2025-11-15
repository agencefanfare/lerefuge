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
//
//go:embed templates/*.html
var templateFS embed.FS

const version = "1.0.20"

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

// For Sonarr calendar response (nested series data)
type CalendarEpisode struct {
	SeasonNumber  int    `json:"seasonNumber"`
	EpisodeNumber int    `json:"episodeNumber"`
	Title         string `json:"title"`
	AirDate       string `json:"airDate"`
	Series        struct {
		Title  string `json:"title"`
		TvdbId int    `json:"tvdbId"`
		ImdbId string `json:"imdbId"`
		Images []struct {
			CoverType string `json:"coverType"`
			Url       string `json:"url"`       // Local URL if available
			RemoteUrl string `json:"remoteUrl"` // Fallback remote URL
		} `json:"images"`
	} `json:"series"`
}

// For Radarr calendar response (direct fields + images array)
type CalendarMovie struct {
	Title           string `json:"title"`
	Year            int    `json:"year"`
	PhysicalRelease string `json:"physicalRelease"` // Assuming you want physical release; adjust if needed (e.g., to "digitalRelease" or "inCinemas")
	ImdbId          string `json:"imdbId"`
	TmdbId          int    `json:"tmdbId"`
	Images          []struct {
		CoverType string `json:"coverType"`
		Url       string `json:"url"`       // Local URL if available
		RemoteUrl string `json:"remoteUrl"` // Fallback remote URL
	} `json:"images"`
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

	// Use a cancellable context for all fetches
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
		downloadedEpisodes, errSonarrHistory = fetchSonarrHistoryWithRetry(ctx, cfg, weekStart, 3)
		if errSonarrHistory != nil {
			log.Printf("⚠️  Sonarr history error: %v", errSonarrHistory)
		} else {
			log.Printf("✓ Found %d downloaded episodes", len(downloadedEpisodes))
		}
	}()

	go func() {
		defer wg.Done()
		log.Println("📺 Fetching Sonarr calendar...")
		upcomingEpisodes, errSonarrCalendar = fetchSonarrCalendarWithRetry(ctx, cfg, weekEnd, weekEnd.AddDate(0, 0, 7), 3)
		if errSonarrCalendar != nil {
			log.Printf("⚠️  Sonarr calendar error: %v", errSonarrCalendar)
		} else {
			log.Printf("✓ Found %d upcoming episodes", len(upcomingEpisodes))
		}
	}()

	go func() {
		defer wg.Done()
		log.Println("🎬 Fetching Radarr history...")
		downloadedMovies, errRadarrHistory = fetchRadarrHistoryWithRetry(ctx, cfg, weekStart, 3)
		if errRadarrHistory != nil {
			log.Printf("⚠️  Radarr history error: %v", errRadarrHistory)
		} else {
			log.Printf("✓ Found %d downloaded movies", len(downloadedMovies))
		}
	}()

	go func() {
		defer wg.Done()
		log.Println("🎬 Fetching Radarr calendar...")
		upcomingMovies, errRadarrCalendar = fetchRadarrCalendarWithRetry(ctx, cfg, weekEnd, weekEnd.AddDate(0, 0, 7), 3)
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

// Retry wrappers for API calls
func fetchSonarrHistoryWithRetry(ctx context.Context, cfg *Config, since time.Time, maxRetries int) ([]Episode, error) {
	var episodes []Episode
	var err error
	for i := 0; i < maxRetries; i++ {
		episodes, err = fetchSonarrHistory(ctx, cfg, since)
		if err == nil {
			return episodes, nil
		}
		if i < maxRetries-1 {
			wait := time.Duration(i+1) * time.Second
			log.Printf("⏳ Retrying Sonarr history in %v... (attempt %d/%d)", wait, i+2, maxRetries)
			time.Sleep(wait)
		}
	}
	return episodes, err
}

func fetchSonarrCalendarWithRetry(ctx context.Context, cfg *Config, start, end time.Time, maxRetries int) ([]Episode, error) {
	var episodes []Episode
	var err error
	for i := 0; i < maxRetries; i++ {
		episodes, err = fetchSonarrCalendar(ctx, cfg, start, end)
		if err == nil {
			return episodes, nil
		}
		if i < maxRetries-1 {
			wait := time.Duration(i+1) * time.Second
			log.Printf("⏳ Retrying Sonarr calendar in %v... (attempt %d/%d)", wait, i+2, maxRetries)
			time.Sleep(wait)
		}
	}
	return episodes, err
}

func fetchRadarrHistoryWithRetry(ctx context.Context, cfg *Config, since time.Time, maxRetries int) ([]Movie, error) {
	var movies []Movie
	var err error
	for i := 0; i < maxRetries; i++ {
		movies, err = fetchRadarrHistory(ctx, cfg, since)
		if err == nil {
			return movies, nil
		}
		if i < maxRetries-1 {
			wait := time.Duration(i+1) * time.Second
			log.Printf("⏳ Retrying Radarr history in %v... (attempt %d/%d)", wait, i+2, maxRetries)
			time.Sleep(wait)
		}
	}
	return movies, err
}

func fetchRadarrCalendarWithRetry(ctx context.Context, cfg *Config, start, end time.Time, maxRetries int) ([]Movie, error) {
	var movies []Movie
	var err error
	for i := 0; i < maxRetries; i++ {
		movies, err = fetchRadarrCalendar(ctx, cfg, start, end)
		if err == nil {
			return movies, nil
		}
		if i < maxRetries-1 {
			wait := time.Duration(i+1) * time.Second
			log.Printf("⏳ Retrying Radarr calendar in %v... (attempt %d/%d)", wait, i+2, maxRetries)
			time.Sleep(wait)
		}
	}
	return movies, err
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

// Updated fetch functions to accept context and use RequestWithContext
func fetchSonarrHistory(ctx context.Context, cfg *Config, since time.Time) ([]Episode, error) {
	if cfg.SonarrURL == "" || cfg.SonarrAPIKey == "" {
		return nil, fmt.Errorf("Sonarr not configured")
	}

	url := fmt.Sprintf("%s/api/v3/history?pageSize=1000&sortKey=date&sortDirection=descending&includeEpisode=true&includeSeries=true", cfg.SonarrURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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

func fetchSonarrCalendar(ctx context.Context, cfg *Config, start, end time.Time) ([]Episode, error) {
	url := fmt.Sprintf("%s/api/v3/calendar?unmonitored=true&includeSeries=true&includeEpisodeImages=true&start=%s&end=%s",
		cfg.SonarrURL, start.Format("2006-01-02"), end.Format("2006-01-02"))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", cfg.SonarrAPIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d", resp.StatusCode)
	}

	// Stream-decode JSON to save memory
	var calendar []CalendarEpisode
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&calendar); err != nil {
		return nil, err
	}

	// Map to Episode struct
	var episodes []Episode
	for _, entry := range calendar {
		posterURL := ""
		for _, img := range entry.Series.Images {
			if img.CoverType == "poster" {
				if img.Url != "" {
					posterURL = img.Url
				} else if img.RemoteUrl != "" {
					posterURL = img.RemoteUrl
				}
				break
			}
		}

		ep := Episode{
			SeriesTitle: entry.Series.Title,
			SeasonNum:   entry.SeasonNumber,
			EpisodeNum:  entry.EpisodeNumber,
			Title:       entry.Title,
			AirDate:     entry.AirDate,
			PosterURL:   posterURL,
			IMDBID:      entry.Series.ImdbId,
			TvdbID:      entry.Series.TvdbId,
		}

		if ep.AirDate != "" {
			airDate, _ := time.Parse("2006-01-02", ep.AirDate)
			ep.AirDate = airDate.Format("2006-01-02")
		}

		episodes = append(episodes, ep)
	}

	return episodes, nil
}

func fetchRadarrHistory(ctx context.Context, cfg *Config, since time.Time) ([]Movie, error) {
	if cfg.RadarrURL == "" || cfg.RadarrAPIKey == "" {
		return nil, fmt.Errorf("Radarr not configured")
	}

	url := fmt.Sprintf("%s/api/v3/history?pageSize=1000&sortKey=date&sortDirection=descending&includeMovie=true", cfg.RadarrURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
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

func fetchRadarrCalendar(ctx context.Context, cfg *Config, start, end time.Time) ([]Movie, error) {
	url := fmt.Sprintf("%s/api/v3/calendar?unmonitored=true&includeMovie=true&start=%s&end=%s",
		cfg.RadarrURL, start.Format("2006-01-02"), end.Format("2006-01-02"))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", cfg.RadarrAPIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d", resp.StatusCode)
	}

	// Stream-decode JSON to save memory
	var calendar []CalendarMovie
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&calendar); err != nil {
		return nil, err
	}

	// Map to Movie struct
	var movies []Movie
	for _, entry := range calendar {
		posterURL := ""
		for _, img := range entry.Images {
			if img.CoverType == "poster" {
				if img.Url != "" {
					posterURL = img.Url
				} else if img.RemoteUrl != "" {
					posterURL = img.RemoteUrl
				}
				break
			}
		}

		mv := Movie{
			Title:       entry.Title,
			Year:        entry.Year,
			ReleaseDate: entry.PhysicalRelease,
			PosterURL:   posterURL,
			IMDBID:      entry.ImdbId,
			TmdbID:      entry.TmdbId,
		}

		if mv.ReleaseDate != "" {
			releaseDate, _ := time.Parse("2006-01-02", mv.ReleaseDate)
			mv.ReleaseDate = releaseDate.Format("2006-01-02")
		}

		movies = append(movies, mv)
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

		// Check for duplicate episodes (same season and episode number)
		isDuplicate := false
		for _, existingEp := range group.Episodes {
			if existingEp.SeasonNum == ep.SeasonNum && existingEp.EpisodeNum == ep.EpisodeNum {
				isDuplicate = true
				break
			}
		}

		// Only add if not a duplicate
		if !isDuplicate {
			group.Episodes = append(group.Episodes, ep)
		}
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
	http.HandleFunc("/api/preview", previewHandler)
	http.HandleFunc("/api/timezone-info", timezoneInfoHandler)

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
            max-width: 1200px;
            margin: 0 auto;
            padding: 20px;
        }
        
        /* Responsive design */
        @media (max-width: 768px) {
            .container { padding: 10px; }
            .header h1 { font-size: 1.8em; }
            .tabs { flex-wrap: wrap; }
            .tab { flex: 1 1 45%; font-size: 12px; padding: 10px; }
            .form-group { margin-bottom: 15px; }
            .action-buttons { flex-direction: column; }
            .action-buttons .btn { margin-bottom: 10px; }
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
        .tab:focus { outline: 2px solid #667eea; outline-offset: 2px; }
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
        .form-group input.error, .form-group select.error {
            border-color: #eb3349;
        }
        .form-group input.success, .form-group select.success {
            border-color: #38ef7d;
        }
        .error-message {
            color: #eb3349;
            font-size: 0.85em;
            margin-top: 5px;
            display: none;
        }
        .error-message.show { display: block; }
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
            position: relative;
        }
        .btn:hover { transform: translateY(-2px); opacity: 0.9; }
        .btn:active { transform: translateY(0); }
        .btn:disabled {
            opacity: 0.5;
            cursor: not-allowed;
            transform: none;
        }
        .btn:focus { outline: 2px solid #667eea; outline-offset: 2px; }
        .btn-secondary {
            background: #2a3444;
        }
        .btn-success {
            background: linear-gradient(135deg, #11998e 0%, #38ef7d 100%);
        }
        .btn-danger {
            background: linear-gradient(135deg, #eb3349 0%, #f45c43 100%);
        }
        .btn.loading::after {
            content: "";
            position: absolute;
            width: 16px;
            height: 16px;
            top: 50%;
            left: 50%;
            margin-left: -8px;
            margin-top: -8px;
            border: 2px solid #ffffff40;
            border-top-color: #fff;
            border-radius: 50%;
            animation: spin 0.6s linear infinite;
        }
        .btn.loading span { opacity: 0; }
        @keyframes spin {
            to { transform: rotate(360deg); }
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
        .timezone-info {
            background: #252f3f;
            padding: 15px;
            border-radius: 8px;
            margin-top: 10px;
            font-size: 0.9em;
        }
        .timezone-info strong {
            color: #667eea;
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
        
        /* Preview Modal */
        .modal {
            display: none;
            position: fixed;
            z-index: 2000;
            left: 0;
            top: 0;
            width: 100%;
            height: 100%;
            background-color: rgba(0,0,0,0.8);
            animation: fadeIn 0.3s;
        }
        .modal.show { display: flex; align-items: center; justify-content: center; }
        .modal-content {
            background: #1a2332;
            width: 90%;
            max-width: 900px;
            max-height: 90vh;
            border-radius: 12px;
            overflow: hidden;
            animation: slideUp 0.3s;
        }
        .modal-header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            padding: 20px;
            display: flex;
            justify-content: space-between;
            align-items: center;
        }
        .modal-header h2 {
            margin: 0;
            color: white;
        }
        .modal-close {
            background: transparent;
            border: none;
            color: white;
            font-size: 28px;
            cursor: pointer;
            padding: 0;
            width: 30px;
            height: 30px;
            line-height: 1;
        }
        .modal-close:hover { opacity: 0.7; }
        .modal-close:focus { outline: 2px solid white; outline-offset: 2px; }
        .modal-body {
            padding: 20px;
            max-height: calc(90vh - 140px);
            overflow-y: auto;
        }
        .modal-body iframe {
            width: 100%;
            height: 600px;
            border: 2px solid #2a3444;
            border-radius: 8px;
            background: white;
        }
        @keyframes fadeIn {
            from { opacity: 0; }
            to { opacity: 1; }
        }
        @keyframes slideUp {
            from { transform: translateY(50px); opacity: 0; }
            to { transform: translateY(0); opacity: 1; }
        }
        
        .action-buttons {
            display: flex;
            gap: 10px;
            margin-top: 20px;
        }
        .action-buttons .btn {
            flex: 1;
        }
        
        /* Loading overlay */
        .loading-overlay {
            display: none;
            position: fixed;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            background: rgba(0,0,0,0.7);
            z-index: 1500;
            align-items: center;
            justify-content: center;
        }
        .loading-overlay.show { display: flex; }
        .loading-spinner {
            width: 60px;
            height: 60px;
            border: 5px solid rgba(255,255,255,0.3);
            border-top-color: #667eea;
            border-radius: 50%;
            animation: spin 0.8s linear infinite;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>📺 Newslettar</h1>
            <p class="version">Version ` + version + ` • Enhanced UI • Timezone-Aware</p>
        </div>

        <div class="tabs" role="tablist">
            <button class="tab active" role="tab" aria-selected="true" aria-controls="config-tab" onclick="showTab('config')">⚙️ Configuration</button>
            <button class="tab" role="tab" aria-selected="false" aria-controls="template-tab" onclick="showTab('template')">📝 Email Template</button>
            <button class="tab" role="tab" aria-selected="false" aria-controls="logs-tab" onclick="showTab('logs')">📋 Logs</button>
            <button class="tab" role="tab" aria-selected="false" aria-controls="update-tab" onclick="showTab('update')">🔄 Update</button>
        </div>

        <div id="config-tab" class="tab-content active" role="tabpanel">
            <div class="info-banner">
                <p><strong>⏰ Next Scheduled Send:</strong> ` + nextRun + `</p>
                <p><strong>🌍 Timezone:</strong> <span id="current-timezone">` + cfg.Timezone + `</span></p>
                <p style="margin-top: 10px; font-size: 0.9em; opacity: 0.8;">
                    ℹ️ Scheduler runs internally (no systemd timer needed). Changes apply immediately.
                </p>
            </div>

            <form id="config-form">
                <h3 style="margin-bottom: 15px; color: #667eea;">Schedule Settings</h3>
                
                <div class="form-group">
                    <label for="timezone">Timezone</label>
                    <select name="timezone" id="timezone" aria-label="Select timezone">
                        <option value="UTC">UTC (GMT+0)</option>
                        <option value="America/New_York">Eastern Time (GMT-5/-4)</option>
                        <option value="America/Chicago">Central Time (GMT-6/-5)</option>
                        <option value="America/Denver">Mountain Time (GMT-7/-6)</option>
                        <option value="America/Los_Angeles">Pacific Time (GMT-8/-7)</option>
                        <option value="America/Toronto">Toronto (GMT-5/-4)</option>
                        <option value="America/Vancouver">Vancouver (GMT-8/-7)</option>
                        <option value="America/Montreal">Montreal (GMT-5/-4)</option>
                        <option value="Europe/London">London (GMT+0/+1)</option>
                        <option value="Europe/Paris">Paris (GMT+1/+2)</option>
                        <option value="Europe/Berlin">Berlin (GMT+1/+2)</option>
                        <option value="Asia/Tokyo">Tokyo (GMT+9)</option>
                        <option value="Asia/Shanghai">Shanghai (GMT+8)</option>
                        <option value="Australia/Sydney">Sydney (GMT+10/+11)</option>
                    </select>
                    <div class="timezone-info" id="timezone-info"></div>
                </div>

                <div class="form-group">
                    <label for="schedule_day">Day of Week</label>
                    <select name="schedule_day" id="schedule_day" aria-label="Select day of week">
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
                    <label for="schedule_time">Time (24-hour format, HH:MM)</label>
                    <input type="time" name="schedule_time" id="schedule_time" required aria-label="Select time">
                    <div class="error-message" id="time-error">Please enter a valid time (HH:MM)</div>
                </div>

                <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

                <h3 style="margin-bottom: 15px; color: #667eea;">Sonarr Settings</h3>
                <div class="form-group">
                    <label for="sonarr_url">Sonarr URL</label>
                    <input type="url" name="sonarr_url" id="sonarr_url" placeholder="http://localhost:8989" aria-label="Sonarr URL">
                    <div class="error-message" id="sonarr-url-error">Please enter a valid URL</div>
                </div>
                <div class="form-group">
                    <label for="sonarr_api_key">Sonarr API Key</label>
                    <input type="text" name="sonarr_api_key" id="sonarr_api_key" placeholder="Your Sonarr API key" aria-label="Sonarr API Key">
                </div>
                <button type="button" class="btn btn-secondary" onclick="testConnection('sonarr')" aria-label="Test Sonarr connection">
                    <span>Test Sonarr</span>
                </button>

                <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

                <h3 style="margin-bottom: 15px; color: #667eea;">Radarr Settings</h3>
                <div class="form-group">
                    <label for="radarr_url">Radarr URL</label>
                    <input type="url" name="radarr_url" id="radarr_url" placeholder="http://localhost:7878" aria-label="Radarr URL">
                    <div class="error-message" id="radarr-url-error">Please enter a valid URL</div>
                </div>
                <div class="form-group">
                    <label for="radarr_api_key">Radarr API Key</label>
                    <input type="text" name="radarr_api_key" id="radarr_api_key" placeholder="Your Radarr API key" aria-label="Radarr API Key">
                </div>
                <button type="button" class="btn btn-secondary" onclick="testConnection('radarr')" aria-label="Test Radarr connection">
                    <span>Test Radarr</span>
                </button>

                <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

                <h3 style="margin-bottom: 15px; color: #667eea;">Email Settings</h3>
                <div class="form-group">
                    <label for="mailgun_smtp">SMTP Server</label>
                    <input type="text" name="mailgun_smtp" id="mailgun_smtp" placeholder="smtp.mailgun.org" aria-label="SMTP Server">
                </div>
                <div class="form-group">
                    <label for="mailgun_port">SMTP Port</label>
                    <input type="number" name="mailgun_port" id="mailgun_port" placeholder="587" aria-label="SMTP Port">
                </div>
                <div class="form-group">
                    <label for="mailgun_user">SMTP Username</label>
                    <input type="text" name="mailgun_user" id="mailgun_user" placeholder="postmaster@yourdomain.com" aria-label="SMTP Username">
                </div>
                <div class="form-group">
                    <label for="mailgun_pass">SMTP Password</label>
                    <input type="password" name="mailgun_pass" id="mailgun_pass" placeholder="Your SMTP password" aria-label="SMTP Password">
                </div>
                <div class="form-group">
                    <label for="from_name">From Name</label>
                    <input type="text" name="from_name" id="from_name" placeholder="Newslettar" aria-label="From Name">
                </div>
                <div class="form-group">
                    <label for="from_email">From Email</label>
                    <input type="email" name="from_email" id="from_email" placeholder="newsletter@yourdomain.com" aria-label="From Email">
                    <div class="error-message" id="from-email-error">Please enter a valid email address</div>
                </div>
                <div class="form-group">
                    <label for="to_emails">To Emails (comma-separated)</label>
                    <input type="text" name="to_emails" id="to_emails" placeholder="user@example.com, user2@example.com" aria-label="To Emails">
                    <div class="error-message" id="to-emails-error">Please enter valid email addresses</div>
                </div>
                <button type="button" class="btn btn-secondary" onclick="testConnection('email')" aria-label="Test email authentication">
                    <span>Test Email Auth</span>
                </button>

                <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

                <button type="submit" class="btn" aria-label="Save configuration">
                    <span>💾 Save Configuration</span>
                </button>
            </form>
        </div>

        <div id="template-tab" class="tab-content" role="tabpanel">
            <h3 style="margin-bottom: 20px;">Email Template Options</h3>
            
            <div class="template-option">
                <div>
                    <strong>Show Movie/Series Posters</strong>
                    <p style="font-size: 0.9em; color: #8899aa; margin-top: 5px;">
                        Display poster images in the newsletter
                    </p>
                </div>
                <label class="toggle-switch">
                    <input type="checkbox" id="show-posters" onchange="saveTemplateSettings()" aria-label="Toggle poster display">
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
                    <input type="checkbox" id="show-downloaded" onchange="saveTemplateSettings()" aria-label="Toggle downloaded section">
                    <span class="toggle-slider"></span>
                </label>
            </div>

            <p style="margin-top: 20px; color: #8899aa; font-size: 0.9em;">
                ℹ️ Changes are saved automatically when you toggle switches.
            </p>

            <hr style="margin: 30px 0; border: none; border-top: 2px solid #2a3444;">

            <h3 style="margin-bottom: 20px;">Actions</h3>
            
            <div class="action-buttons">
                <button class="btn btn-secondary" onclick="previewNewsletter()" aria-label="Preview newsletter">
                    <span>👁️ Preview Newsletter</span>
                </button>
                <button class="btn btn-success" onclick="sendNow()" aria-label="Send newsletter now">
                    <span>📧 Send Newsletter Now</span>
                </button>
            </div>
            
            <p style="margin-top: 15px; color: #8899aa; font-size: 0.9em;">
                Preview generates the email based on current settings without sending. Send Now will generate and send immediately.
            </p>
        </div>

        <div id="logs-tab" class="tab-content" role="tabpanel">
            <h3 style="margin-bottom: 15px;">📋 Newsletter Logs</h3>
            <button class="btn btn-secondary" onclick="loadLogs()" style="margin-bottom: 15px;" aria-label="Refresh logs">
                <span>🔄 Refresh Logs</span>
            </button>
            <div class="logs-container" id="logs" role="log" aria-live="polite"></div>
        </div>

        <div id="update-tab" class="tab-content" role="tabpanel">
            <h3 style="margin-bottom: 20px;">🔄 Update Newslettar</h3>
            
            <div id="version-info" aria-live="polite">
                <p>Checking for updates...</p>
            </div>

            <button class="btn" onclick="checkUpdates()" style="margin-right: 10px;" aria-label="Check for updates">
                <span>🔍 Check for Updates</span>
            </button>
            <button class="btn btn-success" id="update-btn" onclick="performUpdate()" style="display: none;" aria-label="Update now">
                <span>⬇️ Update Now</span>
            </button>
        </div>
    </div>

    <!-- Preview Modal -->
    <div id="preview-modal" class="modal" role="dialog" aria-labelledby="preview-title" aria-modal="true">
        <div class="modal-content">
            <div class="modal-header">
                <h2 id="preview-title">Email Preview</h2>
                <button class="modal-close" onclick="closePreview()" aria-label="Close preview">&times;</button>
            </div>
            <div class="modal-body">
                <iframe id="preview-frame" title="Email preview"></iframe>
            </div>
        </div>
    </div>

    <!-- Loading Overlay -->
    <div id="loading-overlay" class="loading-overlay" role="status" aria-live="polite">
        <div class="loading-spinner"></div>
    </div>

    <script>
        let logsInterval;

        // Keyboard navigation
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                closePreview();
            }
        });

        function showTab(tabName) {
            document.querySelectorAll('.tab').forEach(t => {
                t.classList.remove('active');
                t.setAttribute('aria-selected', 'false');
            });
            document.querySelectorAll('.tab-content').forEach(t => t.classList.remove('active'));
            
            event.target.classList.add('active');
            event.target.setAttribute('aria-selected', 'true');
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

        // Real-time validation
        function validateURL(input) {
            const value = input.value.trim();
            if (!value) return true;
            
            try {
                new URL(value);
                return true;
            } catch {
                return false;
            }
        }

        function validateEmail(email) {
            return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email);
        }

        function validateEmails(input) {
            const value = input.value.trim();
            if (!value) return true;
            
            const emails = value.split(',').map(e => e.trim());
            return emails.every(email => validateEmail(email));
        }

        // Add validation listeners
        document.addEventListener('DOMContentLoaded', () => {
            const sonarrUrl = document.getElementById('sonarr_url');
            const radarrUrl = document.getElementById('radarr_url');
            const fromEmail = document.getElementById('from_email');
            const toEmails = document.getElementById('to_emails');

            sonarrUrl.addEventListener('blur', function() {
                if (this.value && !validateURL(this)) {
                    this.classList.add('error');
                    this.classList.remove('success');
                    document.getElementById('sonarr-url-error').classList.add('show');
                } else if (this.value) {
                    this.classList.remove('error');
                    this.classList.add('success');
                    document.getElementById('sonarr-url-error').classList.remove('show');
                }
            });

            radarrUrl.addEventListener('blur', function() {
                if (this.value && !validateURL(this)) {
                    this.classList.add('error');
                    this.classList.remove('success');
                    document.getElementById('radarr-url-error').classList.add('show');
                } else if (this.value) {
                    this.classList.remove('error');
                    this.classList.add('success');
                    document.getElementById('radarr-url-error').classList.remove('show');
                }
            });

            fromEmail.addEventListener('blur', function() {
                if (this.value && !validateEmail(this.value)) {
                    this.classList.add('error');
                    this.classList.remove('success');
                    document.getElementById('from-email-error').classList.add('show');
                } else if (this.value) {
                    this.classList.remove('error');
                    this.classList.add('success');
                    document.getElementById('from-email-error').classList.remove('show');
                }
            });

            toEmails.addEventListener('blur', function() {
                if (this.value && !validateEmails(this)) {
                    this.classList.add('error');
                    this.classList.remove('success');
                    document.getElementById('to-emails-error').classList.add('show');
                } else if (this.value) {
                    this.classList.remove('error');
                    this.classList.add('success');
                    document.getElementById('to-emails-error').classList.remove('show');
                }
            });

            // Update timezone info on change
            document.getElementById('timezone').addEventListener('change', updateTimezoneInfo);
        });

        async function updateTimezoneInfo() {
            const tz = document.getElementById('timezone').value;
            try {
                const resp = await fetch('/api/timezone-info?tz=' + encodeURIComponent(tz));
                const data = await resp.json();
                
                document.getElementById('timezone-info').innerHTML = 
                    '<strong>Current time:</strong> ' + data.current_time + 
                    ' <strong>•</strong> Offset: ' + data.offset;
            } catch (error) {
                console.error('Failed to fetch timezone info:', error);
            }
        }

        async function loadConfig() {
            showLoading();
            try {
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
                
                await updateTimezoneInfo();
            } catch (error) {
                showNotification('Failed to load configuration: ' + error.message, 'error');
            } finally {
                hideLoading();
            }
        }

        document.getElementById('config-form').addEventListener('submit', async (e) => {
            e.preventDefault();
            
            const formData = new FormData(e.target);
            const data = Object.fromEntries(formData);
            
            const submitBtn = e.target.querySelector('button[type="submit"]');
            submitBtn.classList.add('loading');
            submitBtn.disabled = true;
            
            try {
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
            } catch (error) {
                showNotification('Network error: ' + error.message, 'error');
            } finally {
                submitBtn.classList.remove('loading');
                submitBtn.disabled = false;
            }
        });

        async function testConnection(type) {
            const form = document.getElementById('config-form');
            const formData = new FormData(form);
            const data = Object.fromEntries(formData);
            
            const button = event.target.closest('button');
            button.classList.add('loading');
            button.disabled = true;

            let endpoint, payload;

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

            try {
                const resp = await fetch(endpoint, {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify(payload)
                });

                const result = await resp.json();
                showNotification(result.message, result.success ? 'success' : 'error');
            } catch (error) {
                showNotification('Connection test failed: ' + error.message, 'error');
            } finally {
                button.classList.remove('loading');
                button.disabled = false;
            }
        }

        async function previewNewsletter() {
            const button = event.target.closest('button');
            button.classList.add('loading');
            button.disabled = true;
            
            showLoading();

            try {
                const resp = await fetch('/api/preview', { method: 'POST' });
                const data = await resp.json();

                if (data.success) {
                    const iframe = document.getElementById('preview-frame');
                    iframe.srcdoc = data.html;
                    document.getElementById('preview-modal').classList.add('show');
                } else {
                    showNotification(data.error || 'Failed to generate preview', 'error');
                }
            } catch (error) {
                showNotification('Preview failed: ' + error.message, 'error');
            } finally {
                button.classList.remove('loading');
                button.disabled = false;
                hideLoading();
            }
        }

        function closePreview() {
            document.getElementById('preview-modal').classList.remove('show');
        }

        async function sendNow() {
            if (!confirm('Send newsletter now?')) return;
            
            const button = event.target.closest('button');
            button.classList.add('loading');
            button.disabled = true;
            
            showNotification('Sending newsletter...', 'success');
            
            try {
                const resp = await fetch('/api/send', { method: 'POST' });
                const data = await resp.json();

                if (data.success) {
                    showNotification('Newsletter sent successfully!', 'success');
                } else {
                    showNotification('Failed to send newsletter', 'error');
                }
            } catch (error) {
                showNotification('Send failed: ' + error.message, 'error');
            } finally {
                button.classList.remove('loading');
                button.disabled = false;
            }
        }

        async function loadLogs() {
            try {
                const resp = await fetch('/api/logs');
                const logs = await resp.text();
                document.getElementById('logs').textContent = logs;
                document.getElementById('logs').scrollTop = document.getElementById('logs').scrollHeight;
            } catch (error) {
                console.error('Failed to load logs:', error);
            }
        }

        async function saveTemplateSettings() {
            const showPosters = document.getElementById('show-posters').checked;
            const showDownloaded = document.getElementById('show-downloaded').checked;

            try {
                await fetch('/api/config', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({
                        show_posters: showPosters ? 'true' : 'false',
                        show_downloaded: showDownloaded ? 'true' : 'false'
                    })
                });

                showNotification('Template settings saved', 'success');
            } catch (error) {
                showNotification('Failed to save settings: ' + error.message, 'error');
            }
        }

        async function checkUpdates() {
            const button = event.target;
            button.classList.add('loading');
            button.disabled = true;

            try {
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
            } catch (error) {
                showNotification('Failed to check updates: ' + error.message, 'error');
            } finally {
                button.classList.remove('loading');
                button.disabled = false;
            }
        }

        async function performUpdate() {
            if (!confirm('Update Newslettar? The page will reload in 20 seconds.')) return;

            const button = document.getElementById('update-btn');
            button.classList.add('loading');
            button.disabled = true;

            showNotification('Starting update... Page will reload in 20 seconds', 'success');
            
            try {
                await fetch('/api/update', { method: 'POST' });

                setTimeout(() => {
                    location.reload();
                }, 20000);
            } catch (error) {
                showNotification('Update failed: ' + error.message, 'error');
                button.classList.remove('loading');
                button.disabled = false;
            }
        }

        function showNotification(message, type) {
            const notification = document.createElement('div');
            notification.className = 'notification ' + type;
            notification.textContent = message;
            notification.setAttribute('role', 'alert');
            document.body.appendChild(notification);

            setTimeout(() => {
                notification.remove();
            }, 10000);
        }

        function showLoading() {
            document.getElementById('loading-overlay').classList.add('show');
        }

        function hideLoading() {
            document.getElementById('loading-overlay').classList.remove('show');
        }

        // Load config on page load
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

func timezoneInfoHandler(w http.ResponseWriter, r *http.Request) {
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "UTC"
	}

	loc := getTimezone(tz)
	now := time.Now().In(loc)

	_, offset := now.Zone()
	hours := offset / 3600
	minutes := (offset % 3600) / 60

	offsetStr := fmt.Sprintf("GMT%+d", hours)
	if minutes != 0 {
		offsetStr = fmt.Sprintf("GMT%+d:%02d", hours, minutes)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"current_time": now.Format("Monday, January 2, 2006 3:04 PM"),
		"offset":       offsetStr,
	})
}

// Preview handler for UI
func previewHandler(w http.ResponseWriter, r *http.Request) {
	cfg := getConfig()
	loc := getTimezone(cfg.Timezone)
	now := time.Now().In(loc)

	weekStart := now.AddDate(0, 0, -7)
	weekEnd := now

	// Parallel API calls with context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var downloadedEpisodes, upcomingEpisodes []Episode
	var downloadedMovies, upcomingMovies []Movie

	wg.Add(4)

	go func() {
		defer wg.Done()
		downloadedEpisodes, _ = fetchSonarrHistoryWithRetry(ctx, cfg, weekStart, 2)
	}()

	go func() {
		defer wg.Done()
		upcomingEpisodes, _ = fetchSonarrCalendarWithRetry(ctx, cfg, weekEnd, weekEnd.AddDate(0, 0, 7), 2)
	}()

	go func() {
		defer wg.Done()
		downloadedMovies, _ = fetchRadarrHistoryWithRetry(ctx, cfg, weekStart, 2)
	}()

	go func() {
		defer wg.Done()
		upcomingMovies, _ = fetchRadarrCalendarWithRetry(ctx, cfg, weekEnd, weekEnd.AddDate(0, 0, 7), 2)
	}()

	wg.Wait()

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

	html, err := generateNewsletterHTML(data, cfg.ShowPosters, cfg.ShowDownloaded)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to generate preview: %v", err),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"html":    html,
	})
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
