package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// sealSecretsAtRest encrypts every stored credential that is still plaintext
// and re-encrypts any that only a retired AES key opens. It is idempotent
// (sealed values carry models.SealedPrefix) and runs at every boot, so the
// upgrade and each key rotation finish on their own. Values no key opens are
// reported and left untouched.
func sealSecretsAtRest(ctx context.Context, db *gorm.DB) (int, error) {
	db = db.WithContext(ctx)
	n := 0
	var errs []error
	for _, col := range []struct{ table, column string }{
		{"nodes", "token"}, {"vms", "vnc_password"}, {"import_sources", "api_key"}, {"import_sources", "api_pass"},
	} {
		var rows []struct{ ID, Value string }
		q := fmt.Sprintf("SELECT id, %s AS value FROM %s WHERE %s IS NOT NULL AND %s <> ''", col.column, col.table, col.column, col.column)
		if err := db.Raw(q).Scan(&rows).Error; err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", col.table, err))
			continue
		}
		for _, r := range rows {
			out, changed, err := models.ResealSecret(r.Value)
			if err == nil && changed {
				err = db.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ? AND %s = ?", col.table, col.column, col.column), out, r.ID, r.Value).Error
				if err == nil {
					n++
				}
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", col.table, r.ID, err))
			}
		}
	}

	var sections []struct{ Section, Data string }
	if err := db.Raw("SELECT section, CAST(data AS TEXT) AS data FROM system_settings").Scan(&sections).Error; err != nil {
		return n, errors.Join(append(errs, fmt.Errorf("system_settings: %w", err))...)
	}
	for _, s := range sections {
		var m map[string]interface{}
		if json.Unmarshal([]byte(s.Data), &m) != nil || m == nil {
			continue
		}
		changed := 0
		for _, k := range settingSecretKeys {
			v, _ := m[k].(string)
			out, ch, err := models.ResealSecret(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("setting %s.%s: %w", s.Section, k, err))
				continue
			}
			if ch {
				m[k] = out
				changed++
			}
		}
		if changed == 0 {
			continue
		}
		raw, err := json.Marshal(m)
		if err == nil {
			err = db.Exec("UPDATE system_settings SET data = ? WHERE section = ?", string(raw), s.Section).Error
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("setting %s: %w", s.Section, err))
			continue
		}
		n += changed
	}
	return n, errors.Join(errs...)
}
