# Newslettar v1.0.8 - Update Summary

## 🎉 All Issues Fixed!

### ✅ Schedule Issues FIXED
- **Problem**: Schedule wasn't saving and reset to Sunday 9am on refresh
- **Solution**: Now properly reads from systemd timer and updates it when saved
- Configuration now persists correctly across refreshes
- Timer is automatically reloaded when schedule changes

### ✅ Actions Tab Cleaned Up
- **Removed**: Redundant "Next newsletter: Unknown" message
- The schedule info is already visible in the Configuration tab where it belongs
- Actions tab now has cleaner, more focused UI

### ✅ Logs Auto-Refresh WORKING
- **Problem**: Logs stuck at start time (13:52:11), refresh button did nothing
- **Solution**: 
  - Logs now auto-refresh every 5 seconds when viewing the Logs tab
  - Shows last 200 lines (increased from 100)
  - Refresh button works immediately
  - Auto-scrolls to bottom to show latest entries

### ✅ Update Button FULLY FUNCTIONAL
- **Problem**: Update button just turned off service without updating
- **Solution**: Now properly:
  1. Downloads latest main.go and go.mod from GitHub
  2. Backs up .env file
  3. Builds new binary with Go
  4. Stops service
  5. Replaces binary
  6. Restarts service
  7. Cleans up old binary
- Mirrors the manual update process you showed
- Shows countdown timer (20 seconds) then auto-reloads page

### ✅ Test Connection Buttons Added
- **New Feature**: Individual test buttons in Configuration tab for:
  - Sonarr connection (tests API connectivity)
  - Radarr connection (tests API connectivity)  
  - Email configuration (tests SMTP authentication)
- Real-time feedback with spinner while testing
- Shows success/error messages inline
- No need to go to Actions tab anymore

### ✅ Email Sender Name Field
- **New Feature**: Added "From Name" field in Email Settings
- Allows customization of sender display name (e.g., "Newslettar", "Weekly Updates", etc.)
- Defaults to "Newslettar" if not specified
- Email will show as "From Name <email@domain.com>"

### ✅ IMDB Links Added
- **New Feature**: All movies, series, and episodes now have clickable IMDB links
- Links open in new tab
- Applies to both "Coming This Week" and "Downloaded This Week" sections
- Uses IMDB ID from Sonarr/Radarr API

### ✅ Chronological Ordering with Day of Week
- **Movies**: Now sorted by release date (oldest first) 
- **Episodes**: Sorted by air date (oldest first)
- **Day of Week**: Release dates now show full format "Monday, January 2, 2006"
- Makes it much clearer when things are releasing

### ✅ Other Improvements
- Notifications now persist 10 seconds (was 5)
- Better error messages throughout
- Improved status feedback
- Dark greyish color scheme maintained from your GitHub version

## 🚀 How to Update

### Option 1: Use the Web UI (Easiest)
1. Upload these files to your server
2. Replace `/opt/newslettar/main.go` with the new one
3. Replace `/opt/newslettar/version.json` with the new one
4. Run: `cd /opt/newslettar && /usr/local/go/bin/go build -o newslettar main.go`
5. Run: `systemctl restart newslettar.service`
6. Open web UI - it should show v1.0.8

### Option 2: From GitHub (After you push)
1. Go to Update tab in web UI
2. Click "Check for Updates"
3. Click "Update Now"
4. Wait 20 seconds for automatic restart
5. Page will reload with v1.0.8

### Option 3: Manual Command Line
```bash
cd /opt/newslettar
cp .env .env.backup
wget -O main.go https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/main.go
wget -O version.json https://raw.githubusercontent.com/agencefanfare/lerefuge/main/newslettar/version.json
/usr/local/go/bin/go build -o newslettar main.go
mv .env.backup .env
systemctl restart newslettar.service
```

## 📝 What Changed in the Code

### Configuration Handler
- Now reads current schedule from systemd timer file
- Properly updates timer when schedule changes
- Saves FROM_NAME field to .env

### New API Endpoints
- `/api/test-sonarr` - Tests Sonarr connection
- `/api/test-radarr` - Tests Radarr connection
- `/api/test-email` - Tests SMTP authentication

### Update Handler
- Fixed to download both main.go and go.mod
- Proper build process with error handling
- Graceful service restart

### Frontend JavaScript
- Auto-refresh logs every 5 seconds
- Interval cleared when leaving logs tab
- Better notification timeout handling
- Individual test connection functions
- Fixed schedule persistence

### Newsletter Generation
- Added `formatDateWithDay()` helper function
- Movies/episodes sorted chronologically
- IMDB links in HTML template
- Day of week in release dates

## 🔧 Configuration File Changes

The `.env` file now includes:
```bash
FROM_NAME=Newslettar  # New field for email sender display name
```

All other fields remain the same.

## ✨ Version Info
- **Version**: 1.0.8
- **Released**: November 14, 2025
- **Previous Version**: 1.0.7

Enjoy your fully-functional Newslettar! 🎉