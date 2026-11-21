package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/secret"
	"gorm.io/gorm"
)

// AccessTokenCookieName is the browser session cookie: HttpOnly, set by the
// panel at sign-in. The name is the one the web app always used, so a session
// started before the cookie became HttpOnly keeps working until it expires.
const AccessTokenCookieName = "accessToken"

// AccessTokenExpiry is the built-in lifetime of the panel's single stateless
// access JWT. There is no refresh stack, so this is the full session length
// unless the operator set another one (SignInPolicy.SessionHours).
const AccessTokenExpiry = 24 * time.Hour

// Context keys
const (
	UserContextKey   = "user"
	ClaimsContextKey = "claims"
)

// JWTClaims represents the custom JWT claims structure
type JWTClaims struct {
	UserID      string   `json:"user_id"`
	Email       string   `json:"email"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	TokenType   string   `json:"token_type"`
	// SessionID names the user_sessions row; empty on tokens minted before
	// sessions were recorded, which stay valid until they expire.
	SessionID string `json:"sid,omitempty"`
	// Impersonator is the staff account viewing the panel as this user.
	Impersonator string `json:"imp,omitempty"`
	jwt.RegisteredClaims
}

// UserContext represents the authenticated user context
type UserContext struct {
	ID          uuid.UUID
	Email       string
	Role        models.UserRole
	Permissions []string
	SessionID   string
	// APIKeyReadOnly / APIKeyRestricted describe the API key the request was
	// made with (both false for session logins). Restricted = narrower than the
	// owner (read-only or downgraded role); such keys can't manage credentials.
	APIKeyReadOnly   bool
	APIKeyRestricted bool
	// APIKeyID is the key the request was made with; "" for sessions.
	APIKeyID string
	// TwoFactorEnabled is the account's own setting. TwoFactorSetupRequired is
	// set on a session held to the setup pages until two-factor is on.
	TwoFactorEnabled       bool
	TwoFactorSetupRequired bool
	// StaffRole is a staff account's resolved role (empty for customers).
	StaffRole StaffRole
	// ImpersonatorID/Email name the staff account viewing as this user.
	ImpersonatorID    string
	ImpersonatorEmail string
}

// GetJWTSecret returns the JWT signing key. It resolves via secret.JWTSecret:
// the JWT_SECRET_KEY env var if set, otherwise a per-install random key that is
// generated once and persisted to the data dir. It never falls back to a
// guessable constant (which would let anyone forge admin tokens). Resolution is
// fail-closed: an unreadable/malformed/invalid secrets file or a failed durable
// write is returned as an error rather than silently regenerating in memory.
func GetJWTSecret() ([]byte, error) {
	s, err := secret.JWTSecret()
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// GetPermissionsForRole returns permissions based on user role
func GetPermissionsForRole(role models.UserRole) []string {
	switch role {
	case models.RoleAdmin:
		return []string{
			"vm:create", "vm:read", "vm:update", "vm:delete",
			"node:create", "node:read", "node:update", "node:delete",
			"user:create", "user:read", "user:update", "user:delete",
			"backup:create", "backup:read", "backup:update", "backup:delete",
			"network:create", "network:read", "network:update", "network:delete",
			"floating_ip:read", "floating_ip:update", "floating_ip:create", "floating_ip:delete",
			"vpc:read", "vpc:create", "vpc:delete",
			"firewall:create", "firewall:read", "firewall:update", "firewall:delete",
			"snapshot:create", "snapshot:read", "snapshot:update", "snapshot:delete",
			"audit:read",
			"admin:access",
		}
	case models.RoleClient:
		// Clients may fully operate their OWN VMs. Every vm:*/backup:*/snapshot:*
		// handler enforces per-resource ownership (see VMHandler.authorizeVM /
		// BackupHandler.authorizeVM), so lifecycle and console access here only ever
		// applies to the caller's own resources — never other tenants'.
		return []string{
			"vm:create", "vm:read", "vm:update", "vm:delete",
			"vm:lifecycle", "vm:console", "vm:power", "vm:rescue", "vm:credentials",
			"backup:create", "backup:read", "backup:update", "backup:delete",
			"snapshot:create", "snapshot:read", "snapshot:update", "snapshot:delete",
			// Floating IPs get their own permissions rather than reusing network:*,
			// precisely because network:read would expose global IPAM (see below).
			// These are safe for a client: every floating-IP handler filters by, or
			// checks, ownership — a client sees only floating IPs allocated to them
			// and can only point one at a VM they own. Allocating a new address from
			// a pool and releasing one back to it remain admin:access, so a client
			// can move what they have but never consume a node's address space.
			"floating_ip:read", "floating_ip:update",
			// Ordering an address for yourself, and releasing it again so the
			// billing stops. Choosing the pool or allocating on someone else's
			// behalf stays with an administrator.
			"floating_ip:create", "floating_ip:delete",
			// Tenant VPCs. Safe for a client for the same reason as floating IPs:
			// every handler scopes by owner, and two tenants may hold identical
			// subnets without interfering, so nothing about another tenant's
			// topology is exposed.
			"vpc:read", "vpc:create", "vpc:delete",
			// Firewall groups: the handlers show and change only the caller's own.
			"firewall_group:read", "firewall_group:update",
			// NOTE: no "network:read". That permission gates GLOBAL, un-tenant-filtered
			// IPAM (/ipam/pools/*) and DNS reads, which would let any client enumerate
			// every tenant's IP↔VM↔node↔rDNS mapping. A client's own VM interfaces are
			// served by vm:read + ownership (GET /vms/:id/networks), not network:read.
		}
	default:
		return []string{"vm:read"}
	}
}

// extractTokenFromCookie extracts the JWT token from the specified cookie
func extractTokenFromCookie(c echo.Context, cookieName string) (string, error) {
	cookie, err := c.Cookie(cookieName)
	if err != nil {
		return "", err
	}
	return cookie.Value, nil
}

// extractTokenFromHeader extracts the JWT token from the Authorization header
func extractTokenFromHeader(c echo.Context) (string, error) {
	authHeader := c.Request().Header.Get("Authorization")
	if authHeader == "" {
		return "", errors.New("authorization header missing")
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return "", errors.New("invalid authorization header format")
	}

	return parts[1], nil
}

// ParseAndValidateToken parses and validates a JWT token string
func ParseAndValidateToken(tokenString string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		secret, serr := GetJWTSecret()
		if serr != nil {
			return nil, fmt.Errorf("failed to resolve JWT secret: %w", serr)
		}
		return secret, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token claims")
}

// extractAPIToken returns a presented API token from either the X-API-Key header
// or an "Authorization: Bearer mvk_..." header, and whether one was found. A
// Bearer value that is not an API token (e.g. a JWT) is left for the JWT path.
func extractAPIToken(c echo.Context) (string, bool) {
	if v := strings.TrimSpace(c.Request().Header.Get("X-API-Key")); v != "" {
		return v, true
	}
	parts := strings.SplitN(c.Request().Header.Get("Authorization"), " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		if v := strings.TrimSpace(parts[1]); strings.HasPrefix(v, models.APITokenPrefix) {
			return v, true
		}
	}
	return "", false
}

// APIAccessEnabled reports whether API keys may be used: the "Enable API
// access" switch under Settings, System, API. On unless an operator turned it
// off; a missing or unreadable setting never locks the API.
func APIAccessEnabled(db *gorm.DB) bool {
	if db == nil {
		return true
	}
	var raw string
	if err := db.Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = 'api'").Scan(&raw).Error; err != nil || raw == "" {
		return true
	}
	var cfg struct {
		EnableAPI *bool `json:"enableApi"`
	}
	_ = json.Unmarshal([]byte(raw), &cfg)
	return cfg.EnableAPI == nil || *cfg.EnableAPI
}

// authenticateAPIKey validates a presented API token against the database and,
// on success, returns a populated UserContext. This lets automation clients call
// the API with a long-lived key instead of a session JWT. The lookup is done
// directly here (not via the service layer) to keep the middleware dependency-free.
func authenticateAPIKey(db *gorm.DB, token string, clientIP string) (*UserContext, bool) {
	if db == nil {
		return nil, false
	}
	var key models.APIKey
	if err := db.Where("key_hash = ?", models.HashAPIToken(token)).First(&key).Error; err != nil {
		return nil, false
	}
	if !key.IsValid() {
		return nil, false
	}
	var user models.User
	if err := db.Where("id = ?", key.UserID).First(&user).Error; err != nil || user.DeletedAt.Valid || user.Locked() {
		return nil, false
	}
	if len(user.IPWhitelist) > 0 && !isIPWhitelisted(clientIP, user.IPWhitelist) {
		return nil, false
	}
	// Best-effort last-used bookkeeping; never blocks auth.
	db.Model(&models.APIKey{}).Where("id = ?", key.ID).Update("last_used_at", time.Now())

	role := key.EffectiveRole(user.Role)
	u := &UserContext{
		ID:               user.ID,
		Email:            user.Email,
		Role:             role,
		Permissions:      GetPermissionsForRole(role),
		APIKeyReadOnly:   key.ReadOnly,
		APIKeyRestricted: key.ReadOnly || role != user.Role,
		APIKeyID:         key.ID,
	}
	// A staff key does what its owner's staff role allows, as it is now.
	if role == models.RoleAdmin {
		u.StaffRole, u.Permissions = ResolveStaffRole(context.Background(), db, &user)
	}
	return u, true
}

// allowReadOnlyKeyCtx marks a route whose unsafe method is only a transport
// (the MCP endpoint), so a read-only key may reach it; what it does inside is
// re-checked per call.
const allowReadOnlyKeyCtx = "api_key_read_only_ok"

// AllowReadOnlyKey lets read-only API keys reach the route below it even with
// POST. Use only on endpoints that re-enter RequireAuth for their real work.
func AllowReadOnlyKey(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		c.Set(allowReadOnlyKeyCtx, true)
		return next(c)
	}
}

// apiKeyScopeError returns why a scoped key may not make this request, or "".
func apiKeyScopeError(c echo.Context, u *UserContext) string {
	req := c.Request()
	if u.APIKeyReadOnly && c.Get(allowReadOnlyKeyCtx) == nil &&
		req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions {
		return "This API key is read-only"
	}
	// A restricted key must not be able to mint a broader key or take over
	// the account (password, 2FA, profile).
	if u.APIKeyRestricted {
		for _, p := range []string{"/api/v1/api-keys", "/api/v1/settings"} {
			if req.URL.Path == p || strings.HasPrefix(req.URL.Path, p+"/") {
				return "This API key may not manage account settings or API keys"
			}
		}
	}
	return ""
}

// RequireAuth middleware validates the access token and extracts user claims
func RequireAuth(db *gorm.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// API-key auth (automation clients) takes precedence when a key is
			// explicitly presented; an invalid key is rejected, not fallen through.
			if token, ok := extractAPIToken(c); ok {
				if !APIAccessEnabled(db) {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error":   "Forbidden",
						"message": "API access is turned off on this panel",
					})
				}
				userCtx, ok := authenticateAPIKey(db, token, c.RealIP())
				if !ok {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"error":   "Unauthorized",
						"message": "Invalid or expired API key",
					})
				}
				if msg := apiKeyScopeError(c, userCtx); msg != "" {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error":   "Forbidden",
						"message": msg,
					})
				}
				if limited, err := limitAPI(c, db, "key:"+userCtx.APIKeyID); limited {
					return err
				}
				c.Set(UserContextKey, userCtx)
				return next(c)
			}

			// The session cookie first (the browser), then a Bearer header.
			var tokenString string
			var err error

			tokenString, err = extractTokenFromCookie(c, AccessTokenCookieName)
			fromCookie := err == nil
			if err != nil {
				tokenString, err = extractTokenFromHeader(c)
				if err != nil {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"error":   "Unauthorized",
						"message": "Access token required",
					})
				}
			}
			handler := next
			if fromCookie {
				// A cookie is sent by the browser on its own, so a cross-site
				// page must not be able to make a change with it (SameSite=Lax
				// already stops most of that; this covers the rest).
				if crossSiteWrite(c.Request()) {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error":   "Forbidden",
						"message": "Cross-site request refused",
					})
				}
				// A session the panel no longer accepts is dropped from the
				// browser, so the sign-in page does not bounce straight back.
				authed := false
				c.Response().Before(func() {
					if !authed && c.Response().Status == http.StatusUnauthorized {
						ClearSessionCookie(c)
					}
				})
				handler = func(c echo.Context) error { authed = true; return next(c) }
			}

			// Parse and validate the token
			claims, err := ParseAndValidateToken(tokenString)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"error":   "Unauthorized",
					"message": "Invalid or expired token",
				})
			}

			// Validate token type (skip if not set — backward compat with user service tokens)
			if claims.TokenType != "" && claims.TokenType != "access" {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"error":   "Unauthorized",
					"message": "Invalid token type",
				})
			}

			// Parse user ID
			userID, err := uuid.Parse(claims.UserID)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"error":   "Unauthorized",
					"message": "Invalid user ID in token",
				})
			}

			// Fetch user from database to ensure they still exist and are active
			// If no db is provided, skip DB validation (JWT-only mode)
			if db != nil {
				var user models.User
				if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return c.JSON(http.StatusUnauthorized, map[string]interface{}{
							"error":   "Unauthorized",
							"message": "User not found",
						})
					}
					return c.JSON(http.StatusInternalServerError, map[string]interface{}{
						"error":   "Internal Server Error",
						"message": "Failed to validate user",
					})
				}

				// Check if user is soft-deleted, or a temporary account that ran out
				if user.DeletedAt.Valid || user.Expired() {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"error":   "Unauthorized",
						"message": "User account is deactivated",
					})
				}
				if user.Suspended() {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"error":   "Unauthorized",
						"code":    "account_suspended",
						"message": "This account is suspended. Contact support.",
					})
				}

				// Enforce logout-based revocation: reject any token minted before the
				// user's revocation cutoff. NULL cutoff (never logged out) allows all.
				// JWT `iat` is second-precision, so the token in hand at logout floors
				// below the sub-second cutoff and is correctly rejected. A re-login
				// happens in a later wall-clock second, so its iat clears the cutoff.
				if user.TokenRevokedAt != nil && claims.IssuedAt != nil &&
					claims.IssuedAt.Time.Before(*user.TokenRevokedAt) {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"error":   "Unauthorized",
						"message": "Session has been revoked",
					})
				}

				// A session the user or staff revoked ends here, whatever the token says.
				var viewedBy *uuid.UUID
				var viewSince time.Time
				if claims.SessionID != "" {
					sess, status, msg := checkSession(db, claims, user.ID)
					if sess == nil {
						return c.JSON(status, map[string]interface{}{"error": http.StatusText(status), "message": msg})
					}
					viewedBy, viewSince = sess.ImpersonatorID, sess.CreatedAt
				}
				if claims.Impersonator != "" && (viewedBy == nil || viewedBy.String() != claims.Impersonator) {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized", "message": "Session has been revoked"})
				}

				// Viewing as a customer: the staff account must still be allowed to,
				// its own IP allow-list applies, and some things stay off limits.
				allowList := user.IPWhitelist
				var staff *models.User
				if claims.Impersonator != "" {
					staff = impersonatingStaff(c.Request().Context(), db, claims.Impersonator, viewSince)
					if staff == nil {
						return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized", "message": "This view of the customer's account has ended"})
					}
					if impersonationForbidden(c.Request().Method, c.Request().URL.Path) {
						return c.JSON(http.StatusForbidden, map[string]interface{}{
							"error":   "Forbidden",
							"code":    "impersonation_forbidden",
							"message": "Not while viewing as a customer: their password, email, two-factor, sessions, API keys and initial passwords stay theirs.",
						})
					}
					allowList = staff.IPWhitelist
				}

				// Enforce per-user IP whitelist (opt-in: empty list allows any IP).
				if len(allowList) > 0 && !isIPWhitelisted(c.RealIP(), allowList) {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error":   "Forbidden",
						"message": "Your IP address is not whitelisted for this account",
					})
				}

				// Staff must have two-factor on when the operator requires it. Until
				// then the session reaches only the pages that set it up. API keys
				// are automation and are not held to this.
				setupRequired := user.Role == models.RoleAdmin && !user.TwoFactorEnabled && LoadSignInPolicy(db).RequireStaff2FA
				if setupRequired && !isTwoFactorSetupPath(c.Request().URL.Path) {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error":   "Forbidden",
						"code":    "two_factor_required",
						"message": "Set up two-factor authentication to continue",
					})
				}

				// Create user context from DB user
				userCtx := &UserContext{
					ID:                     user.ID,
					Email:                  user.Email,
					Role:                   user.Role,
					Permissions:            GetPermissionsForRole(user.Role),
					SessionID:              claims.SessionID,
					TwoFactorEnabled:       user.TwoFactorEnabled,
					TwoFactorSetupRequired: setupRequired,
				}
				if user.Role == models.RoleAdmin {
					userCtx.StaffRole, userCtx.Permissions = ResolveStaffRole(c.Request().Context(), db, &user)
				}
				if staff != nil {
					userCtx.ImpersonatorID, userCtx.ImpersonatorEmail = staff.ID.String(), staff.Email
					c.SetRequest(c.Request().WithContext(models.WithImpersonator(c.Request().Context(),
						models.Impersonator{ID: userCtx.ImpersonatorID, Email: staff.Email})))
				}

				c.Set(UserContextKey, userCtx)
				c.Set(ClaimsContextKey, claims)
			} else {
				// JWT-only mode: create context from token claims
				userCtx := &UserContext{
					ID:          userID,
					Email:       claims.Email,
					Role:        models.UserRole(claims.Role),
					Permissions: GetPermissionsForRole(models.UserRole(claims.Role)),
				}

				c.Set(UserContextKey, userCtx)
				c.Set(ClaimsContextKey, claims)
			}

			if u, _ := GetUserContext(c); u != nil {
				if limited, err := limitAPI(c, db, "user:"+u.ID.String()); limited {
					return err
				}
			}
			return handler(c)
		}
	}
}

// crossSiteWrite reports a state-changing request a browser marked as coming
// from another origin. The web app is always same-origin; "same-site" is
// refused too, because SameSite=Lax does not stop a sibling subdomain (a VM
// hostname under the panel's domain). Browsers that send no Sec-Fetch-Site are
// left to SameSite=Lax.
func crossSiteWrite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "cross-site" || site == "same-site"
}

// SetSessionCookie stores a session in the browser: HttpOnly so no script can
// read it, SameSite=Lax, and Secure whenever the browser reached the panel
// over https (directly or through the proxy, per X-Forwarded-Proto).
func SetSessionCookie(c echo.Context, token string, lifetime time.Duration) {
	c.SetCookie(&http.Cookie{Name: AccessTokenCookieName, Value: token, Path: "/", MaxAge: int(lifetime.Seconds()),
		HttpOnly: true, Secure: requestIsHTTPS(c), SameSite: http.SameSiteLaxMode})
}

// ClearSessionCookie removes the session cookie from the browser.
func ClearSessionCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{Name: AccessTokenCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: requestIsHTTPS(c), SameSite: http.SameSiteLaxMode})
}

// DropSessionCookie clears the session cookie on the way in, so signing out
// ends the browser's session even when the panel refuses it with a 403 (an IP
// allow-list): no script can remove an HttpOnly cookie.
func DropSessionCookie(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if !crossSiteWrite(c.Request()) {
			ClearSessionCookie(c)
		}
		return next(c)
	}
}

func requestIsHTTPS(c echo.Context) bool {
	if c.IsTLS() {
		return true
	}
	proto, _, _ := strings.Cut(c.Request().Header.Get(echo.HeaderXForwardedProto), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// OptionalAuth middleware attempts to authenticate but doesn't require it
func OptionalAuth(db *gorm.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Try to extract token from cookie first, then header
			var tokenString string
			var err error

			tokenString, err = extractTokenFromCookie(c, AccessTokenCookieName)
			if err != nil {
				tokenString, err = extractTokenFromHeader(c)
			}

			// If no token found, continue as unauthenticated
			if err != nil {
				c.Set(UserContextKey, nil)
				c.Set(ClaimsContextKey, nil)
				return next(c)
			}

			// Parse and validate the token
			claims, err := ParseAndValidateToken(tokenString)
			if err != nil {
				// Invalid token, continue as unauthenticated
				c.Set(UserContextKey, nil)
				c.Set(ClaimsContextKey, nil)
				return next(c)
			}

			// Validate token type
			if claims.TokenType != "access" {
				c.Set(UserContextKey, nil)
				c.Set(ClaimsContextKey, nil)
				return next(c)
			}

			// Parse user ID
			userID, err := uuid.Parse(claims.UserID)
			if err != nil {
				c.Set(UserContextKey, nil)
				c.Set(ClaimsContextKey, nil)
				return next(c)
			}

			// Fetch user from database
			var user models.User
			if err := db.Where("id = ?", userID).First(&user).Error; err != nil {
				c.Set(UserContextKey, nil)
				c.Set(ClaimsContextKey, nil)
				return next(c)
			}

			// Check if user is soft-deleted
			if user.DeletedAt.Valid {
				c.Set(UserContextKey, nil)
				c.Set(ClaimsContextKey, nil)
				return next(c)
			}

			// Create user context
			userCtx := &UserContext{
				ID:          user.ID,
				Email:       user.Email,
				Role:        user.Role,
				Permissions: claims.Permissions,
			}

			// Store user and claims in context
			c.Set(UserContextKey, userCtx)
			c.Set(ClaimsContextKey, claims)

			return next(c)
		}
	}
}

// GetUserContext retrieves the authenticated user from echo context
func GetUserContext(c echo.Context) (*UserContext, bool) {
	user, ok := c.Get(UserContextKey).(*UserContext)
	return user, ok && user != nil
}

// GetClaims retrieves the JWT claims from echo context
func GetClaims(c echo.Context) (*JWTClaims, bool) {
	claims, ok := c.Get(ClaimsContextKey).(*JWTClaims)
	return claims, ok && claims != nil
}

// HasPermission checks if the authenticated user has a specific permission
func HasPermission(c echo.Context, permission string) bool {
	user, ok := GetUserContext(c)
	return ok && user.Can(permission)
}

// RequirePermission creates middleware that requires a specific permission
func RequirePermission(permission string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !HasPermission(c, permission) {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error":   "Forbidden",
					"message": "Insufficient permissions",
				})
			}
			return next(c)
		}
	}
}

// GenerateAccessToken mints the panel's one and only session credential: an HS256
// access JWT signed with the shared JWT secret, carrying the user's id/email/role/
// permissions, a jti, and an iat that logout revocation (users.token_revoked_at)
// compares against. There is no refresh/server-session stack — this token,
// validated by RequireAuth, IS the session. lifetime is the operator's session
// length (SignInPolicy.SessionLifetime); without it the built-in one applies.
func GenerateAccessToken(user *models.User, lifetime ...time.Duration) (string, error) {
	ttl := AccessTokenExpiry
	if len(lifetime) > 0 && lifetime[0] > 0 {
		ttl = lifetime[0]
	}
	return mintToken(user, "", "", ttl)
}

// mintToken signs an access token; sid and imp may be empty.
func mintToken(user *models.User, sid, imp string, ttl time.Duration) (string, error) {
	secret, err := GetJWTSecret()
	if err != nil {
		return "", fmt.Errorf("failed to resolve JWT secret: %w", err)
	}
	now := time.Now()
	claims := JWTClaims{
		UserID:       user.ID.String(),
		Email:        user.Email,
		Role:         string(user.Role),
		Permissions:  GetPermissionsForRole(user.Role),
		TokenType:    "access",
		SessionID:    sid,
		Impersonator: imp,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   user.ID.String(),
			Issuer:    "maburvm-panel",
			ID:        uuid.New().String(),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

// impersonatingStaff loads the staff account behind a view as a customer, or
// nil when it may no longer do that. A staff password change or "sign out
// everywhere" after the view opened ends the view too.
func impersonatingStaff(ctx context.Context, db *gorm.DB, id string, since time.Time) *models.User {
	var staff models.User
	if db.WithContext(ctx).Where("id = ?", id).First(&staff).Error != nil ||
		staff.Role != models.RoleAdmin || staff.Locked() ||
		(staff.TokenRevokedAt != nil && since.Before(*staff.TokenRevokedAt)) {
		return nil
	}
	if _, perms := ResolveStaffRole(ctx, db, &staff); !Can(perms, PermImpersonate) {
		return nil
	}
	return &staff
}
