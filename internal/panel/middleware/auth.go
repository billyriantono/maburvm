package middleware

import (
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

// JWT Claims keys for cookie storage
const (
	AccessTokenCookieName = "access_token"
)

// AccessTokenExpiry is the lifetime of the panel's single stateless access JWT.
// There is no refresh stack, so this is the full session length.
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
	jwt.RegisteredClaims
}

// UserContext represents the authenticated user context
type UserContext struct {
	ID          uuid.UUID
	Email       string
	Role        models.UserRole
	Permissions []string
	SessionID   string
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
			"vm:lifecycle", "vm:console",
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
	if err := db.Where("id = ?", key.UserID).First(&user).Error; err != nil || user.DeletedAt.Valid {
		return nil, false
	}
	if len(user.IPWhitelist) > 0 && !isIPWhitelisted(clientIP, user.IPWhitelist) {
		return nil, false
	}
	// Best-effort last-used bookkeeping; never blocks auth.
	db.Model(&models.APIKey{}).Where("id = ?", key.ID).Update("last_used_at", time.Now())

	return &UserContext{
		ID:          user.ID,
		Email:       user.Email,
		Role:        user.Role,
		Permissions: GetPermissionsForRole(user.Role),
	}, true
}

// RequireAuth middleware validates the access token and extracts user claims
func RequireAuth(db *gorm.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// API-key auth (automation clients) takes precedence when a key is
			// explicitly presented; an invalid key is rejected, not fallen through.
			if token, ok := extractAPIToken(c); ok {
				userCtx, ok := authenticateAPIKey(db, token, c.RealIP())
				if !ok {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"error":   "Unauthorized",
						"message": "Invalid or expired API key",
					})
				}
				c.Set(UserContextKey, userCtx)
				return next(c)
			}

			// Try to extract token from cookie first, then header
			var tokenString string
			var err error

			tokenString, err = extractTokenFromCookie(c, AccessTokenCookieName)
			if err != nil {
				tokenString, err = extractTokenFromHeader(c)
				if err != nil {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"error":   "Unauthorized",
						"message": "Access token required",
					})
				}
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

				// Check if user is soft-deleted
				if user.DeletedAt.Valid {
					return c.JSON(http.StatusUnauthorized, map[string]interface{}{
						"error":   "Unauthorized",
						"message": "User account is deactivated",
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

				// Enforce per-user IP whitelist (opt-in: empty list allows any IP).
				if len(user.IPWhitelist) > 0 && !isIPWhitelisted(c.RealIP(), user.IPWhitelist) {
					return c.JSON(http.StatusForbidden, map[string]interface{}{
						"error":   "Forbidden",
						"message": "Your IP address is not whitelisted for this account",
					})
				}

				// Create user context from DB user
				userCtx := &UserContext{
					ID:          user.ID,
					Email:       user.Email,
					Role:        user.Role,
					Permissions: GetPermissionsForRole(user.Role),
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

			return next(c)
		}
	}
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
	if !ok {
		return false
	}

	for _, p := range user.Permissions {
		if p == permission || p == "admin:access" {
			return true
		}
	}
	return false
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
// validated by RequireAuth, IS the session.
func GenerateAccessToken(user *models.User) (string, error) {
	secret, err := GetJWTSecret()
	if err != nil {
		return "", fmt.Errorf("failed to resolve JWT secret: %w", err)
	}
	now := time.Now()
	claims := JWTClaims{
		UserID:      user.ID.String(),
		Email:       user.Email,
		Role:        string(user.Role),
		Permissions: GetPermissionsForRole(user.Role),
		TokenType:   "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   user.ID.String(),
			Issuer:    "maburvm-panel",
			ID:        uuid.New().String(),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}
