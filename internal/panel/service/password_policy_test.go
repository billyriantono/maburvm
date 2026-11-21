package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPasswordPolicyComesFromSecuritySettings(t *testing.T) {
	db := setupStorageTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT)`).Error)
	save := func(data string) {
		require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
		require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('security', ?)`, data).Error)
	}

	// Nothing saved: the policy the panel has always had.
	assert.NoError(t, ValidatePasswordStrength(db, "Correct-horse-9"))
	assert.ErrorIs(t, ValidatePasswordStrength(db, "Short-pw-9"), ErrWeakPassword, "under 12 characters")
	assert.ErrorIs(t, ValidatePasswordStrength(db, "correct-horse-9"), ErrWeakPassword, "no uppercase")
	assert.ErrorIs(t, ValidatePasswordStrength(db, "Correct-horse-x"), ErrWeakPassword, "no digit")
	assert.ErrorIs(t, ValidatePasswordStrength(db, "Correcthorse99"), ErrWeakPassword, "no symbol")
	assert.NoError(t, ValidatePasswordStrength(nil, "Correct-horse-9"), "no database at hand: the built-in policy")

	// Fields the old form also saved (session timeout, lockout) are ignored.
	save(`{"minPasswordLength": 16, "requireUppercase": true, "requireNumbers": true, "requireSpecial": true, "sessionTimeout": 60, "maxLoginAttempts": 5}`)
	assert.ErrorIs(t, ValidatePasswordStrength(db, "Correct-horse-9"), ErrWeakPassword, "15 characters under a minimum of 16")
	assert.NoError(t, ValidatePasswordStrength(db, "Correct-horse-99"))

	save(`{"minPasswordLength": 10, "requireUppercase": false, "requireNumbers": false, "requireSpecial": false}`)
	assert.NoError(t, ValidatePasswordStrength(db, "correcthorse"))
	assert.ErrorIs(t, ValidatePasswordStrength(db, "CORRECTHORSE"), ErrWeakPassword, "a lowercase letter is always required")

	// A stored length nobody should run with is pulled back to the floor.
	save(`{"minPasswordLength": 1, "requireUppercase": false, "requireNumbers": false, "requireSpecial": false}`)
	assert.Equal(t, 8, LoadPasswordPolicy(db).MinLength)
	assert.ErrorIs(t, ValidatePasswordStrength(db, "abcdefg"), ErrWeakPassword)

	// A partial save keeps the built-in value of what it leaves out.
	save(`{"minPasswordLength": 14}`)
	p := LoadPasswordPolicy(db)
	assert.True(t, p.RequireUppercase && p.RequireNumbers && p.RequireSpecial)
}
