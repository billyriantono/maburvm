// Package service provides business logic for VNC operations
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/secret"
)

var (
	// ErrConsoleTokenInvalid is returned when the token is invalid
	ErrConsoleTokenInvalid = fmt.Errorf("invalid console token")
	// ErrConsoleTokenExpired is returned when the token has expired
	ErrConsoleTokenExpired = fmt.Errorf("console token expired")
	// ErrConsoleTokenRevoked is returned when the token has been revoked
	ErrConsoleTokenRevoked = fmt.Errorf("console token revoked")
	// ErrConsoleTokenVMNotFound is returned when the VM is not found
	ErrConsoleTokenVMNotFound = fmt.Errorf("VM not found")
	// ErrConsoleTokenUnauthorized is returned when user is not authorized
	ErrConsoleTokenUnauthorized = fmt.Errorf("user not authorized to access this VM")
	// ErrConsoleDisabled is returned when the VM's console has been disabled
	ErrConsoleDisabled = fmt.Errorf("console is disabled for this VM")
)

// ConsoleTokenTTL is the default TTL for console tokens (5 minutes)
const ConsoleTokenTTL = 5 * time.Minute

// ConsoleTokenJWTID is the JWT ID for console tokens
const ConsoleTokenJWTID = "console_token"

// ConsoleToken represents a short-lived console session token
type ConsoleToken struct {
	ID        string    `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	JTI       string    `json:"jti" gorm:"type:varchar(100);uniqueIndex;not null"`
	VMID      string    `json:"vm_id" gorm:"type:uuid;not null;index"`
	UserID    string    `json:"user_id" gorm:"type:uuid;not null;index"`
	Token     string    `json:"-" gorm:"type:text;uniqueIndex;not null"`
	ExpiresAt time.Time `json:"expires_at" gorm:"not null;index"`
	Revoked   bool      `json:"revoked" gorm:"default:false;index"`
	CreatedAt time.Time `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for ConsoleToken
func (ConsoleToken) TableName() string {
	return "console_tokens"
}

// BeforeCreate hook for ConsoleToken
func (c *ConsoleToken) BeforeCreate(tx *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	return nil
}

// IsExpired checks if the token has expired
func (c *ConsoleToken) IsExpired() bool {
	return time.Now().After(c.ExpiresAt)
}

// ConsoleTokenClaims represents the JWT claims for console tokens
type ConsoleTokenClaims struct {
	VMID   string `json:"vm_id"`
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

// ConsoleTokenResponse represents the API response for console token generation
type ConsoleTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	WSURL     string    `json:"ws_url"`
}

// VNCService handles VNC console token operations
type VNCService struct {
	db        *gorm.DB
	vmRepo    *repository.VMRepository
	nodeRepo  *repository.NodeRepository
	logger    *slog.Logger
	jwtSecret []byte
	wsHost    string
}

// NewVNCService creates a new VNCService instance
func NewVNCService(
	db *gorm.DB,
	vmRepo *repository.VMRepository,
	nodeRepo *repository.NodeRepository,
	logger *slog.Logger,
	jwtSecret string,
	wsHost string,
) (*VNCService, error) {
	// Console tokens are signed with the same key as session JWTs. If no key is
	// supplied we resolve the per-install secret via the store, which is
	// fail-closed: an unreadable/malformed/invalid secrets file or a failed
	// durable write is returned as an error rather than silently regenerating a
	// key (which would mismatch session tokens and break console auth).
	key := jwtSecret
	if key == "" {
		resolved, err := secret.JWTSecret()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve console JWT secret: %w", err)
		}
		key = resolved
	}

	return &VNCService{
		db:        db,
		vmRepo:    vmRepo,
		nodeRepo:  nodeRepo,
		logger:    logger,
		jwtSecret: []byte(key),
		wsHost:    wsHost,
	}, nil
}

