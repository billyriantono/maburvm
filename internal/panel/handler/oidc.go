package handler

// "Sign in with SSO" through any OpenID Connect provider: authorization code
// flow with PKCE, email taken from the provider's userinfo endpoint (it must be
// verified), then matched to an existing panel user. The browser carries the
// login state in a signed, short-lived cookie, so nothing is kept in memory.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
)

const (
	oidcStateCookie = "oidc_state"
	oidcCookiePath  = "/api/v1/auth/oidc"
	oidcStateTTL    = 10 * time.Minute
	sso2FATicketTTL = 5 * time.Minute
)

type OIDCHandler struct {
	// base supplies the redirect URL and scopes; issuer, client and label are
	// read per request (System settings, Panel, else OIDC_*).
	base  config.OIDCConfig
	users *service.UserService
	http  *http.Client

	mu            sync.Mutex
	discoveredFor string
	endpoints     oidcEndpoints
}

type oidcEndpoints struct{ auth, token, user string }

// NewOIDCHandler returns the SSO handler. SSO is on while issuer, client ID
// and client secret are all set, so it can be turned on from the panel
// without a restart.
func NewOIDCHandler(cfg config.OIDCConfig, users *service.UserService) *OIDCHandler {
	return &OIDCHandler{base: cfg, users: users, http: &http.Client{Timeout: 15 * time.Second}}
}

// current is the SSO configuration in force now.
func (h *OIDCHandler) current() config.OIDCConfig {
	c := h.base
	c.Issuer, _ = service.PanelValue("oidcIssuer")
	c.ClientID, _ = service.PanelValue("oidcClientId")
	c.ClientSecret, _ = service.PanelValue("oidcClientSecret")
	c.ButtonLabel, _ = service.PanelValue("oidcButtonLabel")
	return c
}

func (h *OIDCHandler) redirectURL(c config.OIDCConfig) string {
	if c.RedirectURL != "" {
		return c.RedirectURL
	}
	return service.PublicURL() + oidcCookiePath + "/callback"
}

// Config tells the login page whether to show the SSO button.
func (h *OIDCHandler) Config(c echo.Context) error {
	cfg := h.current()
	if !cfg.Enabled() {
		return c.JSON(http.StatusOK, map[string]any{"enabled": false})
	}
	return c.JSON(http.StatusOK, map[string]any{"enabled": true, "label": cfg.ButtonLabel})
}

// discover returns the provider's endpoints, read once per issuer. A failure
// is not kept, so a provider that was briefly down works on the next attempt.
// Returned rather than re-read later, so a request never mixes two issuers.
func (h *OIDCHandler) discover(ctx context.Context, issuer string) (oidcEndpoints, error) {
	issuer = strings.TrimRight(issuer, "/")
	h.mu.Lock()
	ep, done := h.endpoints, h.discoveredFor == issuer
	h.mu.Unlock()
	if done {
		return ep, nil
	}
	var d struct {
		Auth  string `json:"authorization_endpoint"`
		Token string `json:"token_endpoint"`
		User  string `json:"userinfo_endpoint"`
	}
	if err := h.getJSON(ctx, issuer+"/.well-known/openid-configuration", "", &d); err != nil {
		return oidcEndpoints{}, err
	}
	if d.Auth == "" || d.Token == "" || d.User == "" {
		return oidcEndpoints{}, errors.New("incomplete OIDC discovery document")
	}
	ep = oidcEndpoints{auth: d.Auth, token: d.Token, user: d.User}
	h.mu.Lock()
	h.discoveredFor, h.endpoints = issuer, ep
	h.mu.Unlock()
	return ep, nil
}

