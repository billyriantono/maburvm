package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
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
	db   *gorm.DB
}

// NewAPIKeyService creates a new APIKeyService.
func NewAPIKeyService(db *gorm.DB) *APIKeyService {
	return &APIKeyService{repo: repository.NewAPIKeyRepository(db), db: db}
}

// CreateAPIKeyRequest is the input for creating an API key.
type CreateAPIKeyRequest struct {
	Name      string     `json:"name" validate:"required,max=100"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Role narrows what the key may do: "" (act as the owner) or "client".
	Role models.UserRole `json:"role,omitempty"`
	// ReadOnly limits the key to GET requests (the read_only scope).
	ReadOnly bool `json:"read_only,omitempty"`
	// Scope narrows the key within the owner's role: full (the default),
	// read_only, vm_power (list VMs, start, stop, restart) or billing (billing,
	// plans, accounts and usage).
	Scope string `json:"scope,omitempty"`
	// AllowedIPs are the addresses or CIDR ranges the key may be used from.
	// Empty means anywhere.
	AllowedIPs []string `json:"allowed_ips,omitempty"`
	// ClientIP is where the request came from, for the owner's email.
	ClientIP string `json:"-"`
}

// CreateAPIKey generates a new token, stores only its hash, and returns the
// plaintext token exactly once (callers must surface it to the user immediately).
// ownerRole is the caller's role; a key may never be granted more than that.
func (s *APIKeyService) CreateAPIKey(ctx context.Context, userID string, ownerRole models.UserRole, req CreateAPIKeyRequest) (*models.APIKey, string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, "", errors.New("name is required")
	}
	switch req.Role {
	case "", ownerRole:
		req.Role = "" // same as the owner: store as inherit
	case models.RoleClient:
	default:
		return nil, "", errors.New("role must be empty or a role no higher than your own")
	}

	scope := strings.TrimSpace(req.Scope)
	switch {
	case scope == "" && req.ReadOnly:
		scope = models.APIKeyScopeReadOnly
	case scope == "":
		scope = models.APIKeyScopeFull
	case !slices.Contains(models.APIKeyScopes, scope):
		return nil, "", errors.New("scope must be one of full, read_only, vm_power or billing")
	case req.ReadOnly && scope != models.APIKeyScopeReadOnly:
		return nil, "", errors.New("read_only and a scope other than read_only cannot be combined")
	}
	allowed, err := cleanAllowedIPs(req.AllowedIPs)
	if err != nil {
		return nil, "", err
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		return nil, "", errors.New("expires_at must be in the future")
	}

	token, err := generateAPIToken()
	if err != nil {
		return nil, "", err
	}

	key := &models.APIKey{
		UserID:     userID,
		Name:       name,
		KeyHash:    models.HashAPIToken(token),
		Prefix:     token[:12],
		ExpiresAt:  req.ExpiresAt,
		IsActive:   true,
		Role:       req.Role,
		ReadOnly:   scope == models.APIKeyScopeReadOnly,
		Scope:      scope,
		AllowedIPs: allowed,
	}
	if err := s.repo.Create(ctx, key); err != nil {
		return nil, "", err
	}
	if s.db != nil {
		if err := NotifyAccount(context.WithoutCancel(ctx), s.db, userID, EmailAPIKeyCreated,
			map[string]string{"key": name, "who": ChangeSource(false, req.ClientIP)}); err != nil {
			slog.Default().Warn("API key created, but its owner could not be told", "user_id", userID, "error", err)
		}
	}
	return key, token, nil
}

// maxAllowedIPs bounds a key's address list; a longer one is a firewall's job.
const maxAllowedIPs = 50

// cleanAllowedIPs trims and checks a key's address list: each entry an IP
// address or a CIDR range.
func cleanAllowedIPs(in []string) ([]string, error) {
	out := []string{}
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(v); err != nil && net.ParseIP(v) == nil {
			return nil, fmt.Errorf("%q is not an IP address or a CIDR range", v)
		}
		out = append(out, v)
	}
	if len(out) > maxAllowedIPs {
		return nil, fmt.Errorf("a key may list at most %d addresses", maxAllowedIPs)
	}
	return out, nil
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
