package main

import (
	"bytes"
	"crypto/tls"
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
	"sort"
	"strings"
	"time"
)

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
}

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
	ScheduleDay    string `json:"schedule_day"`
	ScheduleTime   string `json:"schedule_time"`
	ShowPosters    string `json:"show_posters"`
	ShowDownloaded string `json:"show_downloaded"`
}

const version = "1.0.16"

func main() {
	webMode := flag.Bool("web", false, "Run in web UI mode")
	flag.Parse()

	if *webMode {
		startWebServer()
	} else {
		runNewsletter()
	}
}

// Newsletter sending logic
func runNewsletter() {
	now := time.Now()
	
	// Check if this is being run from the web UI manually or from the scheduled timer
	// We can tell by checking if we're within 5 minutes of a scheduled time
	isManualRun := os.Getenv("MANUAL_RUN") == "true"
	
	if !isManualRun {
		// Read schedule from timer to check if we should run now
		scheduleDay, scheduleTime := getScheduleFromTimer()
		
		// Check if current time matches schedule (within 5 minutes)
		if !isScheduledTime(now, scheduleDay, scheduleTime) {
			log.Printf("⏸️  Not scheduled time. Current: %s, Scheduled: %s %s. Skipping automatic send.", 
				now.Format("Mon 15:04"), scheduleDay, scheduleTime)
			return
		}
	}
	
	// Create lock file to prevent duplicate sends
	lockFile := "/tmp/newslettar.lock"
	
	// Check if lock file exists and is recent (less than 1 hour old)
	if info, err := os.Stat(lockFile); err == nil {
		if time.Since(info.ModTime()) < 1*time.Hour {
			log.Println("⏸️  Newsletter already sent recently (lock file exists). Skipping to prevent duplicates.")
			return
		}
		// Lock file is old, remove it
		os.Remove(lockFile)
	}
	
	// Create lock file
	if err := os.WriteFile(lockFile, []byte(time.Now().Format(time.RFC3339)), 0644); err != nil {
		log.Printf("⚠️  Warning: Could not create lock file: %v", err)
	}
	defer func() {
		// Keep lock file for 1 hour to prevent duplicates
		time.AfterFunc(1*time.Hour, func() {
			os.Remove(lockFile)
		})
	}()

	cfg := loadConfig()

	log.Println("🚀 Starting Newslettar - Weekly newsletter generation...")
	log.Printf("Config: Sonarr=%s, Radarr=%s", cfg.SonarrURL, cfg.RadarrURL)

	weekStart := now.AddDate(0, 0, -7)
	weekEnd := now

	log.Printf("📅 Week range: %s to %s", weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))

	// Fetch data
	log.Println("📺 Fetching Sonarr data...")
	downloadedEpisodes, err := fetchSonarrHistory(cfg, weekStart)
	if err != nil {
		log.Printf("⚠️  Warning: Sonarr history error: %v", err)
	}
	log.Printf("   Found %d downloaded episodes", len(downloadedEpisodes))

	upcomingEpisodes, err := fetchSonarrCalendar(cfg, weekEnd, weekEnd.AddDate(0, 0, 7))
	if err != nil {
		log.Printf("⚠️  Warning: Sonarr calendar error: %v", err)
	}
	log.Printf("   Found %d upcoming episodes", len(upcomingEpisodes))

	log.Println("🎬 Fetching Radarr data...")
	downloadedMovies, err := fetchRadarrHistory(cfg, weekStart)
	if err != nil {
		log.Printf("⚠️  Warning: Radarr history error: %v", err)
	}
	log.Printf("   Found %d downloaded movies", len(downloadedMovies))

	upcomingMovies, err := fetchRadarrCalendar(cfg, weekEnd, weekEnd.AddDate(0, 0, 7))
	if err != nil {
		log.Printf("⚠️  Warning: Radarr calendar error: %v", err)
	}
	log.Printf("   Found %d upcoming movies", len(upcomingMovies))

	// Sort movies by release date chronologically (oldest first)
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
	html, err := generateNewsletterHTML(data)
	if err != nil {
		log.Fatalf("❌ Failed to generate HTML: %v", err)
	}

	subject := fmt.Sprintf("📺 Your Weekly Newsletter - %s", weekEnd.Format("January 2, 2006"))

	log.Println("📧 Sending emails...")
	if err := sendEmail(cfg, subject, html); err != nil {
		log.Fatalf("❌ Failed to send email: %v", err)
	}

	log.Println("✅ Newsletter sent successfully!")
}

// Helper function to read schedule from systemd timer
func getScheduleFromTimer() (string, string) {
	scheduleDay := "Sun"
	scheduleTime := "09:00"
	
	cmd := exec.Command("systemctl", "cat", "newslettar-send.timer")
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "OnCalendar=") {
				// Parse "OnCalendar=Sun *-*-* 09:00:00"
				parts := strings.Fields(strings.TrimPrefix(line, "OnCalendar="))
				if len(parts) >= 3 {
					scheduleDay = parts[0]
					timeStr := parts[2]
					if len(timeStr) >= 5 {
						scheduleTime = timeStr[:5]
					}
				}
			}
		}
	}
	
	return scheduleDay, scheduleTime
}

// Helper function to check if current time matches scheduled time (within 5 minutes)
func isScheduledTime(now time.Time, scheduleDay string, scheduleTime string) bool {
	// Map short day names to Go weekday
	dayMap := map[string]time.Weekday{
		"Mon": time.Monday,
		"Tue": time.Tuesday,
		"Wed": time.Wednesday,
		"Thu": time.Thursday,
		"Fri": time.Friday,
		"Sat": time.Saturday,
		"Sun": time.Sunday,
	}
	
	expectedWeekday, ok := dayMap[scheduleDay]
	if !ok {
		return false
	}
	
	// Check if today is the scheduled day
	if now.Weekday() != expectedWeekday {
		return false
	}
	
	// Parse scheduled time
	scheduledHour := 0
	scheduledMinute := 0
	fmt.Sscanf(scheduleTime, "%d:%d", &scheduledHour, &scheduledMinute)
	
	// Create scheduled time for today
	scheduledTime := time.Date(now.Year(), now.Month(), now.Day(), scheduledHour, scheduledMinute, 0, 0, now.Location())
	
	// Check if we're within 5 minutes of scheduled time
	diff := now.Sub(scheduledTime)
	return diff >= 0 && diff <= 5*time.Minute
}

func loadConfig() Config {
	return Config{
		SonarrURL:    getEnv("SONARR_URL", ""),
		SonarrAPIKey: getEnv("SONARR_API_KEY", ""),
		RadarrURL:    getEnv("RADARR_URL", ""),
		RadarrAPIKey: getEnv("RADARR_API_KEY", ""),
		MailgunSMTP:  getEnv("MAILGUN_SMTP", "smtp.mailgun.org"),
		MailgunPort:  getEnv("MAILGUN_PORT", "587"),
		MailgunUser:  getEnv("MAILGUN_USER", ""),
		MailgunPass:  getEnv("MAILGUN_PASS", ""),
		FromEmail:    getEnv("FROM_EMAIL", ""),
		FromName:     getEnv("FROM_NAME", "Newslettar"),
		ToEmails:     strings.Split(getEnv("TO_EMAILS", ""), ","),
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// Read .env file directly (for web UI to show current saved values)
func readEnvFile() map[string]string {
	envMap := make(map[string]string)
	
	data, err := os.ReadFile("/opt/newslettar/.env")
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
			envMap[parts[0]] = parts[1]
		}
	}
	
	return envMap
}

