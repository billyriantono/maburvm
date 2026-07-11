package middleware

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// LoginRateLimiter throttles authentication attempts per client IP to blunt
// online password / 2FA brute-force attacks. Without it, an attacker can try
// unlimited email+password combinations against /auth/login.
//
// Defaults: an average of 0.5 requests/sec (one attempt every ~2s) with a burst
// of 10, so a legitimate user retrying a typo is never blocked, but automated
// guessing is capped to a few hundred attempts/hour per IP. Idle IP buckets are
// evicted after ExpiresIn to bound memory.
func LoginRateLimiter() echo.MiddlewareFunc {
	store := echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
		Rate:      rate.Limit(0.5),
		Burst:     10,
		ExpiresIn: 3 * time.Minute,
	})

	return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: store,
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		ErrorHandler: func(c echo.Context, err error) error {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "rate limiter error",
			})
		},
		DenyHandler: func(c echo.Context, identifier string, err error) error {
			return c.JSON(http.StatusTooManyRequests, map[string]string{
				"error": "Too many login attempts. Please wait a moment and try again.",
			})
		},
	})
}
