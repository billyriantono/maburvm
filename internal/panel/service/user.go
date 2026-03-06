package service

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"regexp"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	// ErrUserNotFound is returned when a user is not found
	ErrUserNotFound = errors.New("user not found")
	// ErrInvalidCredentials is returned when credentials are invalid
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrWeakPassword is returned when password doesn't meet requirements
	ErrWeakPassword = errors.New("password does not meet security requirements")
	// ErrInvalidTOTPCode is returned when TOTP code is invalid
	ErrInvalidTOTPCode = errors.New("invalid TOTP code")
	// ErrIPNotWhitelisted is returned when IP is not in whitelist
	ErrIPNotWhitelisted = errors.New("IP address not whitelisted")
	// Err2FANotEnabled is returned when 2FA is not enabled for user
	Err2FANotEnabled = errors.New("2FA not enabled for this user")
	// Err2FAAlreadyEnabled is returned when 2FA is already enabled
	Err2FAAlreadyEnabled = errors.New("2FA already enabled for this user")
	// ErrInvalidIPWhitelist is returned when IP whitelist contains invalid entries
	ErrInvalidIPWhitelist = errors.New("invalid IP whitelist entry")
)

// Password requirements
const (
	MinPasswordLength = 12
	BcryptCost        = 12
)

// UserService handles user-related operations
type UserService struct {
	db        *gorm.DB
	aesKey    []byte
	jwtSecret string
	issuer    string
}

// NewUserService creates a new UserService
func NewUserService(db *gorm.DB, aesKey, jwtSecret, issuer string) (*UserService, error) {
	key := []byte(aesKey)
	if len(key) != 32 {
		return nil, errors.New("AES key must be 32 bytes for AES-256")
	}

	return &UserService{
		db:        db,
		aesKey:    key,
		jwtSecret: jwtSecret,
		issuer:    issuer,
	}, nil
}

// RegisterRequest contains registration data
type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
	Role     string `json:"role" validate:"omitempty,oneof=admin client"`
}

// RegisterResponse contains registration response
type RegisterResponse struct {
	User  *models.User `json:"user"`
	Token string       `json:"token,omitempty"`
}

// Register creates a new user account
func (s *UserService) Register(req *RegisterRequest) (*RegisterResponse, error) {
	// Validate password strength
	if err := s.validatePasswordStrength(req.Password); err != nil {
		return nil, err
	}

	// Check if user already exists
	var existingUser models.User
	if err := s.db.Where("email = ?", req.Email).First(&existingUser).Error; err == nil {
		return nil, errors.New("user with this email already exists")
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Create user
	role := models.UserRole(req.Role)
	if role == "" {
		role = models.RoleClient
	}

	user := &models.User{
		Email:        req.Email,
		PasswordHash: string(hashedPassword),
		Role:         role,
		IPWhitelist:  []string{},
	}

	if err := s.db.Create(user).Error; err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	return &RegisterResponse{
		User: user,
	}, nil
}

// validatePasswordStrength checks password complexity
func (s *UserService) validatePasswordStrength(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("%w: must be at least %d characters", ErrWeakPassword, MinPasswordLength)
	}

	// Check for at least one uppercase letter
	if !regexp.MustCompile(`[A-Z]`).MatchString(password) {
		return fmt.Errorf("%w: must contain at least one uppercase letter", ErrWeakPassword)
	}

	// Check for at least one lowercase letter
	if !regexp.MustCompile(`[a-z]`).MatchString(password) {
		return fmt.Errorf("%w: must contain at least one lowercase letter", ErrWeakPassword)
	}

	// Check for at least one digit
	if !regexp.MustCompile(`[0-9]`).MatchString(password) {
		return fmt.Errorf("%w: must contain at least one digit", ErrWeakPassword)
	}

	// Check for at least one special character
	if !regexp.MustCompile(`[!@#$%^&*()_+\-=\[\]{};':"\\|,.<>\/?]`).MatchString(password) {
		return fmt.Errorf("%w: must contain at least one special character", ErrWeakPassword)
	}

	return nil
}

// LoginRequest contains login credentials
type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
	TOTPCode string `json:"totp_code,omitempty"` // Optional for first stage
	ClientIP string `json:"-"`                   // Set by handler
}

// LoginResponse contains login result
type LoginResponse struct {
	User          *models.User `json:"user,omitempty"`
	Token         string       `json:"token,omitempty"`
	Requires2FA   bool         `json:"requires_2fa"`
	TempToken     string       `json:"temp_token,omitempty"` // For 2FA verification
	LoginAttempts int          `json:"login_attempts,omitempty"`
}

