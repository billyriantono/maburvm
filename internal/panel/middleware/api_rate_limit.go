package middleware

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"
	"gorm.io/gorm"
)

// DefaultAPIRequestsPerMinute applies until an operator sets "API requests per
// minute" (Settings, System, API). It is far above what the web UI's polling
// or the billing module need, and still stops one account or key from
// saturating the panel and, behind it, the nodes.
const DefaultAPIRequestsPerMinute = 1200

const apiRateCountedKey = "api_rate_counted"

// APIRequestsPerMinute is the operator's per-user/per-key limit; 0 = off.
func APIRequestsPerMinute(db *gorm.DB) int {
	if db == nil {
		return DefaultAPIRequestsPerMinute
	}
	var raw string
	if err := db.Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = 'api'").Scan(&raw).Error; err != nil || raw == "" {
		return DefaultAPIRequestsPerMinute
	}
	var cfg struct {
		PerMinute *int `json:"apiRequestsPerMinute"`
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil || cfg.PerMinute == nil || *cfg.PerMinute < 0 {
		return DefaultAPIRequestsPerMinute
	}
	return *cfg.PerMinute
}

// ponytail: in-memory buckets, so the limit is per panel process and resets on
// restart; move them to Postgres or Redis if the panel ever runs replicated.
type apiLimiter struct {
	mu      sync.Mutex
	perMin  int
	readAt  time.Time
	buckets map[string]*apiBucket
}

type apiBucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func newAPILimiter() *apiLimiter { return &apiLimiter{buckets: map[string]*apiBucket{}} }

// apiLimits is shared by every RequireAuth instance (one per route group).
var apiLimits = newAPILimiter()

// allow takes one request from identity's bucket, or says how long to wait.
// The setting is re-read every 30 seconds; a change starts every bucket afresh.
func (l *apiLimiter) allow(db *gorm.DB, identity string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	stale := now.Sub(l.readAt) > 30*time.Second
	l.mu.Unlock()
	perMin := -1
	if stale {
		perMin = APIRequestsPerMinute(db)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if stale {
		if perMin != l.perMin {
			l.perMin, l.buckets = perMin, map[string]*apiBucket{}
		}
		l.readAt = now
		for k, b := range l.buckets {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	if l.perMin <= 0 {
		return true, 0
	}
	b := l.buckets[identity]
	if b == nil {
		// Ten seconds' worth may arrive at once: a page load fires a dozen calls.
		b = &apiBucket{lim: rate.NewLimiter(rate.Limit(float64(l.perMin)/60), max(l.perMin/6, 1))}
		l.buckets[identity] = b
	}
	b.seen = now
	r := b.lim.ReserveN(now, 1)
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return false, d
	}
	return true, 0
}

// limitAPI answers 429 with Retry-After when identity is over its limit, and
// counts a request only once even when RequireAuth runs twice on a route.
func limitAPI(c echo.Context, db *gorm.DB, identity string) (bool, error) {
	if c.Get(apiRateCountedKey) != nil {
		return false, nil
	}
	c.Set(apiRateCountedKey, true)
	ok, wait := apiLimits.allow(db, identity, time.Now())
	if ok {
		return false, nil
	}
	secs := int(math.Ceil(wait.Seconds()))
	c.Response().Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
	return true, c.JSON(http.StatusTooManyRequests, map[string]interface{}{
		"error":   "Too Many Requests",
		"code":    "rate_limited",
		"message": "Too many requests from this account. Try again in " + strconv.Itoa(max(secs, 1)) + " seconds.",
	})
}
