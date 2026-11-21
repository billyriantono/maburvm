package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// captchaVerifyURL is each provider's server-side check. Both take the same
// form (secret, response, remoteip) and answer {"success": bool}.
var captchaVerifyURL = map[string]string{
	"turnstile": "https://challenges.cloudflare.com/turnstile/v0/siteverify",
	"hcaptcha":  "https://api.hcaptcha.com/siteverify",
}

var captchaHTTP = &http.Client{Timeout: 10 * time.Second}

// captchaGate stops a sign-in or password reset that needs the captcha and
// has not passed it. It is needed once enough attempts from this address, or
// for this email, failed lately. true when it answered the request.
func captchaGate(c echo.Context, db *gorm.DB, email, token string) (bool, error) {
	p := middleware.LoadSignInPolicy(db)
	if !p.CaptchaOn() {
		return false, nil
	}
	ctx := c.Request().Context()
	if p.CaptchaAfterFailures > 0 &&
		service.RecentFailures(ctx, db, c.RealIP(), email, time.Now().Add(-middleware.CaptchaWindow)) < int64(p.CaptchaAfterFailures) {
		return false, nil
	}
	body := map[string]string{
		"code":     "captcha_required",
		"provider": p.CaptchaProvider,
		"site_key": p.CaptchaSiteKey,
		"error":    "Complete the check below, then try again.",
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return true, c.JSON(http.StatusBadRequest, body)
	}
	secret, err := models.OpenSecret(p.CaptchaSecret)
	if err == nil {
		err = verifyCaptcha(ctx, p.CaptchaProvider, secret, token, c.RealIP())
	}
	if err != nil {
		slog.Default().Warn("sign-in captcha not passed", "provider", p.CaptchaProvider, "ip", c.RealIP(), "error", err)
		body["code"], body["error"] = "captcha_failed", "The check did not pass. Try it again."
		return true, c.JSON(http.StatusBadRequest, body)
	}
	return false, nil
}

// verifyCaptcha asks the provider whether token is a passed check.
func verifyCaptcha(ctx context.Context, provider, secret, token, ip string) error {
	endpoint, ok := captchaVerifyURL[provider]
	if !ok {
		return fmt.Errorf("unknown captcha provider %q", provider)
	}
	form := url.Values{"secret": {secret}, "response": {token}}
	if ip != "" {
		form.Set("remoteip", ip)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := captchaHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Success bool     `json:"success"`
		Codes   []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return fmt.Errorf("%s: %s", provider, resp.Status)
	}
	if !out.Success {
		return errors.New("rejected: " + strings.Join(out.Codes, ","))
	}
	return nil
}