func getEnvFromFile(envMap map[string]string, key, fallback string) string {
	if value, exists := envMap[key]; exists && value != "" {
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
				IMDBID:      ep.IMDBID,
				TvdbID:      ep.TvdbID,
			}
		}
		seriesMap[ep.SeriesTitle].Episodes = append(seriesMap[ep.SeriesTitle].Episodes, ep)
	}

	var groups []SeriesGroup
	for _, group := range seriesMap {
		// Sort episodes by air date chronologically (oldest first)
		sort.Slice(group.Episodes, func(i, j int) bool {
			if group.Episodes[i].AirDate != group.Episodes[j].AirDate {
				return group.Episodes[i].AirDate < group.Episodes[j].AirDate
			}
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
				Title       string `json:"title"`
				ImdbId      string `json:"imdbId"`
				TvdbId      int    `json:"tvdbId"`
				Images      []struct {
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
					IMDBID:      record.Series.ImdbId,
					TvdbID:      record.Series.TvdbId,
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
			Title       string `json:"title"`
			ImdbId      string `json:"imdbId"`
			TvdbId      int    `json:"tvdbId"`
			Images      []struct {
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
			IMDBID:      ep.Series.ImdbId,
			TvdbID:      ep.Series.TvdbId,
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
			MovieID     int       `json:"movieId"`
			SourceTitle string    `json:"sourceTitle"`
			Date        time.Time `json:"date"`
			EventType   string    `json:"eventType"`
			Movie       struct {
				Title         string `json:"title"`
				Year          int    `json:"year"`
				ImdbId        string `json:"imdbId"`
				TmdbId        int    `json:"tmdbId"`
				PhysicalRelease string `json:"physicalRelease"`
				DigitalRelease  string `json:"digitalRelease"`
				InCinemas       string `json:"inCinemas"`
				Images        []struct {
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
		if record.EventType == "downloadFolderImported" && record.Date.After(since) {
			if !seen[record.MovieID] {
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

				releaseDate := record.Movie.DigitalRelease
				if releaseDate == "" {
					releaseDate = record.Movie.PhysicalRelease
				}
				if releaseDate == "" {
					releaseDate = record.Movie.InCinemas
				}

				movies = append(movies, Movie{
					Title:       record.Movie.Title,
					Year:        record.Movie.Year,
					ReleaseDate: releaseDate,
					Downloaded:  true,
					PosterURL:   posterURL,
					IMDBID:      record.Movie.ImdbId,
					TmdbID:      record.Movie.TmdbId,
				})
				seen[record.MovieID] = true
			}
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
		ImdbId          string `json:"imdbId"`
		TmdbId          int    `json:"tmdbId"`
		PhysicalRelease string `json:"physicalRelease"`
		DigitalRelease  string `json:"digitalRelease"`
		InCinemas       string `json:"inCinemas"`
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
	for _, movie := range result {
		posterURL := ""
		for _, img := range movie.Images {
			if img.CoverType == "poster" {
				if img.RemoteURL != "" {
					posterURL = img.RemoteURL
				} else if img.URL != "" {
					posterURL = cfg.RadarrURL + img.URL
				}
				break
			}
		}

		releaseDate := movie.DigitalRelease
		if releaseDate == "" {
			releaseDate = movie.PhysicalRelease
		}
		if releaseDate == "" {
			releaseDate = movie.InCinemas
		}

		movies = append(movies, Movie{
			Title:       movie.Title,
			Year:        movie.Year,
			ReleaseDate: releaseDate,
			Downloaded:  false,
			PosterURL:   posterURL,
			IMDBID:      movie.ImdbId,
			TmdbID:      movie.TmdbId,
		})
	}

	return movies, nil
}

// Helper function to format date with day of week
func formatDateWithDay(dateStr string) string {
	if dateStr == "" {
		return "Date TBA"
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", dateStr)
	if err != nil {
		t, err = time.Parse("2006-01-02", dateStr)
		if err != nil {
			return dateStr
		}
	}
	return t.Format("Monday, January 2, 2006")
}

func generateNewsletterHTML(data NewsletterData) (string, error) {
	// Default: show posters and downloaded section
	return generateNewsletterHTMLWithOptions(data, true, true)
}

func generateNewsletterHTMLWithOptions(data NewsletterData, showPosters bool, showDownloaded bool) (string, error) {
	tmpl := `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; max-width: 800px; margin: 0 auto; padding: 20px; background-color: #f5f5f5; }
        .container { background-color: white; padding: 30px; border-radius: 12px; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
        h1 { color: #2c3e50; border-bottom: 3px solid #3498db; padding-bottom: 10px; margin-bottom: 10px; }
        h2 { color: #34495e; margin-top: 40px; border-left: 4px solid #3498db; padding-left: 15px; }
        h3 { color: #2c3e50; margin-top: 25px; margin-bottom: 15px; font-size: 1.2em; }
        .section { margin-bottom: 30px; }
        .series-group { margin-bottom: 25px; border: 1px solid #e0e0e0; border-radius: 8px; overflow: hidden; background-color: #fafafa; }
        .series-header { display: flex; align-items: center; padding: 15px; background-color: #f0f0f0; border-bottom: 2px solid #3498db; }
        .poster { width: 60px; height: 90px; object-fit: cover; border-radius: 4px; margin-right: 15px; flex-shrink: 0; box-shadow: 0 2px 4px rgba(0,0,0,0.2); }
        .poster-placeholder { width: 60px; height: 90px; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); border-radius: 4px; margin-right: 15px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; font-size: 28px; color: white; }
        .series-title { font-weight: bold; font-size: 1.3em; color: #2c3e50; }
        .series-title a { color: #2c3e50; text-decoration: none; }
        .series-title a:hover { color: #3498db; text-decoration: underline; }
        .episode-list { padding: 10px 15px; }
        .episode-item { padding: 10px; margin: 5px 0; background-color: white; border-left: 3px solid #3498db; border-radius: 4px; }
        .episode-number { font-weight: 600; color: #3498db; display: inline-block; min-width: 70px; }
        .episode-title { color: #2c3e50; }
        .episode-date { color: #7f8c8d; font-size: 0.9em; margin-left: 10px; }
        .movie-item { display: flex; padding: 15px; margin: 12px 0; background-color: #f8f9fa; border-left: 3px solid #e74c3c; border-radius: 8px; align-items: flex-start; transition: transform 0.2s; }
        .movie-item:hover { transform: translateX(5px); background-color: #e9ecef; }
        .movie-poster { width: 80px; height: 120px; object-fit: cover; border-radius: 6px; margin-right: 15px; flex-shrink: 0; box-shadow: 0 2px 4px rgba(0,0,0,0.2); }
        .movie-poster-placeholder { width: 80px; height: 120px; background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%); border-radius: 6px; margin-right: 15px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; font-size: 36px; color: white; }
        .movie-content { flex: 1; }
        .movie-title { font-weight: bold; color: #2c3e50; font-size: 1.1em; }
        .movie-title a { color: #2c3e50; text-decoration: none; }
        .movie-title a:hover { color: #3498db; text-decoration: underline; }
        .movie-year { color: #7f8c8d; font-size: 0.95em; }
        .date-range { color: #7f8c8d; font-size: 0.95em; margin-bottom: 20px; }
        .empty { color: #95a5a6; font-style: italic; padding: 15px; text-align: center; background-color: #f8f9fa; border-radius: 6px; }
        .footer { margin-top: 40px; padding-top: 20px; border-top: 1px solid #e0e0e0; color: #7f8c8d; font-size: 0.85em; text-align: center; }
        .count-badge { background-color: #3498db; color: white; padding: 4px 10px; border-radius: 12px; font-size: 0.85em; margin-left: 10px; font-weight: normal; }
        .downloaded-section { margin-top: 50px; padding-top: 30px; border-top: 2px dashed #e0e0e0; }
        .downloaded-section h2 { color: #7f8c8d; border-left-color: #95a5a6; }
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
                        {{ if .PosterURL }}<img src="{{ .PosterURL }}" alt="{{ .SeriesTitle }}" class="poster" />{{ else }}<div class="poster-placeholder">📺</div>{{ end }}
                        <div class="series-title">{{ if .IMDBID }}<a href="https://www.imdb.com/title/{{ .IMDBID }}/" target="_blank">{{ .SeriesTitle }}</a>{{ else }}{{ .SeriesTitle }}{{ end }} <span style="color: #7f8c8d; font-size: 0.8em; font-weight: normal;">({{ len .Episodes }} episode{{ if gt (len .Episodes) 1 }}s{{ end }})</span></div>
                    </div>
                    <div class="episode-list">
                        {{ range .Episodes }}<div class="episode-item"><span class="episode-number">S{{ printf "%02d" .SeasonNum }}E{{ printf "%02d" .EpisodeNum }}</span><span class="episode-title">{{ if .Title }}{{ .Title }}{{ else }}TBA{{ end }}</span>{{ if .AirDate }}<span class="episode-date">{{ formatDateWithDay .AirDate }}</span>{{ end }}</div>{{ end }}
                    </div>
                </div>
                {{ end }}
            {{ else }}<div class="empty">No shows scheduled for next week</div>{{ end }}
            <h3>Movies <span class="count-badge">{{ len .UpcomingMovies }}</span></h3>
            {{ if .UpcomingMovies }}
                {{ range .UpcomingMovies }}<div class="movie-item">{{ if .PosterURL }}<img src="{{ .PosterURL }}" alt="{{ .Title }}" class="movie-poster" />{{ else }}<div class="movie-poster-placeholder">🎬</div>{{ end }}<div class="movie-content"><div class="movie-title">{{ if .IMDBID }}<a href="https://www.imdb.com/title/{{ .IMDBID }}/" target="_blank">{{ .Title }}</a>{{ else }}{{ .Title }}{{ end }}</div><div class="movie-year">({{ .Year }}){{ if .ReleaseDate }} • {{ formatDateWithDay .ReleaseDate }}{{ end }}</div></div></div>{{ end }}
            {{ else }}<div class="empty">No movies scheduled for next week</div>{{ end }}
        </div>
        <div class="section downloaded-section">
            <h2>📥 Downloaded Last Week</h2>
            <h3>TV Shows <span class="count-badge">{{ len .DownloadedSeriesGroups }}</span></h3>
            {{ if .DownloadedSeriesGroups }}
                {{ range .DownloadedSeriesGroups }}
                <div class="series-group">
                    <div class="series-header">
                        {{ if .PosterURL }}<img src="{{ .PosterURL }}" alt="{{ .SeriesTitle }}" class="poster" />{{ else }}<div class="poster-placeholder">📺</div>{{ end }}
                        <div class="series-title">{{ if .IMDBID }}<a href="https://www.imdb.com/title/{{ .IMDBID }}/" target="_blank">{{ .SeriesTitle }}</a>{{ else }}{{ .SeriesTitle }}{{ end }} <span style="color: #7f8c8d; font-size: 0.8em; font-weight: normal;">({{ len .Episodes }} episode{{ if gt (len .Episodes) 1 }}s{{ end }})</span></div>
                    </div>
                    <div class="episode-list">
                        {{ range .Episodes }}<div class="episode-item"><span class="episode-number">S{{ printf "%02d" .SeasonNum }}E{{ printf "%02d" .EpisodeNum }}</span><span class="episode-title">{{ if .Title }}{{ .Title }}{{ else }}Episode {{ .EpisodeNum }}{{ end }}</span></div>{{ end }}
                    </div>
                </div>
                {{ end }}
            {{ else }}<div class="empty">No shows downloaded this week</div>{{ end }}
            <h3>Movies <span class="count-badge">{{ len .DownloadedMovies }}</span></h3>
            {{ if .DownloadedMovies }}
                {{ range .DownloadedMovies }}<div class="movie-item">{{ if .PosterURL }}<img src="{{ .PosterURL }}" alt="{{ .Title }}" class="movie-poster" />{{ else }}<div class="movie-poster-placeholder">🎬</div>{{ end }}<div class="movie-content"><div class="movie-title">{{ if .IMDBID }}<a href="https://www.imdb.com/title/{{ .IMDBID }}/" target="_blank">{{ .Title }}</a>{{ else }}{{ .Title }}{{ end }}</div><div class="movie-year">({{ .Year }})</div></div></div>{{ end }}
            {{ else }}<div class="empty">No movies downloaded this week</div>{{ end }}
        </div>
        <div class="footer">Generated by Newslettar • {{ .WeekEnd }}</div>
    </div>
</body>
</html>`

	funcMap := template.FuncMap{
		"formatDateWithDay": formatDateWithDay,
		"showPosters":       func() bool { return showPosters },
		"showDownloaded":    func() bool { return showDownloaded },
	}

	t, err := template.New("newsletter").Funcs(funcMap).Parse(tmpl)
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

	fromHeader := cfg.FromEmail
	if cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", cfg.FromName, cfg.FromEmail)
	}

	for _, toEmail := range cfg.ToEmails {
		toEmail = strings.TrimSpace(toEmail)
		if toEmail == "" {
			continue
		}

		headers := make(map[string]string)
		headers["From"] = fromHeader
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

// Web Server
func startWebServer() {
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/api/config", configHandler)
	http.HandleFunc("/api/test-sonarr", testSonarrHandler)
	http.HandleFunc("/api/test-radarr", testRadarrHandler)
	http.HandleFunc("/api/test-email", testEmailHandler)
	http.HandleFunc("/api/send", sendHandler)
	http.HandleFunc("/api/schedule", scheduleHandler)
	http.HandleFunc("/api/logs", logsHandler)
	http.HandleFunc("/api/update", updateHandler)
	http.HandleFunc("/api/version", versionHandler)
	http.HandleFunc("/api/preview", previewHandler)

	port := getEnv("WEBUI_PORT", "8080")
	log.Printf("🌐 Newslettar v%s starting on http://0.0.0.0:%s", version, port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Newslettar v` + version + `</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Arial, sans-serif; background: #2d2d2d; min-height: 100vh; padding: 20px; }
        .container { max-width: 900px; margin: 0 auto; background: #3a3a3a; border-radius: 16px; box-shadow: 0 20px 60px rgba(0,0,0,0.5); overflow: hidden; }
        .header { background: #2d2d2d; color: #e0e0e0; padding: 30px; text-align: center; border-bottom: 2px solid #4a4a4a; }
        .header h1 { font-size: 2.5em; margin-bottom: 10px; color: #ffffff; }
        .header p { opacity: 0.9; font-size: 1.1em; color: #b0b0b0; }
        .version { position: absolute; top: 10px; right: 10px; background: rgba(255,255,255,0.1); padding: 5px 15px; border-radius: 20px; font-size: 0.9em; color: #b0b0b0; }
        .nav { display: flex; background: #2d2d2d; border-bottom: 2px solid #4a4a4a; }
        .nav-item { flex: 1; padding: 15px; text-align: center; cursor: pointer; border: none; background: none; font-size: 1em; font-weight: 500; color: #b0b0b0; transition: all 0.3s; }
        .nav-item:hover { background: #4a4a4a; color: #e0e0e0; }
        .nav-item.active { background: #3a3a3a; color: #ffffff; border-bottom: 3px solid #6a6a6a; }
        .update-badge {
            position: absolute;
            top: -8px;
            right: -8px;
            background: #6a4a4a;
            color: #ffffff;
            border-radius: 50%;
            width: 20px;
            height: 20px;
            font-size: 12px;
            font-weight: bold;
            display: none;
            align-items: center;
            justify-content: center;
            animation: pulse 2s infinite;
        }
        .update-badge.show {
            display: flex;
        }
        @keyframes pulse {
            0%, 100% { transform: scale(1); }
            50% { transform: scale(1.1); }
        }
        .content { padding: 30px; background: #3a3a3a; }
        .section { display: none; }
        .section.active { display: block; }
        .form-group { margin-bottom: 25px; }
        .form-group label { display: block; margin-bottom: 8px; font-weight: 600; color: #e0e0e0; }
        .form-group input, .form-group select { width: 100%; padding: 12px 15px; border: 2px solid #4a4a4a; border-radius: 8px; font-size: 1em; transition: border-color 0.3s; background: #2d2d2d; color: #e0e0e0; }
        .form-group input:focus, .form-group select:focus { outline: none; border-color: #6a6a6a; }
        .form-group input::placeholder { color: #808080; }
        .form-section { background: #2d2d2d; padding: 20px; border-radius: 8px; margin-bottom: 25px; border: 1px solid #4a4a4a; }
        .form-section h3 { color: #e0e0e0; margin-bottom: 15px; padding-bottom: 10px; border-bottom: 2px solid #4a4a4a; }
        .btn { padding: 12px 30px; border: none; border-radius: 8px; font-size: 1em; font-weight: 600; cursor: pointer; transition: all 0.3s; margin-right: 10px; margin-bottom: 10px; }
        .btn-primary { background: #4a4a4a; color: #ffffff; border: 1px solid #5a5a5a; }
        .btn-primary:hover { transform: translateY(-2px); box-shadow: 0 5px 15px rgba(0, 0, 0, 0.3); background: #5a5a5a; }
        .btn-primary:disabled { background: #3a3a3a; cursor: not-allowed; transform: none; }
        .btn-success { background: #4a6a4a; color: white; border: 1px solid #5a7a5a; }
        .btn-success:hover { background: #5a7a5a; }
        .btn-danger { background: #6a4a4a; color: white; border: 1px solid #7a5a5a; }
        .btn-danger:hover { background: #7a5a5a; }
        .btn-secondary { background: #4a4a4a; color: white; border: 1px solid #5a5a5a; }
        .btn-secondary:hover { background: #5a5a5a; }
        .btn-warning { background: #6a6a4a; color: #ffffff; border: 1px solid #7a7a5a; }
        .btn-warning:hover { background: #7a7a5a; }
        .status-box { padding: 15px; border-radius: 8px; margin-bottom: 15px; display: none; }
        .status-box.success { background: #2d4a2d; color: #a0d0a0; border: 1px solid #3a5a3a; display: block; }
        .status-box.error { background: #4a2d2d; color: #d0a0a0; border: 1px solid #5a3a3a; display: block; }
        .status-box.info { background: #2d3a4a; color: #a0b0d0; border: 1px solid #3a4a5a; display: block; }
        .test-results { margin-top: 20px; }
        .test-item { padding: 12px; margin: 8px 0; border-radius: 6px; background: #2d2d2d; border-left: 4px solid #6a6a6a; color: #e0e0e0; }
        .test-item.success { border-left-color: #4a6a4a; background: #2d3a2d; color: #a0d0a0; }
        .test-item.error { border-left-color: #6a4a4a; background: #3a2d2d; color: #d0a0a0; }
        .logs { background: #1e1e1e; color: #d4d4d4; padding: 20px; border-radius: 8px; font-family: 'Courier New', monospace; font-size: 0.9em; max-height: 500px; overflow-y: auto; white-space: pre-wrap; }
        .action-buttons { display: flex; gap: 10px; flex-wrap: wrap; }
        .update-info { background: #2d2d2d; border: 1px solid #4a4a4a; padding: 15px; border-radius: 8px; margin-bottom: 20px; color: #e0e0e0; }
        .spinner { display: inline-block; width: 20px; height: 20px; border: 3px solid rgba(255,255,255,.3); border-radius: 50%; border-top-color: white; animation: spin 1s ease-in-out infinite; }
        @keyframes spin { to { transform: rotate(360deg); } }
        .test-connection-btn { margin-top: 10px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header" style="position: relative;">
            <div class="version">v` + version + `</div>
            <h1>📺 Newslettar</h1>
            <p>Configuration & Management</p>
        </div>
        <div class="nav">
            <button class="nav-item active" onclick="showSection('config')">⚙️ Configuration</button>
            <button class="nav-item" onclick="showSection('template')">📧 Email Template</button>
            <button class="nav-item" onclick="showSection('actions')">🚀 Actions</button>
            <button class="nav-item" onclick="showSection('logs')">📋 Logs</button>
            <button class="nav-item" onclick="showSection('update')" style="position: relative;">
                🔄 Update
                <span class="update-badge" id="updateBadge">!</span>
            </button>
        </div>
        <div class="content">
            <div id="config" class="section active">
                <div id="configStatus" class="status-box"></div>
                <form id="configForm" onsubmit="saveConfig(event)">
                    <div class="form-section">
                        <h3><img src="https://raw.githubusercontent.com/Sonarr/Sonarr/develop/Logo/256.png" style="width: 24px; height: 24px; vertical-align: middle; margin-right: 8px;">Sonarr</h3>
                        <div class="form-group"><label>Sonarr URL</label><input type="text" id="sonarr_url" placeholder="http://192.168.1.100:8989"></div>
                        <div class="form-group"><label>Sonarr API Key</label><input type="text" id="sonarr_api_key" placeholder="Your Sonarr API Key"></div>
                        <button type="button" class="btn btn-secondary test-connection-btn" onclick="testSonarr()">🔍 Test Connection</button>
                        <div id="sonarrTestResult" class="test-results"></div>
                    </div>
                    <div class="form-section">
                        <h3><img src="https://raw.githubusercontent.com/Radarr/Radarr/develop/Logo/256.png" style="width: 24px; height: 24px; vertical-align: middle; margin-right: 8px;">Radarr</h3>
                        <div class="form-group"><label>Radarr URL</label><input type="text" id="radarr_url" placeholder="http://192.168.1.100:7878"></div>
                        <div class="form-group"><label>Radarr API Key</label><input type="text" id="radarr_api_key" placeholder="Your Radarr API Key"></div>
                        <button type="button" class="btn btn-secondary test-connection-btn" onclick="testRadarr()">🔍 Test Connection</button>
                        <div id="radarrTestResult" class="test-results"></div>
                    </div>
                    <div class="form-section">
                        <h3>📧 Email Settings</h3>
                        <div class="form-group"><label>SMTP Server</label><input type="text" id="mailgun_smtp" placeholder="smtp.mailgun.org"></div>
                        <div class="form-group"><label>SMTP Port</label><input type="text" id="mailgun_port" placeholder="587"></div>
                        <div class="form-group"><label>SMTP Username</label><input type="text" id="mailgun_user" placeholder="postmaster@yourdomain.mailgun.org"></div>
                        <div class="form-group"><label>SMTP Password</label><input type="password" id="mailgun_pass" placeholder="Your SMTP Password"></div>
                        <div class="form-group"><label>From Email</label><input type="email" id="from_email" placeholder="newsletter@yourdomain.com"></div>
                        <div class="form-group"><label>From Name (Sender Display Name)</label><input type="text" id="from_name" placeholder="Newslettar" value="Newslettar"></div>
                        <div class="form-group"><label>To Email(s) (comma-separated)</label><input type="text" id="to_emails" placeholder="user1@example.com, user2@example.com"></div>
                        <button type="button" class="btn btn-secondary test-connection-btn" onclick="testEmail()">🔍 Test Connection</button>
                        <div id="emailTestResult" class="test-results"></div>
                    </div>

                    <div class="form-section">
                        <h3>⏰ Schedule</h3>
                        <div class="form-group">
                            <label>Day of Week</label>
                            <select id="schedule_day">
                                <option value="Mon">Monday</option>
                                <option value="Tue">Tuesday</option>
                                <option value="Wed">Wednesday</option>
                                <option value="Thu">Thursday</option>
                                <option value="Fri">Friday</option>
                                <option value="Sat">Saturday</option>
                                <option value="Sun" selected>Sunday</option>
                            </select>
                        </div>
                        <div class="form-group">
                            <label>Time (24-hour format)</label>
                            <input type="time" id="schedule_time" value="09:00">
                        </div>
                        <div style="background: #2d3a4a; padding: 10px; border-radius: 6px; font-size: 0.9em; color: #a0b0d0; border: 1px solid #3a4a5a; margin-bottom: 15px;">
                            ℹ️ Newsletter will be sent automatically every <strong><span id="schedule_preview">Sunday at 09:00</span></strong>
                        </div>
                    </div>
                    <button type="submit" class="btn btn-primary" style="margin-top: 20px;">💾 Save All Configuration</button>
                </form>
            </div>
            <div id="template" class="section">
                <div id="templateStatus" class="status-box"></div>
                <h2 style="margin-bottom: 20px; color: #e0e0e0;">Email Template Settings</h2>
                
                <div class="form-section">
                    <h3>📝 Template Options</h3>
                    <div class="form-group">
                        <label style="display: flex; align-items: center; gap: 10px; cursor: pointer;">
                            <input type="checkbox" id="show_posters" checked onchange="updatePreview()">
                            <span>Show poster images in email</span>
                        </label>
                        <p style="color: #b0b0b0; font-size: 0.9em; margin-top: 5px; margin-left: 30px;">Display movie and series posters in the newsletter</p>
                    </div>
                    <div class="form-group">
                        <label style="display: flex; align-items: center; gap: 10px; cursor: pointer;">
                            <input type="checkbox" id="show_downloaded" checked onchange="updatePreview()">
                            <span>Include "Downloaded Last Week" section</span>
                        </label>
                        <p style="color: #b0b0b0; font-size: 0.9em; margin-top: 5px; margin-left: 30px;">Show content that was downloaded in the past week</p>
                    </div>
                    <button type="button" class="btn btn-primary" onclick="saveTemplateSettings()">💾 Save Template Settings</button>
                </div>

                <div class="form-section">
                    <h3>👁️ Live Preview</h3>
                    <p style="color: #b0b0b0; margin-bottom: 15px;">Preview how your newsletter will look with current settings</p>
                    <button type="button" class="btn btn-secondary" onclick="loadPreview()" style="margin-bottom: 15px;">🔄 Refresh Preview</button>
                    <div style="background: #2d2d2d; border: 1px solid #4a4a4a; border-radius: 8px; padding: 20px; max-height: 600px; overflow-y: auto;">
                        <iframe id="emailPreview" style="width: 100%; min-height: 500px; border: none; background: white;" srcdoc="<p style='text-align: center; padding: 50px; color: #999;'>Click 'Refresh Preview' to load preview</p>"></iframe>
                    </div>
                </div>
            </div>
            <div id="actions" class="section">
                <div id="actionStatus" class="status-box"></div>
                <h2 style="margin-bottom: 20px; color: #e0e0e0;">Quick Actions</h2>
                <div class="action-buttons">
                    <button class="btn btn-primary" onclick="sendNewsletter()">📧 Send Newsletter Now</button>
                </div>
            </div>
            <div id="logs" class="section">
                <h2 style="margin-bottom: 20px; color: #e0e0e0;">Recent Logs</h2>
                <button class="btn btn-secondary" onclick="loadLogs()" style="margin-bottom: 15px;">🔄 Refresh Logs</button>
                <div id="logsContent" class="logs">Loading logs...</div>
            </div>
            <div id="update" class="section">
                <div id="updateStatus" class="status-box"></div>
                <h2 style="margin-bottom: 20px; color: #e0e0e0;">Update Newslettar</h2>
                <div class="update-info">
                    <strong>Current Version:</strong> <span id="currentVersion">` + version + `</span><br>
                    <strong>Latest Version:</strong> <span id="latestVersion">Checking...</span><br>
                    <strong>Repository:</strong> github.com/agencefanfare/lerefuge
                </div>
                <div id="changelogSection" style="display: none; margin-top: 20px; padding: 15px; background: #2d2d2d; border-radius: 8px; border: 1px solid #4a4a4a;">
                    <h3 style="margin-bottom: 10px; color: #e0e0e0;">What's New:</h3>
                    <ul id="changelogList" style="margin-left: 20px; color: #b0b0b0;"></ul>
                </div>
                <div class="action-buttons" style="margin-top: 20px;">
                    <button class="btn btn-warning" onclick="checkUpdate()">🔍 Check for Updates</button>
                    <button class="btn btn-primary" id="updateBtn" onclick="performUpdate()" style="display: none;">🚀 Update Now</button>
                </div>
                <div id="updateResults" style="margin-top: 20px;"></div>
            </div>
        </div>
    </div>
    <script>
        let logsRefreshInterval;
        
        window.onload = () => {
            loadConfig();
            checkUpdateSilently();
        };
        
        function checkUpdateSilently() {
            fetch('/api/version').then(r => r.json())
                .then(data => {
                    document.getElementById('latestVersion').textContent = data.latest_version;
                    if (data.update_available) {
                        document.getElementById('updateBadge').classList.add('show');
                        document.getElementById('updateBtn').style.display = 'inline-block';
                    }
                })
                .catch(() => {
                    document.getElementById('latestVersion').textContent = 'Unknown';
                });
        }
        
        function showSection(section) {
            document.querySelectorAll('.section').forEach(s => s.classList.remove('active'));
            document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
            document.getElementById(section).classList.add('active');
            event.target.classList.add('active');
            
            // Clear logs interval when leaving logs section
            if (logsRefreshInterval) {
                clearInterval(logsRefreshInterval);
                logsRefreshInterval = null;
            }
            
            if (section === 'logs') {
                loadLogs();
                // Auto-refresh logs every 5 seconds
                logsRefreshInterval = setInterval(loadLogs, 5000);
            }
            
            if (section === 'template') {
                loadTemplateSettings();
            }
        }
        
        function loadConfig() {
            fetch('/api/config').then(r => r.json()).then(data => {
                document.getElementById('sonarr_url').value = data.sonarr_url || '';
                document.getElementById('sonarr_api_key').value = data.sonarr_api_key || '';
                document.getElementById('radarr_url').value = data.radarr_url || '';
                document.getElementById('radarr_api_key').value = data.radarr_api_key || '';
                document.getElementById('mailgun_smtp').value = data.mailgun_smtp || '';
                document.getElementById('mailgun_port').value = data.mailgun_port || '';
                document.getElementById('mailgun_user').value = data.mailgun_user || '';
                document.getElementById('mailgun_pass').value = data.mailgun_pass || '';
                document.getElementById('from_email').value = data.from_email || '';
                document.getElementById('from_name').value = data.from_name || 'Newslettar';
                document.getElementById('to_emails').value = data.to_emails || '';
                document.getElementById('schedule_day').value = data.schedule_day || 'Sun';
                document.getElementById('schedule_time').value = data.schedule_time || '09:00';
                updateSchedulePreview();
            }).catch(err => showStatus('configStatus', 'Error loading configuration', 'error'));
        }
        
        function updateSchedulePreview() {
            const day = document.getElementById('schedule_day').options[document.getElementById('schedule_day').selectedIndex].text;
            const time = document.getElementById('schedule_time').value;
            document.getElementById('schedule_preview').textContent = day + ' at ' + time;
        }
        
        document.addEventListener('DOMContentLoaded', () => {
            const daySelect = document.getElementById('schedule_day');
            const timeInput = document.getElementById('schedule_time');
            if (daySelect) daySelect.addEventListener('change', updateSchedulePreview);
            if (timeInput) timeInput.addEventListener('change', updateSchedulePreview);
        });
        
        function saveConfig(e) {
            if (e) e.preventDefault();
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
                from_name: document.getElementById('from_name').value,
                to_emails: document.getElementById('to_emails').value,
                schedule_day: document.getElementById('schedule_day').value,
                schedule_time: document.getElementById('schedule_time').value,
            };
            
            showStatus('configStatus', '💾 Saving configuration...', 'info');
            
            fetch('/api/config', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(config) })
                .then(r => r.json())
                .then(() => {
                    showStatus('configStatus', '✓ Configuration saved successfully!', 'success');
                    // Reload config to show what was actually saved
                    setTimeout(() => loadConfig(), 500);
                })
                .catch(err => {
                    console.error('Save error:', err);
                    showStatus('configStatus', '✗ Error saving configuration', 'error');
                });
        }
        
        function updateSystemdTimer() {
            fetch('/api/schedule', { method: 'POST' })
                .then(r => r.json())
                .catch(err => console.error('Failed to update timer:', err));
        }
        
        function testSonarr() {
            const resultDiv = document.getElementById('sonarrTestResult');
            resultDiv.innerHTML = '<div class="test-item"><span class="spinner"></span> Testing Sonarr connection...</div>';
            
            fetch('/api/test-sonarr', { 
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    url: document.getElementById('sonarr_url').value,
                    api_key: document.getElementById('sonarr_api_key').value
                })
            })
            .then(r => r.json())
            .then(data => {
                const status = data.success ? 'success' : 'error';
                const icon = data.success ? '✓' : '✗';
                resultDiv.innerHTML = '<div class="test-item ' + status + '">' + icon + ' ' + data.message + '</div>';
            })
            .catch(() => {
                resultDiv.innerHTML = '<div class="test-item error">✗ Connection test failed</div>';
            });
        }
        
        function testRadarr() {
            const resultDiv = document.getElementById('radarrTestResult');
            resultDiv.innerHTML = '<div class="test-item"><span class="spinner"></span> Testing Radarr connection...</div>';
            
            fetch('/api/test-radarr', { 
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    url: document.getElementById('radarr_url').value,
                    api_key: document.getElementById('radarr_api_key').value
                })
            })
            .then(r => r.json())
            .then(data => {
                const status = data.success ? 'success' : 'error';
                const icon = data.success ? '✓' : '✗';
                resultDiv.innerHTML = '<div class="test-item ' + status + '">' + icon + ' ' + data.message + '</div>';
            })
            .catch(() => {
                resultDiv.innerHTML = '<div class="test-item error">✗ Connection test failed</div>';
            });
        }
        
        function testEmail() {
            const resultDiv = document.getElementById('emailTestResult');
            const user = document.getElementById('mailgun_user').value;
            const pass = document.getElementById('mailgun_pass').value;
            
            if (!user || !pass) {
                resultDiv.innerHTML = '<div class="test-item error">✗ Please enter SMTP credentials first</div>';
                return;
            }
            
            resultDiv.innerHTML = '<div class="test-item"><span class="spinner"></span> Testing email configuration...</div>';
            
            fetch('/api/test-email', { 
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    smtp: document.getElementById('mailgun_smtp').value,
                    port: document.getElementById('mailgun_port').value,
                    user: user,
                    pass: pass
                })
            })
            .then(r => r.json())
            .then(data => {
                const status = data.success ? 'success' : 'error';
                const icon = data.success ? '✓' : '✗';
                resultDiv.innerHTML = '<div class="test-item ' + status + '">' + icon + ' ' + data.message + '</div>';
            })
            .catch(() => {
                resultDiv.innerHTML = '<div class="test-item error">✗ Connection test failed</div>';
            });
        }
        
        function sendNewsletter() {
            if (!confirm('Send newsletter now?')) return;
            showStatus('actionStatus', '📧 Sending newsletter...', 'info');
            fetch('/api/send', { method: 'POST' }).then(r => r.json())
                .then(data => showStatus('actionStatus', data.success ? '✓ Newsletter sent successfully!' : '✗ ' + data.message, data.success ? 'success' : 'error'))
                .catch(() => showStatus('actionStatus', '✗ Error sending newsletter', 'error'));
        }
        
        function loadLogs() {
            fetch('/api/logs').then(r => r.text())
                .then(data => {
                    const logsDiv = document.getElementById('logsContent');
                    logsDiv.textContent = data || 'No logs available';
                    // Auto-scroll to bottom
                    logsDiv.scrollTop = logsDiv.scrollHeight;
                })
                .catch(() => document.getElementById('logsContent').textContent = 'Error loading logs');
        }
        
        function checkUpdate() {
            showStatus('updateStatus', '🔍 Checking for updates...', 'info');
            fetch('/api/version').then(r => r.json())
                .then(data => {
                    document.getElementById('currentVersion').textContent = data.current_version;
                    document.getElementById('latestVersion').textContent = data.latest_version;
                    
                    if (data.update_available) {
                        const msg = '✓ Update available: ' + data.current_version + ' → ' + data.latest_version;
                        showStatus('updateStatus', msg, 'info');
                        document.getElementById('updateBtn').style.display = 'inline-block';
                        document.getElementById('updateBadge').classList.add('show');
                        
                        // Show changelog
                        if (data.changelog && data.changelog.length > 0) {
                            const changelogList = document.getElementById('changelogList');
                            changelogList.innerHTML = '';
                            data.changelog.forEach(change => {
                                const li = document.createElement('li');
                                li.textContent = change;
                                li.style.marginBottom = '5px';
                                changelogList.appendChild(li);
                            });
                            document.getElementById('changelogSection').style.display = 'block';
                        }
                    } else {
                        showStatus('updateStatus', '✓ You are running the latest version (' + data.current_version + ')', 'success');
                        document.getElementById('updateBtn').style.display = 'none';
                        document.getElementById('updateBadge').classList.remove('show');
                        document.getElementById('changelogSection').style.display = 'none';
                    }
                })
                .catch(() => showStatus('updateStatus', '✗ Error checking for updates', 'error'));
        }
        
        function performUpdate() {
            if (!confirm('This will download and install the latest version. The web interface will be unavailable for about 20 seconds during the update and restart. Continue?')) return;
            
            const updateBtn = document.getElementById('updateBtn');
            updateBtn.disabled = true;
            updateBtn.textContent = '⏳ Updating...';
            
            showStatus('updateStatus', '🚀 Downloading and building update...', 'info');
            
            fetch('/api/update', { method: 'POST' })
                .then(r => r.json())
                .then(data => {
                    if (data.success) {
                        showStatus('updateStatus', '⏳ Building and restarting... Page will reload automatically.', 'info');
                        document.getElementById('updateBadge').classList.remove('show');
                        
                        // Wait 20 seconds for download + build + restart, then reload
                        let countdown = 20;
                        const countdownInterval = setInterval(() => {
                            countdown--;
                            showStatus('updateStatus', '⏳ Restarting service... (' + countdown + 's)', 'info');
                            if (countdown <= 0) {
                                clearInterval(countdownInterval);
                                location.reload();
                            }
                        }, 1000);
                    } else {
                        showStatus('updateStatus', '✗ ' + data.message, 'error');
                        updateBtn.disabled = false;
                        updateBtn.textContent = '🚀 Update Now';
                    }
                })
                .catch(() => {
                    showStatus('updateStatus', '✗ Update request failed', 'error');
                    updateBtn.disabled = false;
                    updateBtn.textContent = '🚀 Update Now';
                });
        }
        
        function showStatus(elementId, message, type) {
            const el = document.getElementById(elementId);
            el.innerHTML = message;
            el.className = 'status-box ' + type;
            if (type !== 'error' && type !== 'info') {
                setTimeout(() => el.className = 'status-box', 10000);
            }
        }
        
        function loadTemplateSettings() {
            fetch('/api/config').then(r => r.json()).then(data => {
                document.getElementById('show_posters').checked = (data.show_posters || 'true') === 'true';
                document.getElementById('show_downloaded').checked = (data.show_downloaded || 'true') === 'true';
            });
        }
        
        function saveTemplateSettings() {
            const showPosters = document.getElementById('show_posters').checked ? 'true' : 'false';
            const showDownloaded = document.getElementById('show_downloaded').checked ? 'true' : 'false';
            
            // Load current config and update only template settings
            fetch('/api/config').then(r => r.json()).then(currentConfig => {
                currentConfig.show_posters = showPosters;
                currentConfig.show_downloaded = showDownloaded;
                
                showStatus('templateStatus', '💾 Saving template settings...', 'info');
                
                fetch('/api/config', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(currentConfig) })
                    .then(r => r.json())
                    .then(() => {
                        showStatus('templateStatus', '✓ Template settings saved successfully!', 'success');
                        loadPreview();
                    })
                    .catch(() => showStatus('templateStatus', '✗ Error saving template settings', 'error'));
            });
        }
        
        function updatePreview() {
            loadPreview();
        }
        
        function loadPreview() {
            const showPosters = document.getElementById('show_posters').checked;
            const showDownloaded = document.getElementById('show_downloaded').checked;
            const iframe = document.getElementById('emailPreview');
            
            showStatus('templateStatus', '🔄 Loading preview...', 'info');
            
            fetch('/api/preview?show_posters=' + showPosters + '&show_downloaded=' + showDownloaded)
                .then(r => r.text())
                .then(html => {
                    iframe.srcdoc = html;
                    showStatus('templateStatus', '✓ Preview loaded', 'success');
                })
                .catch(() => {
                    showStatus('templateStatus', '✗ Error loading preview. Make sure Sonarr/Radarr are configured.', 'error');
                });
        }
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, tmpl)
}

func configHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		// Read current schedule from timer
		scheduleDay := "Sun"
		scheduleTime := "09:00"
		
		cmd := exec.Command("systemctl", "cat", "newslettar-send.timer")
		output, err := cmd.Output()
		if err == nil {
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "OnCalendar=") {
					// Parse "OnCalendar=Sun *-*-* 09:00:00"
					parts := strings.Fields(strings.TrimPrefix(line, "OnCalendar="))
					if len(parts) >= 3 {
						scheduleDay = parts[0]
						timeStr := parts[2]
						if len(timeStr) >= 5 {
							scheduleTime = timeStr[:5]
						}
					}
				}
			}
		}

		// Read from .env file directly instead of os.Getenv()
		// This ensures we show the saved values, not the process environment
		envMap := readEnvFile()

		cfg := WebConfig{
			SonarrURL:    getEnvFromFile(envMap, "SONARR_URL", ""),
			SonarrAPIKey: getEnvFromFile(envMap, "SONARR_API_KEY", ""),
			RadarrURL:    getEnvFromFile(envMap, "RADARR_URL", ""),
			RadarrAPIKey: getEnvFromFile(envMap, "RADARR_API_KEY", ""),
			MailgunSMTP:  getEnvFromFile(envMap, "MAILGUN_SMTP", "smtp.mailgun.org"),
			MailgunPort:  getEnvFromFile(envMap, "MAILGUN_PORT", "587"),
			MailgunUser:  getEnvFromFile(envMap, "MAILGUN_USER", ""),
			MailgunPass:  getEnvFromFile(envMap, "MAILGUN_PASS", ""),
			FromEmail:    getEnvFromFile(envMap, "FROM_EMAIL", ""),
			FromName:     getEnvFromFile(envMap, "FROM_NAME", "Newslettar"),
			ToEmails:     getEnvFromFile(envMap, "TO_EMAILS", ""),
			ScheduleDay:  scheduleDay,
			ScheduleTime: scheduleTime,
			ShowPosters:    getEnvFromFile(envMap, "SHOW_POSTERS", "true"),
			ShowDownloaded: getEnvFromFile(envMap, "SHOW_DOWNLOADED", "true"),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	} else if r.Method == "POST" {
		var cfg WebConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		envContent := fmt.Sprintf(`SONARR_URL=%s
SONARR_API_KEY=%s
RADARR_URL=%s
RADARR_API_KEY=%s
MAILGUN_SMTP=%s
MAILGUN_PORT=%s
MAILGUN_USER=%s
MAILGUN_PASS=%s
FROM_EMAIL=%s
FROM_NAME=%s
TO_EMAILS=%s
SHOW_POSTERS=%s
SHOW_DOWNLOADED=%s
WEBUI_PORT=%s
`, cfg.SonarrURL, cfg.SonarrAPIKey, cfg.RadarrURL, cfg.RadarrAPIKey,
			cfg.MailgunSMTP, cfg.MailgunPort, cfg.MailgunUser, cfg.MailgunPass,
			cfg.FromEmail, cfg.FromName, cfg.ToEmails,
			cfg.ShowPosters, cfg.ShowDownloaded,
			getEnv("WEBUI_PORT", "8080"))

		if err := os.WriteFile("/opt/newslettar/.env", []byte(envContent), 0644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Update timer with new schedule
		updateTimerSchedule(cfg.ScheduleDay, cfg.ScheduleTime)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	}
}

func updateTimerSchedule(day, timeStr string) error {
	timerContent := fmt.Sprintf(`[Unit]
Description=Newslettar Weekly Newsletter Timer
Requires=newslettar-send.service

[Timer]
OnCalendar=%s *-*-* %s:00
AccuracySec=1s
Persistent=false

[Install]
WantedBy=timers.target
`, day, timeStr)

	if err := os.WriteFile("/etc/systemd/system/newslettar-send.timer", []byte(timerContent), 0644); err != nil {
		return err
	}

	// Reload systemd
	exec.Command("systemctl", "daemon-reload").Run()
	exec.Command("systemctl", "restart", "newslettar-send.timer").Run()

	return nil
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

	client := &http.Client{Timeout: 10 * time.Second}
	httpReq, _ := http.NewRequest("GET", req.URL+"/api/v3/system/status", nil)
	httpReq.Header.Set("X-Api-Key", req.APIKey)
	
	resp, err := client.Do(httpReq)
	success := false
	message := "Connection failed"
	
	if err == nil && resp.StatusCode == 200 {
		success = true
		message = "Connected successfully to Sonarr"
		resp.Body.Close()
	} else if err != nil {
		message = fmt.Sprintf("Connection failed: %v", err)
	} else {
		message = fmt.Sprintf("Connection failed: HTTP %d", resp.StatusCode)
		resp.Body.Close()
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

	client := &http.Client{Timeout: 10 * time.Second}
	httpReq, _ := http.NewRequest("GET", req.URL+"/api/v3/system/status", nil)
	httpReq.Header.Set("X-Api-Key", req.APIKey)
	
	resp, err := client.Do(httpReq)
	success := false
	message := "Connection failed"
	
	if err == nil && resp.StatusCode == 200 {
		success = true
		message = "Connected successfully to Radarr"
		resp.Body.Close()
	} else if err != nil {
		message = fmt.Sprintf("Connection failed: %v", err)
	} else {
		message = fmt.Sprintf("Connection failed: HTTP %d", resp.StatusCode)
		resp.Body.Close()
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
		// Test SMTP authentication with STARTTLS
		addr := fmt.Sprintf("%s:%s", req.SMTP, req.Port)
		
		// Try to connect
		client, err := smtp.Dial(addr)
		if err != nil {
			message = fmt.Sprintf("Connection failed: %v", err)
		} else {
			defer client.Close()
			
			// Send EHLO
			if err = client.Hello("localhost"); err != nil {
				message = fmt.Sprintf("EHLO failed: %v", err)
			} else if ok, _ := client.Extension("STARTTLS"); ok {
				// Use STARTTLS if available
				config := &tls.Config{ServerName: req.SMTP}
				if err = client.StartTLS(config); err != nil {
					message = fmt.Sprintf("STARTTLS failed: %v", err)
				} else {
					// Now try to authenticate
					auth := smtp.PlainAuth("", req.User, req.Pass, req.SMTP)
					if err = client.Auth(auth); err != nil {
						message = fmt.Sprintf("Authentication failed: %v", err)
					} else {
						success = true
						message = "SMTP authentication successful (with STARTTLS)"
					}
				}
			} else {
				// Try authentication without STARTTLS
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
	// Load .env file and add to environment
	envMap := readEnvFile()
	envVars := os.Environ()
	
	// Add all .env variables to the environment
	for key, value := range envMap {
		envVars = append(envVars, fmt.Sprintf("%s=%s", key, value))
	}
	
	// Add MANUAL_RUN flag
	envVars = append(envVars, "MANUAL_RUN=true")
	
	cmd := exec.Command("/opt/newslettar/newslettar")
	cmd.Env = envVars
	output, err := cmd.CombinedOutput()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": err == nil,
		"message": string(output),
	})
}

func scheduleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		// Trigger timer update
		exec.Command("systemctl", "daemon-reload").Run()
		exec.Command("systemctl", "restart", "newslettar-send.timer").Run()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
		return
	}

	cmd := exec.Command("systemctl", "list-timers", "newslettar-send.timer", "--no-pager")
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
	json.NewEncoder(w).Encode(map[string]string{"next_run": nextRun})
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("/var/log/newslettar.log")
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "No logs available")
		return
	}

	lines := strings.Split(string(data), "\n")
	start := len(lines) - 200
	if start < 0 {
		start = 0
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, strings.Join(lines[start:], "\n"))
}

// Compare semantic versions (returns true if remote is newer than current)
func isNewerVersion(remote, current string) bool {
	// Remove 'v' prefix if present
	remote = strings.TrimPrefix(remote, "v")
	current = strings.TrimPrefix(current, "v")
	
	// Parse versions like "1.0.13" into [1, 0, 13]
	remoteParts := strings.Split(remote, ".")
	currentParts := strings.Split(current, ".")
	
	// Pad to same length
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
	
	// Compare each part
	for i := 0; i < maxLen; i++ {
		var remoteNum, currentNum int
		fmt.Sscanf(remoteParts[i], "%d", &remoteNum)
		fmt.Sscanf(currentParts[i], "%d", &currentNum)
		
		if remoteNum > currentNum {
			return true // Remote is newer
		} else if remoteNum < currentNum {
			return false // Current is newer
		}
		// If equal, continue to next part
	}
	
	return false // Versions are equal
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	// Fetch latest version from GitHub
	resp, err := http.Get("https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/version.json")
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

	// Compare versions properly (only update if remote is newer)
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

func previewHandler(w http.ResponseWriter, r *http.Request) {
	// Get template settings from query params
	showPosters := r.URL.Query().Get("show_posters") == "true"
	showDownloaded := r.URL.Query().Get("show_downloaded") == "true"
	
	cfg := loadConfig()
	
	now := time.Now()
	weekStart := now.AddDate(0, 0, -7)
	weekEnd := now
	
	// Fetch data for preview
	downloadedEpisodes, _ := fetchSonarrHistory(cfg, weekStart)
	upcomingEpisodes, _ := fetchSonarrCalendar(cfg, weekEnd, weekEnd.AddDate(0, 0, 7))
	downloadedMovies, _ := fetchRadarrHistory(cfg, weekStart)
	upcomingMovies, _ := fetchRadarrCalendar(cfg, weekEnd, weekEnd.AddDate(0, 0, 7))
	
	// Sort chronologically
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
	
	// Generate HTML with template options
	html, err := generateNewsletterHTMLWithOptions(data, showPosters, showDownloaded)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
}

func updateHandler(w http.ResponseWriter, r *http.Request) {
	// Send response immediately so UI doesn't hang
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Update started! Building in background...",
	})

	// Run update in background (same as your working manual command)
	go func() {
		time.Sleep(1 * time.Second) // Give response time to send

		log.Println("🔄 Starting update process...")
		
		cmd := exec.Command("bash", "-c", `
			set -e
			cd /opt/newslettar
			echo "Backing up .env..."
			cp .env .env.backup
			echo "Downloading main.go..."
			wget -O main.go https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/main.go
			echo "Downloading version.json..."
			wget -O version.json https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/version.json
			echo "Building..."
			/usr/local/go/bin/go build -o newslettar main.go
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