// GenerateConsoleToken generates a new console token for VNC access
func (s *VNCService) GenerateConsoleToken(ctx context.Context, vmID, userID string) (*ConsoleTokenResponse, error) {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrConsoleTokenVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Verify user owns the VM (or is admin - handled at handler level)
	if vm.UserID != userID {
		return nil, ErrConsoleTokenUnauthorized
	}

	// Refuse if the operator disabled the console for this VM.
	if !vm.ConsoleEnabled {
		return nil, ErrConsoleDisabled
	}

	// Check VM has VNC configured
	if vm.VNCPort == nil {
		return nil, fmt.Errorf("VNC is not configured for this VM")
	}

	// Generate unique token ID (JTI)
	jti := generateSecureToken(32)

	// Set expiry (5 minutes from now)
	expiresAt := time.Now().Add(ConsoleTokenTTL)

	// Create JWT claims
	claims := ConsoleTokenClaims{
		VMID:   vmID,
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Subject:   userID,
			Issuer:    "maburvm-panel",
			ID:        jti,
		},
	}

	// Create and sign the token
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign token: %w", err)
	}

	// Store token in database for revocation tracking
	consoleToken := &ConsoleToken{
		JTI:       jti,
		VMID:      vmID,
		UserID:    userID,
		Token:     tokenString,
		ExpiresAt: expiresAt,
		Revoked:   false,
	}

	if err := s.db.WithContext(ctx).Create(consoleToken).Error; err != nil {
		return nil, fmt.Errorf("failed to store console token: %w", err)
	}

	// Build WebSocket URL
	wsURL := s.buildWebSocketURL(vmID, vm.NodeID)

	s.logger.InfoContext(ctx, "console token generated",
		"vm_id", vmID,
		"user_id", userID,
		"jti", jti,
		"expires_at", expiresAt,
	)

	return &ConsoleTokenResponse{
		Token:     tokenString,
		ExpiresAt: expiresAt,
		WSURL:     wsURL,
	}, nil
}

// RevokeConsoleToken revokes a console token by JTI
func (s *VNCService) RevokeConsoleToken(ctx context.Context, jti string) error {
	result := s.db.WithContext(ctx).Model(&ConsoleToken{}).
		Where("jti = ?", jti).
		Updates(map[string]interface{}{"revoked": true})

	if result.Error != nil {
		return fmt.Errorf("failed to revoke token: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("token not found")
	}

	s.logger.InfoContext(ctx, "console token revoked", "jti", jti)
	return nil
}

// RevokeConsoleTokenForVM revokes a console token scoped to a specific VM. It
// only matches a token whose JTI AND vm_id both equal the supplied values, so a
// caller cannot revoke a token belonging to another tenant's VM. A non-matching
// JTI or a token on a different VM yields ErrConsoleTokenInvalid (the caller
// learns nothing about the token's existence — anti-enumeration). No token
// details are returned.
func (s *VNCService) RevokeConsoleTokenForVM(ctx context.Context, vmID, jti string) error {
	result := s.db.WithContext(ctx).Model(&ConsoleToken{}).
		Where("vm_id = ? AND jti = ?", vmID, jti).
		Updates(map[string]interface{}{"revoked": true})

	if result.Error != nil {
		return fmt.Errorf("failed to revoke token: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		// Either the JTI does not exist, or it belongs to a different VM. Both
		// map identically to "not found" to avoid leaking cross-tenant state.
		return ErrConsoleTokenInvalid
	}

	s.logger.InfoContext(ctx, "console token revoked for VM", "vm_id", vmID, "jti", jti)
	return nil
}

// ValidateConsoleToken validates a console token and returns claims
func (s *VNCService) ValidateConsoleToken(ctx context.Context, tokenString string) (*ConsoleTokenClaims, error) {
	// Parse and validate the JWT
	token, err := jwt.ParseWithClaims(tokenString, &ConsoleTokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.jwtSecret, nil
	})

	if err != nil {
		return nil, ErrConsoleTokenInvalid
	}

	claims, ok := token.Claims.(*ConsoleTokenClaims)
	if !ok || !token.Valid {
		return nil, ErrConsoleTokenInvalid
	}

	// Check if token has expired
	if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
		return nil, ErrConsoleTokenExpired
	}

	// Check if token has been revoked
	var consoleToken ConsoleToken
	if err := s.db.WithContext(ctx).Where("jti = ?", claims.ID).First(&consoleToken).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrConsoleTokenInvalid
		}
		return nil, fmt.Errorf("failed to check token: %w", err)
	}

	if consoleToken.Revoked {
		return nil, ErrConsoleTokenRevoked
	}

	// Verify VM still exists and its console is still enabled (disabling the
	// console must drop in-flight sessions, not just block new tokens).
	vm, err := s.vmRepo.GetByID(ctx, claims.VMID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrConsoleTokenVMNotFound
		}
		return nil, fmt.Errorf("failed to verify VM: %w", err)
	}
	if !vm.ConsoleEnabled {
		return nil, ErrConsoleDisabled
	}

	return claims, nil
}