// Login sends the browser to the provider.
func (h *OIDCHandler) Login(c echo.Context) error {
	cfg := h.current()
	if !cfg.Enabled() {
		return c.Redirect(http.StatusFound, "/login?sso=error")
	}
	ep, err := h.discover(c.Request().Context(), cfg.Issuer)
	if err != nil {
		slog.Default().Error("oidc discovery failed", "error", err)
		return c.Redirect(http.StatusFound, "/login?sso=error")
	}
	st := oidcState{State: randomString(24), Verifier: randomString(48), Exp: time.Now().Add(oidcStateTTL).Unix()}
	value, err := signJSON("oidc-state", st)
	if err != nil {
		return c.Redirect(http.StatusFound, "/login?sso=error")
	}
	// Lax: the callback arrives as a top-level navigation from the provider.
	c.SetCookie(&http.Cookie{Name: oidcStateCookie, Value: value, Path: oidcCookiePath,
		MaxAge: int(oidcStateTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	sum := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{
		"client_id":             {cfg.ClientID},
		"response_type":         {"code"},
		"scope":                 {cfg.Scopes},
		"redirect_uri":          {h.redirectURL(cfg)},
		"state":                 {st.State},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	return c.Redirect(http.StatusFound, ep.auth+"?"+q.Encode())
}

type oidcState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Exp      int64  `json:"e"`
}

// Callback finishes the provider round trip and signs the user in.
func (h *OIDCHandler) Callback(c echo.Context) error {
	c.SetCookie(&http.Cookie{Name: oidcStateCookie, Path: oidcCookiePath, MaxAge: -1, HttpOnly: true, Secure: true})
	var st oidcState
	ck, err := c.Cookie(oidcStateCookie)
	if err != nil || verifyJSON("oidc-state", ck.Value, &st) != nil || time.Now().Unix() > st.Exp ||
		c.QueryParam("state") == "" || !hmac.Equal([]byte(c.QueryParam("state")), []byte(st.State)) {
		return c.Redirect(http.StatusFound, "/login?sso=expired")
	}
	if c.QueryParam("error") != "" || c.QueryParam("code") == "" {
		slog.Default().Warn("oidc provider returned an error", "error", c.QueryParam("error"))
		return c.Redirect(http.StatusFound, "/login?sso=error")
	}
	cfg := h.current()
	if !cfg.Enabled() {
		return c.Redirect(http.StatusFound, "/login?sso=error")
	}
	ep, err := h.discover(c.Request().Context(), cfg.Issuer)
	if err != nil {
		return c.Redirect(http.StatusFound, "/login?sso=error")
	}
	email, err := h.email(c.Request().Context(), cfg, ep, c.QueryParam("code"), st.Verifier)
	if err != nil {
		slog.Default().Error("oidc sign-in failed", "error", err)
		return c.Redirect(http.StatusFound, "/login?sso=error")
	}

	user, token, needs2FA, err := h.users.SSOLogin(email, c.RealIP(), c.Request().UserAgent())
	if err != nil || !needs2FA {
		h.recordSignIn(c, user, email, middleware.SignInSSO, err)
	}
	switch {
	case errors.Is(err, service.ErrNoSSOAccount):
		slog.Default().Warn("sso sign-in refused: no panel user", "email", email, "ip", c.RealIP())
		return c.Redirect(http.StatusFound, "/login?sso=noaccount")
	case errors.Is(err, service.ErrIPNotWhitelisted):
		return c.Redirect(http.StatusFound, "/login?sso=ip")
	case errors.Is(err, service.ErrAccountSuspended):
		return c.Redirect(http.StatusFound, "/login?sso=suspended")
	case err != nil:
		slog.Default().Error("sso sign-in failed", "error", err)
		return c.Redirect(http.StatusFound, "/login?sso=error")
	case needs2FA:
		ticket, err := signJSON("sso-2fa", sso2FATicket{UserID: user.ID, Exp: time.Now().Add(sso2FATicketTTL).Unix()})
		if err != nil {
			return c.Redirect(http.StatusFound, "/login?sso=error")
		}
		// In the fragment, so it never reaches a server log.
		return c.Redirect(http.StatusFound, "/login?sso=2fa#ticket="+url.QueryEscape(ticket))
	}
	slog.Default().Info("sso sign-in", "user_id", user.ID, "ip", c.RealIP())
	middleware.SetSessionCookie(c, token, h.users.SessionLifetime())
	return c.Redirect(http.StatusFound, "/dashboard")
}

// recordSignIn adds an SSO attempt to the account's sign-in history.
func (h *OIDCHandler) recordSignIn(c echo.Context, user *models.User, email, method string, err error) {
	e := models.SignInEvent{Email: email, Method: method, Success: err == nil,
		IPAddress: c.RealIP(), UserAgent: c.Request().UserAgent()}
	if err != nil {
		e.Note = signInFailure(err)
	}
	if user != nil {
		e.UserID = &user.ID
	}
	service.RecordSignIn(c.Request().Context(), h.users.DB(), e)
}

type sso2FATicket struct {
	UserID uuid.UUID `json:"u"`
	Exp    int64     `json:"e"`
	// Method is how the sign-in started (SignInSSO when empty); Next the page
	// it lands on once the code is accepted.
	Method string `json:"m,omitempty"`
	Next   string `json:"n,omitempty"`
}

// Verify2FA completes an SSO sign-in for a user with panel two-factor on.
// Responds like the password login, session cookie included.
func (h *OIDCHandler) Verify2FA(c echo.Context) error {
	var req struct {
		Ticket   string `json:"ticket"`
		TOTPCode string `json:"totpCode"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}
	var t sso2FATicket
	if verifyJSON("sso-2fa", req.Ticket, &t) != nil || time.Now().Unix() > t.Exp {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "This sign-in expired. Start again with SSO."})
	}
	method := middleware.SignInSSO
	if t.Method == middleware.SignInBillingSSO {
		method = t.Method
	}
	user, token, err := h.users.SSOVerify2FA(t.UserID, req.TOTPCode, c.RealIP(), c.Request().UserAgent(), method)
	h.recordSignIn(c, &models.User{ID: t.UserID}, "", method, err)
	if locked, out := accountLocked(c, err); locked {
		return out
	}
	switch {
	case errors.Is(err, service.ErrIPNotWhitelisted):
		return c.JSON(http.StatusForbidden, map[string]string{"error": "IP address not whitelisted"})
	case err != nil:
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid 2FA code"})
	}
	slog.Default().Info("sso sign-in", "user_id", user.ID, "ip", c.RealIP(), "two_factor", true, "method", method)
	if method == middleware.SignInBillingSSO {
		h.auditBillingSSO(c, user, t.Next, true)
	}
	middleware.SetSessionCookie(c, token, h.users.SessionLifetime())
	return c.JSON(http.StatusOK, LoginResponse{User: user, Token: token, ExpiresIn: int(h.users.SessionLifetime().Seconds()), Redirect: t.Next})
}

// email swaps the code for a token and asks the provider whose it is.
func (h *OIDCHandler) email(ctx context.Context, cfg config.OIDCConfig, ep oidcEndpoints, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {h.redirectURL(cfg)},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", ep.token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(cfg.ClientID), url.QueryEscape(cfg.ClientSecret))
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := h.doJSON(req, &tok); err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	var info struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if err := h.getJSON(ctx, ep.user, tok.AccessToken, &info); err != nil {
		return "", fmt.Errorf("userinfo: %w", err)
	}
	if info.Email == "" || !info.Verified {
		return "", errors.New("provider returned no verified email")
	}
	return strings.ToLower(info.Email), nil
}

func (h *OIDCHandler) getJSON(ctx context.Context, u, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return h.doJSON(req, out)
}

func (h *OIDCHandler) doJSON(req *http.Request, out any) error {
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", req.URL.Host, resp.Status)
	}
	return json.Unmarshal(body, out)
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// signJSON and verifyJSON protect small values with an HMAC under the panel's
// JWT secret. The purpose string keeps one kind of value from passing as another.
func signJSON(purpose string, v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	mac, err := macFor(purpose, raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw) + "." + mac, nil
}

func verifyJSON(purpose, value string, v any) error {
	payload, mac, ok := strings.Cut(value, ".")
	if !ok {
		return errors.New("malformed value")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	want, err := macFor(purpose, raw)
	if err != nil || !hmac.Equal([]byte(want), []byte(mac)) {
		return errors.New("bad signature")
	}
	return json.Unmarshal(raw, v)
}

func macFor(purpose string, raw []byte) (string, error) {
	key, err := middleware.GetJWTSecret()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose + "\x00"))
	m.Write(raw)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}
