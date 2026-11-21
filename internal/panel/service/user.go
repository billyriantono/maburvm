package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"log/slog"
	"math"
	"net"
	"net/smtp"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/middleware"
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

// Password requirements. MinPasswordLength is the built-in minimum; the one
// in force comes from LoadPasswordPolicy.
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
	prevKeys  [][]byte // retired AES keys, decrypt-only (rotation)
	jwtSecret string
	issuer    string
}

// NewUserService creates a new UserService. previousAESKeys are retired keys
// still accepted for decryption while a key rotation is in progress.
func NewUserService(db *gorm.DB, aesKey, jwtSecret, issuer string, previousAESKeys ...string) (*UserService, error) {
	key := []byte(aesKey)
	if len(key) != 32 {
		return nil, errors.New("AES key must be 32 bytes for AES-256")
	}
	var prev [][]byte
	for _, k := range previousAESKeys {
		if len(k) != 32 {
			return nil, errors.New("previous AES key must be 32 bytes for AES-256")
		}
		prev = append(prev, []byte(k))
	}

	return &UserService{
		db:        db,
		aesKey:    key,
		prevKeys:  prev,
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

// PasswordPolicy is what a new password must satisfy. A lowercase letter is
// always required; the rest is the operator's, under Settings, System, Security.
type PasswordPolicy struct {
	MinLength        int  `json:"minPasswordLength"`
	RequireUppercase bool `json:"requireUppercase"`
	RequireNumbers   bool `json:"requireNumbers"`
	RequireSpecial   bool `json:"requireSpecial"`
}

// LoadPasswordPolicy reads the operator's password policy. A field never saved
// keeps its built-in value, and a length outside 8 to 128 is pulled back in.
func LoadPasswordPolicy(db *gorm.DB) PasswordPolicy {
	p := PasswordPolicy{MinLength: MinPasswordLength, RequireUppercase: true, RequireNumbers: true, RequireSpecial: true}
	if db == nil {
		return p
	}
	var raw string
	if err := db.Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = 'security'").Scan(&raw).Error; err != nil || raw == "" {
		return p
	}
	_ = json.Unmarshal([]byte(raw), &p)
	p.MinLength = min(max(p.MinLength, 8), 128)
	return p
}

// ValidatePasswordStrength checks a new password against the operator's policy.
func ValidatePasswordStrength(db *gorm.DB, password string) error {
	return LoadPasswordPolicy(db).Check(password)
}

func (s *UserService) validatePasswordStrength(password string) error {
	if s == nil {
		return ValidatePasswordStrength(nil, password)
	}
	return ValidatePasswordStrength(s.db, password)
}

// Check reports the first rule a password breaks.
func (p PasswordPolicy) Check(password string) error {
	if len(password) < p.MinLength {
		return fmt.Errorf("%w: must be at least %d characters", ErrWeakPassword, p.MinLength)
	}
	if p.RequireUppercase && !regexp.MustCompile(`[A-Z]`).MatchString(password) {
		return fmt.Errorf("%w: must contain at least one uppercase letter", ErrWeakPassword)
	}
	if !regexp.MustCompile(`[a-z]`).MatchString(password) {
		return fmt.Errorf("%w: must contain at least one lowercase letter", ErrWeakPassword)
	}
	if p.RequireNumbers && !regexp.MustCompile(`[0-9]`).MatchString(password) {
		return fmt.Errorf("%w: must contain at least one digit", ErrWeakPassword)
	}
	if p.RequireSpecial && !regexp.MustCompile(`[!@#$%^&*()_+\-=\[\]{};':"\\|,.<>\/?]`).MatchString(password) {
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
			// Spend the time a real account would, so the answer does not come
			// back faster for an email nobody has.
			_ = bcrypt.CompareHashAndPassword(dummyPasswordHash(), []byte(req.Password))
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("database error: %w", err)
	}

	// Outside the allow-list, before the password: otherwise the answer tells a
	// right password from a wrong one, and the wrong ones lock the owner out.
	if len(user.IPWhitelist) > 0 && !s.isIPWhitelisted(req.ClientIP, user.IPWhitelist) {
		return nil, ErrIPNotWhitelisted
	}

	// A locked account takes no guesses at all, right or wrong.
	if user.SignInLocked() {
		return nil, &AccountLockedError{Until: *user.LockedUntil}
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		s.recordFailedSignIn(&user, req.ClientIP)
		return nil, ErrInvalidCredentials
	}
	// A temporary account that has run out is simply not an account any more.
	if user.Expired() {
		return nil, ErrInvalidCredentials
	}
	// Said only to someone who has just proved the account is theirs.
	if user.Suspended() {
		return nil, ErrAccountSuspended
	}

	// Check if 2FA is enabled (verified), not merely set up. An abandoned setup
	// leaves a secret but TwoFactorEnabled=false and must not gate login.
	if user.TwoFactorEnabled {
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
			s.recordFailedSignIn(&user, req.ClientIP)
			return nil, ErrInvalidTOTPCode
		}
	}

	// Generate the session access token via the single shared minter.
	token, err := s.mintSession(&user)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}
	// Only a finished sign-in clears the count: the password step alone must
	// not, or it would reset the count between guesses at the code.
	if user.FailedLoginCount != 0 {
		_ = ClearSignInLock(context.Background(), s.db, user.ID)
	}

	auditSupportSignIn(s.db, &user, req.ClientIP)

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

	// Only a fully-enabled 2FA blocks re-setup. A dangling secret from an
	// abandoned setup is overwritten by the fresh one below.
	if user.TwoFactorEnabled {
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
	// Codes of the old secret mean nothing to the new one: start its record afresh,
	// or re-enabling within the same half minute would refuse a good code.
	if err := s.db.Table("users").Where("id = ?", user.ID).Update("totp_last_step", 0).Error; err != nil {
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
	if !s.acceptTOTP(&user, decryptedSecret, req.TOTPCode) {
		return ErrInvalidTOTPCode
	}

	// 2FA is now fully enabled — flip the flag Login gates on.
	if err := s.db.Model(&user).Update("two_factor_enabled", true).Error; err != nil {
		return fmt.Errorf("failed to enable 2FA: %w", err)
	}
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
	user.TwoFactorEnabled = false
	return s.db.Save(&user).Error
}

// Verify2FARequest contains 2FA verification request for login
type Verify2FARequest struct {
	UserID uuid.UUID `json:"user_id" validate:"required"`
	Code   string    `json:"code" validate:"required"` // TOTP code or backup code
	// ClientIP goes into the audit row if the wrong codes lock the account.
	ClientIP string `json:"-"`
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
	return s.checkTwoFactor(&user, req.Code, req.ClientIP)
}

// checkTwoFactor verifies a code outside the password sign-in, under the same
// lockout: a held session or an SSO ticket must not allow unlimited guesses.
func (s *UserService) checkTwoFactor(user *models.User, code, clientIP string) error {
	if user.SignInLocked() {
		return &AccountLockedError{Until: *user.LockedUntil}
	}
	if !s.verifyTwoFactorCode(user, code) {
		s.recordFailedSignIn(user, clientIP)
		return ErrInvalidTOTPCode
	}
	if user.FailedLoginCount != 0 {
		_ = ClearSignInLock(context.Background(), s.db, user.ID)
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
	if len(code) == 6 && s.acceptTOTP(user, secret, code) {
		return true
	}
	// Backup codes are shown in capitals; accept them typed either way.
	if len(code) == 8 && s.verifyBackupCode(user, strings.ToUpper(code)) {
		return true
	}
	return false
}

// totpStepSeconds is the length of one authenticator time step.
const totpStepSeconds = 30

// acceptTOTP checks an authenticator code against the current time step and
// its neighbours, and takes it once only: a step at or before the last one
// accepted is refused, so a code seen over a shoulder or on the wire cannot be
// used again. The step is recorded with a conditional update, so two requests
// with the same code cannot both pass.
func (s *UserService) acceptTOTP(user *models.User, secret, code string) bool {
	current := time.Now().Unix() / totpStepSeconds
	for _, step := range []int64{current, current - 1, current + 1} {
		if step <= user.TOTPLastStep {
			continue
		}
		want, err := totp.GenerateCode(secret, time.Unix(step*totpStepSeconds, 0))
		if err != nil || subtle.ConstantTimeCompare([]byte(want), []byte(code)) != 1 {
			continue
		}
		res := s.db.Table("users").Where("id = ? AND totp_last_step < ?", user.ID, step).Update("totp_last_step", step)
		if res.Error != nil || res.RowsAffected != 1 {
			return false
		}
		user.TOTPLastStep = step
		return true
	}
	return false
}

// mintSession issues the access token for a finished sign-in, valid for the
// operator's session length.
func (s *UserService) mintSession(user *models.User) (string, error) {
	return middleware.GenerateAccessToken(user, s.SessionLifetime())
}

// SessionLifetime is how long a sign-in lasts (Settings, System, Security).
func (s *UserService) SessionLifetime() time.Duration {
	return middleware.LoadSignInPolicy(s.db).SessionLifetime()
}

// dummyPasswordHash is a real bcrypt hash of nothing in particular, compared
// against when the email is unknown.
var dummyPasswordHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("no such account"), bcrypt.DefaultCost)
	return h
})

