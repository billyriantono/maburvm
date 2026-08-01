package service

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/smtp"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/golang-jwt/jwt/v5"
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

// SMTP configuration for sending emails
var (
	SMTPServer = getEnvOrDefault("SMTP_SERVER", "localhost:1025")
	SMTPFrom   = getEnvOrDefault("SMTP_FROM", "noreply@maburvm.local")
)

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// sendEmail sends an email via SMTP
func sendEmail(to, subject, body string) error {
	msg := []byte(fmt.Sprintf("To: %s\r\nSubject: %s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s", to, subject, body))
	return smtp.SendMail(SMTPServer, nil, SMTPFrom, []string{to}, msg)
}

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
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
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

		// Accept a TOTP code or a one-time backup code (so a lost authenticator
		// device doesn't lock the user out).
		if !s.verifyTwoFactorCode(&user, req.TOTPCode) {
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
	BackupCodes []string `json:"backup_codes"` // Recovery codes for 2FA bypass
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

	// Generate backup codes
	plaintextCodes, hashedCodes := s.generateBackupCodes()

	// Store hashed backup codes
	codesJSON, _ := json.Marshal(hashedCodes)
	encryptedCodes, err := s.encryptSecret(string(codesJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt backup codes: %w", err)
	}
	user.TwoFactorBackupCodes = encryptedCodes
	if err := s.db.Save(&user).Error; err != nil {
		return nil, fmt.Errorf("failed to save backup codes: %w", err)
	}

	return &Setup2FAResponse{
		Secret:      key.Secret(),
		QRCodeURL:   key.URL(),
		QRCodePNG:   qrCode,
		BackupCodes: plaintextCodes, // Return plaintext ONCE
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
	user.TwoFactorBackupCodes = ""
	return s.db.Save(&user).Error
}

// Verify2FARequest contains 2FA verification request for login
type Verify2FARequest struct {
	UserID uuid.UUID `json:"user_id" validate:"required"`
	Code   string    `json:"code" validate:"required"` // TOTP code or backup code
}

// Verify2FA verifies 2FA code (TOTP or backup code) during login
func (s *UserService) Verify2FA(req *Verify2FARequest) error {
	var user models.User
	if err := s.db.First(&user, req.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUserNotFound
		}
		return fmt.Errorf("database error: %w", err)
	}

	if user.TwoFactorSecret == "" {
		return Err2FANotEnabled
	}

	if !s.verifyTwoFactorCode(&user, req.Code) {
		return ErrInvalidTOTPCode
	}
	return nil
}

// verifyTwoFactorCode validates a 2FA code for a user: a 6-digit TOTP code, or
// failing that an unused 8-char backup code (which is then consumed). Shared by
// the login flow and the explicit verify endpoint so both accept backup codes.
func (s *UserService) verifyTwoFactorCode(user *models.User, code string) bool {
	secret, err := s.decryptSecret(user.TwoFactorSecret)
	if err != nil {
		return false
	}
	if len(code) == 6 && totp.Validate(code, secret) {
		return true
	}
	if len(code) == 8 && s.verifyBackupCode(user, code) {
		return true
	}
	return false
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

// SECURE PASSWORD-RESET CONTAINMENT (Phase 1A)
//
// The legacy raw-token reset path (PasswordResetToken with `token`/`used`
// columns, RequestPasswordReset, ResetPassword) has been removed. It persisted
// raw reset tokens and built reset URLs, which conflicts with the canonical
// hash-only password_reset_tokens schema defined by migration 034 (SHA-256 hex,
// token_hash, consumed_at) and the post-034 integrity constraints (migrations
// 035). No HTTP route consumed these methods, so they were a future-dangerous
// trap and their SQLite tests created a divergent legacy table shape.
//
// The secure reset flow (hash-only token, mailer delivery, token_hash consume
// via EnrollmentRepository, all under a single Phase 1B outer transaction)
// is implemented in Phase 1B. Do NOT reintroduce raw-token reset behavior here.
// Any call site that previously used UserService.RequestPasswordReset /
// ResetPassword must be built against the Phase 1B secure API instead.

// generateTempToken generates a temporary token for 2FA verification
func (s *UserService) generateTempToken(userID uuid.UUID) string {
	timestamp := time.Now().Unix()
	nonce := make([]byte, 16)
	rand.Read(nonce)

	data := fmt.Sprintf("%s:%d:%s", userID.String(), timestamp, hex.EncodeToString(nonce))
	h := hmac.New(sha256.New, []byte(s.jwtSecret))
	h.Write([]byte(data))
	signature := hex.EncodeToString(h.Sum(nil))

	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", data, signature)))
}

// JWTClaims represents the JWT claims
type JWTClaims struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// generateJWT generates a JWT token for authenticated users using golang-jwt/jwt/v5
func (s *UserService) generateJWT(userID uuid.UUID, role models.UserRole) (string, error) {
	claims := JWTClaims{
		UserID: userID.String(),
		Role:   string(role),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    s.issuer,
			Subject:   userID.String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.jwtSecret))
}

// generateBackupCodes generates 10 backup codes for 2FA recovery
// Returns plaintext codes (to be shown once) and stores hashed versions
func (s *UserService) generateBackupCodes() ([]string, []string) {
	plaintextCodes := make([]string, 10)
	hashedCodes := make([]string, 10)

	for i := 0; i < 10; i++ {
		// Generate 8-character code
		bytes := make([]byte, 4)
		rand.Read(bytes)
		code := strings.ToUpper(hex.EncodeToString(bytes))

		// Hash with bcrypt
		hashed, err := bcrypt.GenerateFromPassword([]byte(code), BcryptCost)
		if err != nil {
			// Fallback to unhashed if bcrypt fails (shouldn't happen)
			hashed = []byte(code)
		}

		plaintextCodes[i] = code
		hashedCodes[i] = string(hashed)
	}

	return plaintextCodes, hashedCodes
}

// verifyBackupCode verifies a backup code against stored hashed codes
// Returns true if valid and marks the code as used
func (s *UserService) verifyBackupCode(user *models.User, code string) bool {
	// Decrypt backup codes
	if user.TwoFactorBackupCodes == "" {
		return false
	}

	decryptedCodes, err := s.decryptSecret(user.TwoFactorBackupCodes)
	if err != nil {
		return false
	}

	// Parse stored hashed codes
	var storedCodes []string
	if err := json.Unmarshal([]byte(decryptedCodes), &storedCodes); err != nil {
		return false
	}

	// Check each code
	for i, hashedCode := range storedCodes {
		if hashedCode == "USED" {
			continue
		}

		if bcrypt.CompareHashAndPassword([]byte(hashedCode), []byte(code)) == nil {
			// Mark as used
			storedCodes[i] = "USED"

			// Save back to user
			updatedCodes, _ := json.Marshal(storedCodes)
			encryptedCodes, _ := s.encryptSecret(string(updatedCodes))
			user.TwoFactorBackupCodes = encryptedCodes
			s.db.Save(user)

			return true
		}
	}

	return false
}
