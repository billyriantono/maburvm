package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

var (
	// ErrSSHKeyNotFound is returned when a key does not exist (or isn't owned).
	ErrSSHKeyNotFound = fmt.Errorf("SSH key not found")
	// ErrSSHKeyInvalid is returned when the supplied public key can't be parsed.
	ErrSSHKeyInvalid = fmt.Errorf("invalid SSH public key")
	// ErrSSHKeyDuplicate is returned when the user already has the same key.
	ErrSSHKeyDuplicate = fmt.Errorf("an SSH key with the same fingerprint already exists")
)

// SSHKeyService manages a user's saved SSH public keys.
type SSHKeyService struct {
	repo *repository.SSHKeyRepository
}

// NewSSHKeyService creates a new SSHKeyService.
func NewSSHKeyService(db *gorm.DB) *SSHKeyService {
	return &SSHKeyService{repo: repository.NewSSHKeyRepository(db)}
}

// CreateSSHKeyRequest is the payload for registering a public key.
type CreateSSHKeyRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

// CreateSSHKey validates and stores a user's SSH public key. The fingerprint is
// derived (SHA256) so duplicates are rejected and shown in the UI.
func (s *SSHKeyService) CreateSSHKey(ctx context.Context, userID string, req CreateSSHKeyRequest) (*models.SSHKey, error) {
	name := strings.TrimSpace(req.Name)
	pub := strings.TrimSpace(req.PublicKey)
	if pub == "" {
		return nil, ErrSSHKeyInvalid
	}

	parsed, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSSHKeyInvalid, err)
	}
	fingerprint := ssh.FingerprintSHA256(parsed)
	if name == "" {
		if comment != "" {
			name = comment
		} else {
			name = fingerprint
		}
	}

	exists, err := s.repo.ExistsByFingerprint(ctx, userID, fingerprint)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrSSHKeyDuplicate
	}

	// Store the canonical authorized_keys line (type + base64, plus comment).
	normalized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed)))
	if comment != "" {
		normalized += " " + comment
	}

	key := &models.SSHKey{
		UserID:      userID,
		Name:        name,
		PublicKey:   normalized,
		Fingerprint: fingerprint,
	}
	if err := s.repo.Create(ctx, key); err != nil {
		return nil, err
	}
	return key, nil
}

// GenerateSSHKeyRequest is the payload for generating a keypair server-side.
type GenerateSSHKeyRequest struct {
	Name string `json:"name"`
}

// GeneratedSSHKey pairs the stored public key with the private key PEM, which
// is returned to the caller exactly once and never persisted or logged.
type GeneratedSSHKey struct {
	Key        *models.SSHKey `json:"key"`
	PrivateKey string         `json:"private_key"`
}

// GenerateSSHKey creates an ed25519 keypair, stores only the public half via
// the normal creation path, and returns the private key PEM one time.
func (s *SSHKeyService) GenerateSSHKey(ctx context.Context, userID string, req GenerateSSHKeyRequest) (*GeneratedSSHKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("failed to encode public key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, fmt.Errorf("failed to encode private key: %w", err)
	}
	key, err := s.CreateSSHKey(ctx, userID, CreateSSHKeyRequest{
		Name:      req.Name,
		PublicKey: string(ssh.MarshalAuthorizedKey(sshPub)),
	})
	if err != nil {
		return nil, err
	}
	return &GeneratedSSHKey{Key: key, PrivateKey: string(pem.EncodeToMemory(block))}, nil
}

// ListSSHKeys returns all of the user's saved keys (newest first).
func (s *SSHKeyService) ListSSHKeys(ctx context.Context, userID string) ([]models.SSHKey, error) {
	return s.repo.ListByUserID(ctx, userID)
}

// DeleteSSHKey removes a user's key.
func (s *SSHKeyService) DeleteSSHKey(ctx context.Context, id, userID string) error {
	if _, err := s.repo.GetByID(ctx, id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSSHKeyNotFound
		}
		return err
	}
	return s.repo.Delete(ctx, id, userID)
}

// ResolvePublicKeys returns the public-key lines for the given key IDs owned by
// the user, de-duplicated. Unknown or unowned IDs are silently ignored.
func (s *SSHKeyService) ResolvePublicKeys(ctx context.Context, userID string, ids []string) ([]string, error) {
	keys, err := s.repo.ListByIDsForUser(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		line := strings.TrimSpace(k.PublicKey)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out, nil
}