// ValidateConsoleTokenFromHeader validates token from Authorization header
func (s *VNCService) ValidateConsoleTokenFromHeader(ctx context.Context, authHeader string) (*ConsoleTokenClaims, error) {
	if authHeader == "" {
		return nil, ErrConsoleTokenInvalid
	}

	// Extract token from "Bearer <token>" format
	const prefix = "Bearer "
	if len(authHeader) <= len(prefix) {
		return nil, ErrConsoleTokenInvalid
	}

	if authHeader[:len(prefix)] != prefix {
		return nil, ErrConsoleTokenInvalid
	}

	tokenString := authHeader[len(prefix):]
	return s.ValidateConsoleToken(ctx, tokenString)
}

// GetConsoleTokenByJTI retrieves a console token by its JTI
func (s *VNCService) GetConsoleTokenByJTI(ctx context.Context, jti string) (*ConsoleToken, error) {
	var token ConsoleToken
	if err := s.db.WithContext(ctx).Where("jti = ?", jti).First(&token).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrConsoleTokenInvalid
		}
		return nil, fmt.Errorf("failed to get token: %w", err)
	}
	return &token, nil
}

// RevokeAllTokensForVM revokes all console tokens for a specific VM
func (s *VNCService) RevokeAllTokensForVM(ctx context.Context, vmID string) error {
	result := s.db.WithContext(ctx).Model(&ConsoleToken{}).
		Where("vm_id = ? AND revoked = ?", vmID, false).
		Updates(map[string]interface{}{"revoked": true})

	if result.Error != nil {
		return fmt.Errorf("failed to revoke tokens: %w", result.Error)
	}

	s.logger.InfoContext(ctx, "all console tokens revoked for VM",
		"vm_id", vmID,
		"count", result.RowsAffected,
	)
	return nil
}

// CleanExpiredTokens removes expired tokens from the database (can be run as a scheduled job)
func (s *VNCService) CleanExpiredTokens(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).Where("expires_at < ?", time.Now()).Unscoped().Delete(&ConsoleToken{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to clean expired tokens: %w", result.Error)
	}

	if result.RowsAffected > 0 {
		s.logger.InfoContext(ctx, "expired console tokens cleaned",
			"count", result.RowsAffected)
	}

	return result.RowsAffected, nil
}

// buildWebSocketURL builds the WebSocket URL for VNC connection
func (s *VNCService) buildWebSocketURL(vmID, nodeID string) string {
	wsHost := s.wsHost
	if wsHost == "" {
		wsHost = "localhost:8080"
	}
	return fmt.Sprintf("wss://%s/api/vms/%s/console", wsHost, vmID)
}

// generateSecureToken generates a cryptographically secure random token
func generateSecureToken(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to uuid if random fails
		return uuid.New().String()
	}
	return hex.EncodeToString(bytes)[:length]
}

// GetWebSocketURL gets the WebSocket URL for a VM (used by proxy middleware)
func (s *VNCService) GetWebSocketURL(ctx context.Context, vmID string) (string, error) {
	vm, err := s.vmRepo.GetByIDWithNode(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", ErrConsoleTokenVMNotFound
		}
		return "", fmt.Errorf("failed to get VM: %w", err)
	}

	if vm.VNCPort == nil {
		return "", fmt.Errorf("VNC is not configured for this VM")
	}

	wsHost := s.wsHost
	if wsHost == "" {
		wsHost = "localhost:8080"
	}

	return fmt.Sprintf("wss://%s/api/vms/%s/console", wsHost, vmID), nil
}

// CheckAuthForVNC checks if a user is authorized to access a VM's console
func (s *VNCService) CheckAuthForVNC(ctx context.Context, vmID, userID string) error {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return ErrConsoleTokenVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// User must own the VM
	if vm.UserID != userID {
		return ErrConsoleTokenUnauthorized
	}

	// Refuse if the operator disabled the console for this VM.
	if !vm.ConsoleEnabled {
		return ErrConsoleDisabled
	}

	return nil
}

func (s *VNCService) Migrate() error {
	return s.db.AutoMigrate(&ConsoleToken{})
}
