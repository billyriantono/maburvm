package middleware

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// RequestLogEntry is one API request as the Logs page lists it. The path is
// kept without its query string: some links carry a token there.
type RequestLogEntry struct {
	Time      time.Time `json:"time"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Route     string    `json:"route"`
	Status    int       `json:"status"`
	LatencyMS float64   `json:"latency_ms"`
	UserID    string    `json:"user_id,omitempty"`
	Email     string    `json:"email,omitempty"`
	APIKeyID  string    `json:"api_key_id,omitempty"`
	IP        string    `json:"ip"`
	RequestID string    `json:"request_id,omitempty"`
}

// RequestLogSize is how many recent requests are kept.
// ponytail: in memory, so a restart empties it and one process sees only its
// own requests; move to a table if the log must outlive restarts.
const RequestLogSize = 10000

var requestLog = struct {
	sync.Mutex
	buf  []RequestLogEntry
	next int
	full bool
}{buf: make([]RequestLogEntry, RequestLogSize)}

// requestLogSkip are requests not worth a line: probes, scrapes, and the log
// page reading itself.
func requestLogSkip(path string) bool {
	return !(strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/webhooks/")) ||
		path == "/api/v1/logs/requests"
}

func recordRequest(e RequestLogEntry) {
	requestLog.Lock()
	requestLog.buf[requestLog.next] = e
	requestLog.next = (requestLog.next + 1) % len(requestLog.buf)
	if requestLog.next == 0 {
		requestLog.full = true
	}
	requestLog.Unlock()
}

// RequestLog records every API request: method, path, status, latency, who
// made it (account, API key) and from where. Mounted globally; it reads the
// account after the route's own authentication has run.
func RequestLog() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if requestLogSkip(req.URL.Path) {
				return next(c)
			}
			start := time.Now()
			err := next(c)
			status := c.Response().Status
			if err != nil && !c.Response().Committed {
				status = 500
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				}
			}
			e := RequestLogEntry{
				Time: start.UTC(), Method: req.Method, Path: truncate(req.URL.Path, 300), Route: c.Path(),
				Status: status, LatencyMS: float64(time.Since(start).Microseconds()) / 1000,
				IP: c.RealIP(), RequestID: c.Response().Header().Get(echo.HeaderXRequestID),
			}
			if u, ok := GetUserContext(c); ok {
				e.UserID, e.Email, e.APIKeyID = u.ID.String(), u.Email, u.APIKeyID
			}
			recordRequest(e)
			return err
		}
	}
}

// RequestFilter narrows the request log. Every field is optional.
type RequestFilter struct {
	Method string
	// Status is a class ("2xx", "4xx", "5xx") or an exact code ("404").
	Status string
	// Q matches the path, account email, API key id, address or request id.
	Q            string
	MinLatencyMS float64
}

func (f RequestFilter) match(e RequestLogEntry) bool {
	if f.Method != "" && !strings.EqualFold(f.Method, e.Method) {
		return false
	}
	if s := f.Status; s != "" {
		code := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "xx")
		if got := strconv.Itoa(e.Status); len(code) == 1 && !strings.HasPrefix(got, code) || len(code) == 3 && got != code {
			return false
		}
	}
	if f.MinLatencyMS > 0 && e.LatencyMS < f.MinLatencyMS {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Q)); q != "" {
		hay := strings.ToLower(e.Path + " " + e.Email + " " + e.APIKeyID + " " + e.IP + " " + e.RequestID + " " + e.UserID)
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

// RecentRequests returns the matching requests, newest first: one page of
// them and how many matched in all.
func RecentRequests(f RequestFilter, limit, offset int) ([]RequestLogEntry, int) {
	requestLog.Lock()
	n := requestLog.next
	if requestLog.full {
		n = len(requestLog.buf)
	}
	out := []RequestLogEntry{}
	total := 0
	for i := 0; i < n; i++ {
		e := requestLog.buf[(requestLog.next-1-i+len(requestLog.buf))%len(requestLog.buf)]
		if !f.match(e) {
			continue
		}
		if total >= offset && len(out) < limit {
			out = append(out, e)
		}
		total++
	}
	requestLog.Unlock()
	return out, total
}