// AccountLockedError is returned while an account is locked after too many
// failed sign-ins. Until is when it unlocks by itself.
type AccountLockedError struct{ Until time.Time }

func (e *AccountLockedError) Error() string {
	return "account locked after too many failed sign-ins"
}

// Wait says how long is left, for a message: "1 minute", "15 minutes".
func (e *AccountLockedError) Wait() string {
	minutes := max(int(math.Ceil(time.Until(e.Until).Minutes())), 1)
	if minutes == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

// recordFailedSignIn counts one wrong password or code and locks the account
// when the count reaches the operator's limit. Best-effort: a failed write is
// logged and the sign-in is refused either way.
func (s *UserService) recordFailedSignIn(user *models.User, clientIP string) {
	policy := middleware.LoadSignInPolicy(s.db)
	if policy.LockoutAttempts <= 0 {
		return
	}
	if err := s.db.Table("users").Where("id = ?", user.ID).
		Update("failed_login_count", gorm.Expr("failed_login_count + 1")).Error; err != nil {
		slog.Default().Error("sign-in lockout: could not count a failed sign-in", "user_id", user.ID, "error", err)
		return
	}
	until := time.Now().Add(time.Duration(policy.LockoutMinutes) * time.Minute)
	// The count starts again once the lock is set, so an expired lock leaves a clean slate.
	res := s.db.Table("users").Where("id = ? AND failed_login_count >= ?", user.ID, policy.LockoutAttempts).
		Updates(map[string]interface{}{"locked_until": until, "failed_login_count": 0})
	if res.Error != nil || res.RowsAffected != 1 {
		return
	}
	slog.Default().Warn("account locked after failed sign-ins", "user_id", user.ID, "ip", clientIP, "until", until)
	AuditUserEvent(context.Background(), s.db, "user.locked", "", user.ID.String(), clientIP, "", map[string]any{
		"email": user.Email, "failed_attempts": policy.LockoutAttempts, "locked_until": until.UTC().Format(time.RFC3339),
	})
}

// ClearSignInLock unlocks an account and forgets its failed sign-ins.
func ClearSignInLock(ctx context.Context, db *gorm.DB, userID uuid.UUID) error {
	return db.WithContext(ctx).Table("users").Where("id = ?", userID).
		Updates(map[string]interface{}{"locked_until": nil, "failed_login_count": 0}).Error
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

// decryptSecret decrypts an encrypted TOTP secret, falling back to retired
// keys so data stays readable mid-rotation.
func (s *UserService) decryptSecret(cipherText string) (string, error) {
	plain, err := decryptWithKey(s.aesKey, cipherText)
	for _, k := range s.prevKeys {
		if err == nil {
			break
		}
		plain, err = decryptWithKey(k, cipherText)
	}
	return plain, err
}

// ReencryptSecrets rewrites 2FA secrets and backup codes that only decrypt
// under a retired key so they use the current key. It returns how many values
// were rewritten; values no key can decrypt are reported in err and left as-is.
func (s *UserService) ReencryptSecrets(ctx context.Context) (int, error) {
	if len(s.prevKeys) == 0 {
		return 0, nil
	}
	var users []models.User
	if err := s.db.WithContext(ctx).Select("id", "two_factor_secret", "two_factor_backup_codes").
		Where("two_factor_secret <> '' OR two_factor_backup_codes <> ''").Find(&users).Error; err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, u := range users {
		for col, ct := range map[string]string{"two_factor_secret": u.TwoFactorSecret, "two_factor_backup_codes": u.TwoFactorBackupCodes} {
			if ct == "" {
				continue
			}
			if _, err := decryptWithKey(s.aesKey, ct); err == nil {
				continue // already on the current key
			}
			plain, err := s.decryptSecret(ct)
			if err != nil {
				errs = append(errs, fmt.Errorf("user %s %s: %w", u.ID, col, err))
				continue
			}
			fresh, err := s.encryptSecret(plain)
			if err == nil {
				err = s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", u.ID).Update(col, fresh).Error
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("user %s %s: %w", u.ID, col, err))
				continue
			}
			n++
		}
	}
	return n, errors.Join(errs...)
}

func decryptWithKey(key []byte, cipherText string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
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

// RevokeUserTokens invalidates every outstanding access token for the user by
// advancing the revocation cutoff to now. RequireAuth rejects any token whose iat
// predates this instant.
func (s *UserService) RevokeUserTokens(userID uuid.UUID) error {
	return s.db.Model(&models.User{}).
		Where("id = ?", userID).
		Update("token_revoked_at", time.Now()).Error
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
			storedCodes[i] = "USED"
			updatedCodes, _ := json.Marshal(storedCodes)
			encryptedCodes, err := s.encryptSecret(string(updatedCodes))
			if err != nil {
				return false
			}
			// Conditional on the codes read above, so of two requests with the
			// same code only one gets in.
			res := s.db.Model(&models.User{}).Where("id = ? AND two_factor_backup_codes = ?", user.ID, user.TwoFactorBackupCodes).
				Update("two_factor_backup_codes", encryptedCodes)
			if res.Error != nil || res.RowsAffected != 1 {
				return false
			}
			user.TwoFactorBackupCodes = encryptedCodes
			return true
		}
	}

	return false
}
