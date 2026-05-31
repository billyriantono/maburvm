package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrAPIKeyNotFound is returned when an API key does not exist.
	ErrAPIKeyNotFound = errors.New("api key not found")
	// ErrAPIKeyInvalid is returned when a presented token is malformed, unknown, revoked, or expired.
	ErrAPIKeyInvalid = errors.New("invalid or expired api key")
)

// APIKeyService manages per-user API automation credentials.
type APIKeyService struct {
	repo *repository.APIKeyRepository
}

// NewAPIKeyService creates a new APIKeyService.
func NewAPIKeyService(db *gorm.DB) *APIKeyService {
	return &APIKeyService{repo: repository.NewAPIKeyRepository(db)}
}

// CreateAPIKeyRequest is the input for creating an API key.
type CreateAPIKeyRequest struct {
	Name      string     `json:"name" validate:"required,max=100"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// CreateAPIKey generates a new token, stores only its hash, and returns the
// plaintext token exactly once (callers must surface it to the user immediately).
func (s *APIKeyService) CreateAPIKey(ctx context.Context, userID string, req CreateAPIKeyRequest) (*models.APIKey, string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, "", errors.New("name is required")
	}

	token, err := generateAPIToken()
	if err != nil {
		return nil, "", err
	}

	key := &models.APIKey{
		UserID:    userID,
		Name:      name,
		KeyHash:   models.HashAPIToken(token),
		Prefix:    token[:12],
		ExpiresAt: req.ExpiresAt,
		IsActive:  true,
	}
	if err := s.repo.Create(ctx, key); err != nil {
		return nil, "", err
	}
	return key, token, nil
}

// ListAPIKeys returns all keys owned by the user (without secrets).
func (s *APIKeyService) ListAPIKeys(ctx context.Context, userID string) ([]models.APIKey, error) {
	return s.repo.ListByUserID(ctx, userID)
}

// RevokeAPIKey deletes a key owned by the user.
func (s *APIKeyService) RevokeAPIKey(ctx context.Context, id, userID string) error {
	key, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAPIKeyNotFound
		}
		return err
	}
	if key.UserID != userID {
		return ErrAPIKeyNotFound // do not reveal existence of other users' keys
	}
	return s.repo.Delete(ctx, id, userID)
}

// Authenticate validates a presented token and returns the owning key.
// On success it best-effort records last-used time. Returns ErrAPIKeyInvalid
// for any malformed/unknown/revoked/expired token.
func (s *APIKeyService) Authenticate(ctx context.Context, token string) (*models.APIKey, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, models.APITokenPrefix) {
		return nil, ErrAPIKeyInvalid
	}
	key, err := s.repo.GetByHash(ctx, models.HashAPIToken(token))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAPIKeyInvalid
		}
		return nil, err
	}
	if !key.IsValid() {
		return nil, ErrAPIKeyInvalid
	}
	// Best-effort; failure to record last-used must not block auth.
	if err := s.repo.TouchLastUsed(ctx, key.ID); err == nil {
		now := time.Now()
		key.LastUsedAt = &now
	}
	return key, nil
}

// generateAPIToken returns a high-entropy token of the form "mvk_<base64url>".
func generateAPIToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return models.APITokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}
