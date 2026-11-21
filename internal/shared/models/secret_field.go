package models

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"

	"github.com/maburvm/panel/internal/shared/secret"
	"gorm.io/gorm/schema"
)

// SealedPrefix marks a value encrypted by SealSecret. Plaintext from before
// encryption at rest never carries it, so a value is never encrypted twice and
// legacy rows stay readable until the boot pass seals them.
const SealedPrefix = "enc:v1:"

var secretKeys struct {
	sync.RWMutex
	current  []byte
	previous [][]byte
}

// SetSecretKeys installs the panel's AES key (and retired keys still accepted
// for decryption). Called once at boot, before any database access.
func SetSecretKeys(current string, previous ...string) {
	secretKeys.Lock()
	defer secretKeys.Unlock()
	secretKeys.current = []byte(current)
	secretKeys.previous = nil
	for _, k := range previous {
		secretKeys.previous = append(secretKeys.previous, []byte(k))
	}
}

// IsSealed reports whether v was produced by SealSecret.
func IsSealed(v string) bool { return strings.HasPrefix(v, SealedPrefix) }

// SealSecret encrypts a credential for storage. Empty and already sealed values
// are returned unchanged. With no key installed (unit tests) it stores plaintext.
func SealSecret(plain string) (string, error) {
	if plain == "" || IsSealed(plain) {
		return plain, nil
	}
	secretKeys.RLock()
	key := secretKeys.current
	secretKeys.RUnlock()
	if len(key) == 0 {
		return plain, nil
	}
	enc, err := secret.Encrypt(key, plain)
	if err != nil {
		return "", err
	}
	return SealedPrefix + enc, nil
}

// OpenSecret returns the plaintext of a stored credential. A value without the
// prefix is legacy plaintext and is returned as is.
func OpenSecret(v string) (string, error) {
	plain, _, err := openSecret(v)
	return plain, err
}

// openSecret also reports whether only a retired key could open v.
func openSecret(v string) (plain string, stale bool, err error) {
	if !IsSealed(v) {
		return v, false, nil
	}
	secretKeys.RLock()
	current, previous := secretKeys.current, secretKeys.previous
	secretKeys.RUnlock()
	if len(current) == 0 {
		return "", false, errors.New("encrypted value but no AES key is configured")
	}
	body := strings.TrimPrefix(v, SealedPrefix)
	if plain, err = secret.Decrypt(current, body); err == nil {
		return plain, false, nil
	}
	for _, k := range previous {
		if plain, err = secret.Decrypt(k, body); err == nil {
			return plain, true, nil
		}
	}
	return "", false, fmt.Errorf("cannot decrypt a stored secret (AES key changed without AES_KEY_PREVIOUS?): %w", err)
}

// ResealSecret returns v sealed under the current key, and whether that differs
// from v: plaintext gets sealed, a value on a retired key is re-sealed. Used by
// the boot pass that brings every stored credential onto the current key.
func ResealSecret(v string) (string, bool, error) {
	if v == "" {
		return v, false, nil
	}
	plain, stale, err := openSecret(v)
	if err != nil {
		return v, false, err
	}
	if IsSealed(v) && !stale {
		return v, false, nil
	}
	out, err := SealSecret(plain)
	return out, err == nil && out != v, err
}

// secretSerializer encrypts a string field on write and decrypts it on read
// (`gorm:"serializer:secret"`). GORM skips serializers for Update("col", v)
// and map updates: those callers must pass SealSecret(v) themselves.
type secretSerializer struct{}

func (secretSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	var raw string
	switch v := dbValue.(type) {
	case nil:
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("secret field %s: unsupported database value %T", field.Name, dbValue)
	}
	plain, err := OpenSecret(raw)
	if err != nil {
		// Fail soft: one unreadable credential must not make every VM or node
		// list fail. The field reads empty (a VNC password is then regenerated).
		slog.Default().Error("stored secret could not be decrypted", "table", field.Schema.Table, "field", field.Name, "error", err)
		plain = ""
	}
	field.ReflectValueOf(ctx, dst).SetString(plain)
	return nil
}

func (secretSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue interface{}) (interface{}, error) {
	s, _ := fieldValue.(string)
	return SealSecret(s)
}

func init() {
	schema.RegisterSerializer("secret", secretSerializer{})
}
