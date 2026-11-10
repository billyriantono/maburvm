package models

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// APITokenPrefix namespaces MaburVM API tokens so they are recognizable on the wire.
const APITokenPrefix = "mvk_"

// HashAPIToken returns the lowercase hex SHA-256 of a token. Tokens carry 256
// bits of entropy, so a fast hash (vs. bcrypt) is appropriate and enables O(1)
// indexed lookup on every authenticated request. This is the single source of
// truth for how tokens map to stored hashes (used by both service and middleware).
func HashAPIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// APIKey is a scoped, hashed credential a user creates for API automation.
// The plaintext token is shown once at creation; only its SHA-256 hash is stored.
type APIKey struct {
	ID         string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID     string     `json:"user_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	Name       string     `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	KeyHash    string     `json:"-" gorm:"type:varchar(64);not null;uniqueIndex"` // sha256 hex of the token
	Prefix     string     `json:"prefix" gorm:"type:varchar(20);not null"`        // display prefix, e.g. mvk_ab12cd34
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	IsActive   bool       `json:"is_active" gorm:"default:true"`
	// Role, when "client", makes the key act as a plain tenant even if its owner
	// is an admin. Empty means the owner's role. See EffectiveRole.
	Role UserRole `json:"role,omitempty" gorm:"type:varchar(20)"`
	// ReadOnly keys may only make safe (GET/HEAD) requests.
	ReadOnly  bool           `json:"read_only" gorm:"not null;default:false"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for APIKey.
func (APIKey) TableName() string { return "api_keys" }

// BeforeCreate hook for APIKey.
func (k *APIKey) BeforeCreate(tx *gorm.DB) error {
	if k.ID == "" {
		k.ID = uuid.New().String()
	}
	return nil
}

// EffectiveRole is the role a request made with this key acts as. A key can
// only narrow its owner's role, never widen it: if the owner is demoted, the
// key follows.
func (k *APIKey) EffectiveRole(owner UserRole) UserRole {
	if k.Role == RoleClient {
		return RoleClient
	}
	return owner
}

// IsValid reports whether the key is active and not expired.
func (k *APIKey) IsValid() bool {
	if !k.IsActive {
		return false
	}
	if k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}