// Login authenticates a user
func (s *UserService) Login(req *LoginRequest) (*LoginResponse, error) {
	// Find user by email
	var user models.User
	if err := s.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("database error: %w", err)
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	// Check IP whitelist (if configured)
	if len(user.IPWhitelist) > 0 {
		if !s.isIPWhitelisted(req.ClientIP, user.IPWhitelist) {
			return nil, ErrIPNotWhitelisted
		}
	}

	// Check if 2FA is enabled
	if user.TwoFactorSecret != "" {
		// If no TOTP code provided, return requires 2FA
		if req.TOTPCode == "" {
			return &LoginResponse{
				Requires2FA: true,
				TempToken:   s.generateTempToken(user.ID),
			}, nil
		}

		// Decrypt and verify TOTP code
		decryptedSecret, err := s.decryptSecret(user.TwoFactorSecret)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt 2FA secret: %w", err)
		}

		if !totp.Validate(req.TOTPCode, decryptedSecret) {
			return nil, ErrInvalidTOTPCode
		}
	}

	// Generate JWT token
	token, err := s.generateJWT(user.ID, user.Role)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	return &LoginResponse{
		User:  &user,
		Token: token,
	}, nil
}

// isIPWhitelisted checks if the given IP matches any entry in the whitelist
// Supports individual IPs and CIDR notation
func (s *UserService) isIPWhitelisted(clientIP string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true // Empty whitelist allows all IPs
	}

	clientAddr := net.ParseIP(clientIP)
	if clientAddr == nil {
		return false
	}

	for _, entry := range whitelist {
		if entry == "" {
			continue
		}

		// Try to parse as CIDR
		_, ipNet, err := net.ParseCIDR(entry)
		if err == nil {
			// It's a CIDR - check if IP is in range
			if ipNet.Contains(clientAddr) {
				return true
			}
			continue
		}

		// Try to parse as single IP
		whitelistedIP := net.ParseIP(entry)
		if whitelistedIP != nil {
			if whitelistedIP.Equal(clientAddr) {
				return true
			}
		}
	}

	return false
}

// Setup2FARequest contains 2FA setup request
type Setup2FARequest struct {
	UserID uuid.UUID `json:"user_id" validate:"required"`
}

// Setup2FAResponse contains 2FA setup information
type Setup2FAResponse struct {
	Secret      string   `json:"secret"`       // Base32 encoded secret (for manual entry)
	QRCodeURL   string   `json:"qr_code_url"`  // otpauth:// URL
	QRCodePNG   string   `json:"qr_code_png"`  // Base64 encoded PNG image
	BackupCodes []string `json:"backup_codes"` // TODO: Implement backup codes
}

// Setup2FA generates a new TOTP secret and QR code for 2FA setup
func (s *UserService) Setup2FA(req *Setup2FARequest) (*Setup2FAResponse, error) {
	// Find user
	var user models.User
	if err := s.db.First(&user, req.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("database error: %w", err)
	}

	// Check if 2FA is already enabled
	if user.TwoFactorSecret != "" {
		return nil, Err2FAAlreadyEnabled
	}

	// Generate new TOTP secret
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      s.issuer,
		AccountName: user.Email,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to generate TOTP secret: %w", err)
	}

	// Encrypt the secret before storing
	encryptedSecret, err := s.encryptSecret(key.Secret())
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt secret: %w", err)
	}

	// Store encrypted secret temporarily (will be confirmed after verification)
	// In production, you might want to store this in a separate "pending_2fa" table
	user.TwoFactorSecret = encryptedSecret
	if err := s.db.Save(&user).Error; err != nil {
		return nil, fmt.Errorf("failed to save 2FA secret: %w", err)
	}

	// Generate QR code
	qrCode, err := s.generateQRCode(key.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to generate QR code: %w", err)
	}

	return &Setup2FAResponse{
		Secret:      key.Secret(),
		QRCodeURL:   key.URL(),
		QRCodePNG:   qrCode,
		BackupCodes: []string{}, // TODO: Generate backup codes
	}, nil
}

// Verify2FASetupRequest contains 2FA verification request
type Verify2FASetupRequest struct {
	UserID   uuid.UUID `json:"user_id" validate:"required"`
	TOTPCode string    `json:"totp_code" validate:"required"`
}

// Verify2FASetup verifies the TOTP code during 2FA setup
func (s *UserService) Verify2FASetup(req *Verify2FASetupRequest) error {
	// Find user
	var user models.User
	if err := s.db.First(&user, req.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("database error: %w", err)
	}

	// Check if 2FA secret exists
	if user.TwoFactorSecret == "" {
		return Err2FANotEnabled
	}

	// Decrypt secret
	decryptedSecret, err := s.decryptSecret(user.TwoFactorSecret)
	if err != nil {
		return fmt.Errorf("failed to decrypt 2FA secret: %w", err)
	}

	// Verify TOTP code
	if !totp.Validate(req.TOTPCode, decryptedSecret) {
		return ErrInvalidTOTPCode
	}

	// 2FA is now fully enabled
	return nil
}

// Disable2FA disables 2FA for a user
func (s *UserService) Disable2FA(userID uuid.UUID) error {
	var user models.User
	if err := s.db.First(&user, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("database error: %w", err)
	}

	if user.TwoFactorSecret == "" {
		return Err2FANotEnabled
	}

	user.TwoFactorSecret = ""
	return s.db.Save(&user).Error
}

