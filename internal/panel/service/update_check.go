package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/buildinfo"
)

// The update notice: when the Owner turns it on, the panel reads the release
// feed of its repository once a day and tells the Owner about a newer release
// and whether it fixes a security problem. Off by default. The request carries
// nothing about this panel: no version, no address, no identifier.

const (
	updateCheckSection = "update_check"
	updateStateSection = "update_check_state"
)

// UpdateCheckSettings is the Owner's choice.
type UpdateCheckSettings struct {
	Enabled bool `json:"enabled"`
	// Repository is the GitHub owner/name the releases are published from.
	Repository string `json:"repository"`
}

// ReleaseInfo is one release from the feed.
type ReleaseInfo struct {
	Tag         string    `json:"tag"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	Notes       string    `json:"notes"`
}

// UpdateCheckState is what the last check found.
type UpdateCheckState struct {
	CheckedAt *time.Time   `json:"checked_at"`
	Latest    *ReleaseInfo `json:"latest"`
	// Security: a release newer than this build says it fixes a security problem.
	Security     bool   `json:"security"`
	Error        string `json:"error,omitempty"`
	DismissedTag string `json:"dismissed_tag,omitempty"`
}

// UpdateStatus is what the panel shows: the setting, the build and the news.
type UpdateStatus struct {
	UpdateCheckSettings
	UpdateCheckState
	Current string `json:"current"`
	// UpdateAvailable is nil when this build is not a release and cannot be compared.
	UpdateAvailable *bool `json:"update_available"`
	// Notice: a newer release the Owner has not dismissed.
	Notice bool `json:"notice"`
}

var (
	// ErrUpdateCheckOff refuses a check while the notice is off.
	ErrUpdateCheckOff = errors.New("the update notice is off: turn it on first")
	// ErrInvalidRepository refuses a repository that is not owner/name.
	ErrInvalidRepository = errors.New("the repository is a GitHub owner/name, such as owner/maburvm")
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

// releaseFeed is where releases are read from; tests point it elsewhere.
var releaseFeed = "https://api.github.com/repos/%s/releases?per_page=30"

var updateHTTP = &http.Client{Timeout: 20 * time.Second}

func readSection(ctx context.Context, db *gorm.DB, section string, into any) {
	var raw string
	if err := db.WithContext(ctx).Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", section).Scan(&raw).Error; err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), into)
	}
}

func writeSection(ctx context.Context, db *gorm.DB, section string, v any) error {
	data, _ := json.Marshal(v)
	return db.WithContext(ctx).Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()", section, string(data)).Error
}

// SaveUpdateCheckSettings stores the Owner's choice. Turning it on needs the repository.
func SaveUpdateCheckSettings(ctx context.Context, db *gorm.DB, s UpdateCheckSettings) error {
	s.Repository = strings.Trim(strings.TrimSpace(s.Repository), "/")
	s.Repository = strings.TrimPrefix(s.Repository, "https://github.com/")
	if (s.Enabled || s.Repository != "") && !repositoryPattern.MatchString(s.Repository) {
		return ErrInvalidRepository
	}
	return writeSection(ctx, db, updateCheckSection, s)
}

// DismissUpdate hides the notice for one release.
func DismissUpdate(ctx context.Context, db *gorm.DB, tag string) error {
	var st UpdateCheckState
	readSection(ctx, db, updateStateSection, &st)
	st.DismissedTag = strings.TrimSpace(tag)
	return writeSection(ctx, db, updateStateSection, st)
}

// GetUpdateStatus reports the setting, this build and the last check.
func GetUpdateStatus(ctx context.Context, db *gorm.DB) UpdateStatus {
	var out UpdateStatus
	readSection(ctx, db, updateCheckSection, &out.UpdateCheckSettings)
	readSection(ctx, db, updateStateSection, &out.UpdateCheckState)
	out.Current = buildinfo.Get().Version
	if out.Latest != nil {
		if cur, ok := parseVersion(out.Current); ok {
			if latest, ok := parseVersion(out.Latest.Tag); ok {
				newer := compareVersions(latest, cur) > 0
				out.UpdateAvailable = &newer
			}
		}
		out.Notice = out.Enabled && out.UpdateAvailable != nil && *out.UpdateAvailable && out.DismissedTag != out.Latest.Tag
	}
	return out
}

// CheckForUpdate reads the release feed now and records what it found.
func CheckForUpdate(ctx context.Context, db *gorm.DB) (UpdateStatus, error) {
	var s UpdateCheckSettings
	readSection(ctx, db, updateCheckSection, &s)
	if !s.Enabled || s.Repository == "" {
		return UpdateStatus{}, ErrUpdateCheckOff
	}
	var st UpdateCheckState
	readSection(ctx, db, updateStateSection, &st)
	now := time.Now().UTC()
	st.CheckedAt = &now
	latest, security, err := fetchReleases(ctx, s.Repository, buildinfo.Get().Version)
	st.Error = ""
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Latest, st.Security = latest, security
	}
	if werr := writeSection(ctx, db, updateStateSection, st); werr != nil {
		return UpdateStatus{}, werr
	}
	return GetUpdateStatus(ctx, db), err
}

// fetchReleases returns the newest published release, and whether any release
// newer than current says it fixes a security problem.
func fetchReleases(ctx context.Context, repo, current string) (*ReleaseInfo, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(releaseFeed, repo), nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "maburvm-panel")
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("the release feed could not be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("the release feed answered %s", resp.Status)
	}
	var feed []struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&feed); err != nil {
		return nil, false, fmt.Errorf("the release feed could not be read: %w", err)
	}
	cur, curOK := parseVersion(current)
	var best *ReleaseInfo
	var bestV [3]int
	security := false
	for _, r := range feed {
		v, ok := parseVersion(r.TagName)
		if r.Draft || r.Prerelease || !ok {
			continue
		}
		if curOK && compareVersions(v, cur) > 0 && mentionsSecurity(r.Name+" "+r.Body) {
			security = true
		}
		if best == nil || compareVersions(v, bestV) > 0 {
			notes := r.Body
			if len(notes) > 8000 {
				notes = notes[:8000] + "…"
			}
			url := r.HTMLURL
			if !strings.HasPrefix(url, "https://") {
				url = "" // shown as a link to the Owner: never a javascript: or plain-http one
			}
			best, bestV = &ReleaseInfo{Tag: r.TagName, Name: r.Name, URL: url, PublishedAt: r.PublishedAt, Notes: notes}, v
		}
	}
	if best == nil {
		return nil, false, errors.New("the repository has no published release")
	}
	return best, security, nil
}

func mentionsSecurity(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "security") || strings.Contains(s, "cve-")
}

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// parseVersion reads the release part of a version: v1.2.3, 1.2.3-rc1, or
// what git describe prints after a release (v1.2.3-4-gabcdef).
func parseVersion(s string) ([3]int, bool) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

func compareVersions(a, b [3]int) int { return slices.Compare(a[:], b[:]) }

// RunUpdateChecker checks the release feed a few minutes after start and then
// daily, while the Owner has the notice on.
func RunUpdateChecker(ctx context.Context, db *gorm.DB) {
	t := time.NewTimer(3 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := CheckForUpdate(ctx, db); err != nil && !errors.Is(err, ErrUpdateCheckOff) {
				slog.Default().Warn("update notice: the release feed could not be checked", "error", err)
			}
			t.Reset(24 * time.Hour)
		}
	}
}
