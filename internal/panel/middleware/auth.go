package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// JWT Claims keys for cookie storage
const (
	AccessTokenCookieName  = "access_token"
	RefreshTokenCookieName = "refresh_token"
)

// Default token expiry durations
const (
	AccessTokenExpiry  = 15 * time.Minute
	RefreshTokenExpiry = 7 * 24 * time.Hour // 7 days
)

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

// TokenPair represents an access and refresh token pair
type TokenPair struct {
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	AccessExpiry  time.Time `json:"access_expiry"`
	RefreshExpiry time.Time `json:"refresh_expiry"`
}

// UserContext represents the authenticated user context
type UserContext struct {
	ID          uuid.UUID
	Email       string
	Role        models.UserRole
	Permissions []string
	SessionID   string
}

// GetJWTSecret retrieves the JWT secret from environment
func GetJWTSecret() []byte {
	secret := os.Getenv("JWT_SECRET_KEY")
	if secret == "" {
		secret = "your-jwt-secret-change-in-production"
	}
	return []byte(secret)
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
			"firewall:create", "firewall:read", "firewall:update", "firewall:delete",
			"snapshot:create", "snapshot:read", "snapshot:update", "snapshot:delete",
			"audit:read",
			"admin:access",
		}
	case models.RoleClient:
		return []string{
			"vm:create", "vm:read", "vm:update", "vm:delete",
			"backup:create", "backup:read", "backup:update", "backup:delete",
			"snapshot:create", "snapshot:read", "snapshot:update", "snapshot:delete",
			"network:read",
		}
	default:
		return []string{"vm:read"}
	}
}

// GenerateTokenPair generates a new access and refresh token pair for a user
func GenerateTokenPair(user *models.User, db *gorm.DB) (*TokenPair, error) {
	now := time.Now()
	accessExpiry := now.Add(AccessTokenExpiry)
	refreshExpiry := now.Add(RefreshTokenExpiry)

	// Get permissions for the user's role
	permissions := GetPermissionsForRole(user.Role)

	// Create access token claims
	accessClaims := JWTClaims{
		UserID:      user.ID.String(),
		Email:       user.Email,
		Role:        string(user.Role),
		Permissions: permissions,
		TokenType:   "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(accessExpiry),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   user.ID.String(),
			Issuer:    "maburvm-panel",
		},
	}

	// Create and sign access token
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := accessToken.SignedString(GetJWTSecret())
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// Create refresh token claims (minimal claims for refresh tokens)
	refreshClaims := JWTClaims{
		UserID:    user.ID.String(),
		TokenType: "refresh",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(refreshExpiry),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   user.ID.String(),
			Issuer:    "maburvm-panel",
			ID:        uuid.New().String(), // JTI for token revocation
		},
	}

	// Create and sign refresh token
	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString(GetJWTSecret())
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	// Store refresh token in database for revocation capability
	if db != nil {
		session := &models.Session{
			UserID:    user.ID.String(),
			Token:     refreshTokenString,
			ExpiresAt: refreshExpiry,
		}
		if err := db.Create(session).Error; err != nil {
			return nil, fmt.Errorf("failed to store refresh token: %w", err)
		}
	}

	return &TokenPair{
		AccessToken:   accessTokenString,
		RefreshToken:  refreshTokenString,
		AccessExpiry:  accessExpiry,
		RefreshExpiry: refreshExpiry,
	}, nil
}

// SetTokenCookies sets both access and refresh tokens as HTTP-only cookies
func SetTokenCookies(c echo.Context, tokens *TokenPair) {
	// Set access token cookie
	accessCookie := &http.Cookie{
		Name:     AccessTokenCookieName,
		Value:    tokens.AccessToken,
		Expires:  tokens.AccessExpiry,
		HttpOnly: true,
		Secure:   true, // Requires HTTPS
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	}
	c.SetCookie(accessCookie)

	// Set refresh token cookie
	refreshCookie := &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    tokens.RefreshToken,
		Expires:  tokens.RefreshExpiry,
		HttpOnly: true,
		Secure:   true, // Requires HTTPS
		SameSite: http.SameSiteStrictMode,
		Path:     "/api/auth/refresh", // Restricted path for refresh token
	}
	c.SetCookie(refreshCookie)
}

// ClearTokenCookies clears both token cookies (logout)
func ClearTokenCookies(c echo.Context) {
	// Clear access token cookie
	accessCookie := &http.Cookie{
		Name:     AccessTokenCookieName,
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/",
	}
	c.SetCookie(accessCookie)

	// Clear refresh token cookie
	refreshCookie := &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/api/auth/refresh",
	}
	c.SetCookie(refreshCookie)
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
		return GetJWTSecret(), nil
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

// RefreshTokenHandler handles the token refresh endpoint
// It validates the refresh token, revokes the old one, and issues a new pair
func RefreshTokenHandler(db *gorm.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Extract refresh token from cookie
		refreshTokenString, err := extractTokenFromCookie(c, RefreshTokenCookieName)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error":   "Unauthorized",
				"message": "Refresh token required",
			})
		}

		// Parse and validate the refresh token
		claims, err := ParseAndValidateToken(refreshTokenString)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error":   "Unauthorized",
				"message": "Invalid or expired refresh token",
			})
		}

		// Validate token type
		if claims.TokenType != "refresh" {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error":   "Unauthorized",
				"message": "Invalid token type",
			})
		}

		// Check if refresh token exists in database (not revoked)
		var session models.Session
		if err := db.Where("token = ?", refreshTokenString).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return c.JSON(http.StatusUnauthorized, map[string]interface{}{
					"error":   "Unauthorized",
					"message": "Refresh token has been revoked",
				})
			}
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": "Failed to validate refresh token",
			})
		}

		// Check if session is expired
		if session.IsExpired() {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error":   "Unauthorized",
				"message": "Refresh token has expired",
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

		// Fetch user from database
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
				"message": "Failed to fetch user",
			})
		}

		// Check if user is soft-deleted
		if user.DeletedAt.Valid {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"error":   "Unauthorized",
				"message": "User account is deactivated",
			})
		}

		// Revoke the old refresh token (delete from database)
		if err := db.Delete(&session).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": "Failed to revoke old refresh token",
			})
		}

		// Generate new token pair
		tokens, err := GenerateTokenPair(&user, db)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": "Failed to generate new tokens",
			})
		}

		// Set new cookies
		SetTokenCookies(c, tokens)

		return c.JSON(http.StatusOK, map[string]interface{}{
			"message": "Tokens refreshed successfully",
			"data": map[string]interface{}{
				"access_expiry":  tokens.AccessExpiry,
				"refresh_expiry": tokens.RefreshExpiry,
			},
		})
	}
}

// LogoutHandler handles user logout by revoking the refresh token and clearing cookies
func LogoutHandler(db *gorm.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Extract refresh token from cookie
		refreshTokenString, err := extractTokenFromCookie(c, RefreshTokenCookieName)
		if err == nil && refreshTokenString != "" {
			// Revoke the refresh token (delete from database)
			db.Where("token = ?", refreshTokenString).Delete(&models.Session{})
		}

		// Clear cookies
		ClearTokenCookies(c)

		return c.JSON(http.StatusOK, map[string]interface{}{
			"message": "Logged out successfully",
		})
	}
}