// generateQRCode creates a QR code from the TOTP URL and returns it as base64 PNG
func (s *UserService) generateQRCode(url string) (string, error) {
	// Create QR code
	qrCode, err := qr.Encode(url, qr.M, qr.Auto)
	if err != nil {
		return "", err
	}

	// Scale to appropriate size
	qrCode, err = barcode.Scale(qrCode, 200, 200)
	if err != nil {
		return "", err
	}

	// Encode to PNG
	var buf bytes.Buffer
	if err := png.Encode(&buf, qrCode); err != nil {
		return "", err
	}

	// Return base64 encoded PNG
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// encryptSecret encrypts a TOTP secret using AES-GCM
func (s *UserService) encryptSecret(plainText string) (string, error) {
	block, err := aes.NewCipher(s.aesKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plainText), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptSecret decrypts an encrypted TOTP secret
func (s *UserService) decryptSecret(cipherText string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(s.aesKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// UpdateIPWhitelist updates a user's IP whitelist
type UpdateIPWhitelistRequest struct {
	UserID      uuid.UUID `json:"user_id" validate:"required"`
	IPWhitelist []string  `json:"ip_whitelist" validate:"dive,ip_or_cidr"`
}

// UpdateIPWhitelist updates the IP whitelist for a user
func (s *UserService) UpdateIPWhitelist(req *UpdateIPWhitelistRequest) error {
	// Validate each IP/CIDR entry
	for _, ip := range req.IPWhitelist {
		if ip == "" {
			continue
		}
		if !s.isValidIPOrCIDR(ip) {
			return fmt.Errorf("%w: %s", ErrInvalidIPWhitelist, ip)
		}
	}

	var user models.User
	if err := s.db.First(&user, req.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("database error: %w", err)
	}

	user.IPWhitelist = req.IPWhitelist
	return s.db.Save(&user).Error
}

// isValidIPOrCIDR validates if a string is a valid IP or CIDR
func (s *UserService) isValidIPOrCIDR(input string) bool {
	// Try CIDR first
	_, _, err := net.ParseCIDR(input)
	if err == nil {
		return true
	}

	// Try single IP
	ip := net.ParseIP(input)
	return ip != nil
}

// PasswordResetRequest contains password reset request
type PasswordResetRequest struct {
	Email string `json:"email" validate:"required,email"`
}

// RequestPasswordReset initiates a password reset flow (placeholder)
func (s *UserService) RequestPasswordReset(req *PasswordResetRequest) error {
	// Find user
	var user models.User
	if err := s.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Don't reveal if email exists - return success anyway
			return nil
		}
		return fmt.Errorf("database error: %w", err)
	}

	// TODO: Implement actual password reset logic
	// 1. Generate reset token
	// 2. Store token with expiration
	// 3. Send email with reset link
	// For now, this is just a placeholder

	fmt.Printf("[PLACEHOLDER] Password reset requested for: %s\n", user.Email)
	return nil
}

// ResetPasswordRequest contains password reset confirmation
type ResetPasswordRequest struct {
	Token       string `json:"token" validate:"required"`
	NewPassword string `json:"new_password" validate:"required"`
}

// ResetPassword resets a user's password using a reset token (placeholder)
func (s *UserService) ResetPassword(req *ResetPasswordRequest) error {
	// Validate password strength
	if err := s.validatePasswordStrength(req.NewPassword); err != nil {
		return err
	}

	// TODO: Implement actual password reset logic
	// 1. Validate reset token
	// 2. Check expiration
	// 3. Hash new password
	// 4. Update user password
	// 5. Invalidate token

	return errors.New("password reset not yet implemented")
}

// generateTempToken generates a temporary token for 2FA verification
func (s *UserService) generateTempToken(userID uuid.UUID) string {
	// Simple implementation - in production, use proper JWT with short expiration
	// This is a placeholder implementation
	timestamp := time.Now().Unix()
	data := fmt.Sprintf("%s:%d:%s", userID.String(), timestamp, s.jwtSecret)
	// In production, use proper HMAC or JWT library
	return base64.StdEncoding.EncodeToString([]byte(data))
}

// generateJWT generates a JWT token for authenticated users
func (s *UserService) generateJWT(userID uuid.UUID, role models.UserRole) (string, error) {
	// TODO: Implement proper JWT generation
	// For now, return a placeholder
	claims := map[string]interface{}{
		"user_id": userID.String(),
		"role":    string(role),
		"exp":     time.Now().Add(24 * time.Hour).Unix(),
		"iat":     time.Now().Unix(),
		"iss":     s.issuer,
	}

	// Simple JWT structure (header.payload.signature)
	// In production, use a proper JWT library like github.com/golang-jwt/jwt
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)

	// Create signature (in production, use proper HMAC)
	signature := fmt.Sprintf("%s.%s", header, payloadB64)
	return fmt.Sprintf("%s.%s", signature, "signature_placeholder"), nil
}
