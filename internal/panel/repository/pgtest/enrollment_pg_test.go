//go:build pg
// +build pg

// Package repository_pg contains native PostgreSQL 15 evidence for the Phase 1A
// Gate-1 enrollment-control remediation, exercised on the REAL migration chain so
// the enrollment fixtures run against a schema consistent with production. It is
// guarded by the `pg` build tag so the default `go test ./...` (and CI without a
// live PG) never tries to connect. Run it against a local PG 15 cluster:
//
//	PGHOST=/tmp PGUSER=postgres PGDATABASE=maburvm_enroll_test \
//	  go test -tags pg ./internal/panel/repository/pgtest/ -v
//
// The harness:
//   - applies the COMPLETE lexical migration chain 001..039 (including the
//     managed-quota-cap lane 037 / 037a / 037b and the 039 snapshot-integrity
//     migration) inside the runner's per-migration transaction +
//     schema_migrations recording model, mirroring cmd/migrate/main.go. No
//     migration is skipped; every applied file must run cleanly against the
//     current schema so the fixtures (and 039's users.quota_mode dependency) hold.
//   - resets the target database between sub-scenarios,
//   - and exercises the DB-contract behaviors that SQLite cannot prove:
//     reset-shape guard states, control-plane activation/replacement/retirement,
//     rollback + orphan/active-hole rejection, reset + invite lifecycle, and
//     outer-transaction (row-lock) behavior.
//
// It connects via the pgx stdlib driver on the Unix socket in PGHOST (default
// /tmp) and is read/write on its own dedicated database only.
package repository_pg

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	host := os.Getenv("PGHOST")
	if host == "" {
		host = "/tmp"
	}
	user := os.Getenv("PGUSER")
	if user == "" {
		user = "postgres"
	}
	dbname := os.Getenv("PGDATABASE")
	if dbname == "" {
		dbname = "maburvm_enroll_test"
	}
	return fmt.Sprintf("host=%s user=%s dbname=%s sslmode=disable", host, user, dbname)
}

func openPG(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", pgDSN(t))
	require.NoError(t, err, "open pg")
	require.NoError(t, db.PingContext(context.Background()), "ping pg")
	return db
}

// applyMigrations applies every *.up.sql in lexical order (the full chain,
// 001..039 — including 037 / 037a / 037b and 039) inside the runner's
// single-transaction + schema_migrations recording model, mirroring
// cmd/migrate/main.go. No file is skipped; the complete chain must apply so the
// fixtures run against a schema consistent with production (e.g. 039's
// users.quota_mode dependency requires 037's column).
func migrationsDir(t *testing.T) string {
	t.Helper()
	// Resolve the migrations dir relative to the repo root by walking up to the
	// directory containing go.mod (the test binary runs in the package dir).
	dir, err := os.Getwd()
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "internal", "shared", "db", "migrations")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate migrations dir from cwd %s", dir)
	return ""
}

func applyMigrations(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`)
	require.NoError(t, err)

	dir := migrationsDir(t)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	for _, name := range files {
		// Apply the full lexical migration chain with no skips. The quota-cap
		// lane (037 / 037a / 037b) and 039 are part of the production schema and
		// must run so fixtures stay consistent (e.g. users.quota_mode and the
		// quota-policy cap-binding trigger from 037).
		version := strings.TrimSuffix(name, ".up.sql")
		var exists string
		_ = db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version=$1`, version).Scan(&exists)
		if exists == version {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(contents))
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s: %v", version, err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}
}

// resetDB drops the public schema and re-applies the complete migration chain
// (001..039, with no skips) so each scenario starts from a freshly migrated
// schema. schema_migrations lives in public, so we recreate it (mirroring
// cmd/migrate's ensureSchemaMigrations) after the drop.
func resetDB(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `DROP SCHEMA IF EXISTS public CASCADE`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE SCHEMA public`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`)
	require.NoError(t, err)
	applyMigrations(t, db)
}

func exec(t *testing.T, db *sql.DB, q string, args ...interface{}) sql.Result {
	t.Helper()
	r, err := db.ExecContext(context.Background(), q, args...)
	require.NoError(t, err, q)
	return r
}

func seedUsers(t *testing.T, db *sql.DB) (creator string) {
	t.Helper()
	creator = "11111111-1111-1111-1111-111111111111"
	exec(t, db, `INSERT INTO users (id, email, password_hash, role) VALUES ($1,'admin@example.com','h','admin')`, creator)
	return
}

func seedQuotaPolicy(t *testing.T, db *sql.DB) (policyID, versionID string) {
	t.Helper()
	ctx := context.Background()

	// The 037 chain binds every quota_policy_versions row to the ACTIVE platform
	// cap revision (trigger trg_quota_policy_version_cap) and 039 adds snapshot
	// integrity over the same. To keep this fixture valid after the cap
	// invariants, we transactionally publish and activate a sufficiently WIDE
	// platform cap (a real control-plane operation) BEFORE inserting the policy
	// version, then insert a version that sits safely under that cap. We do not
	// weaken or skip any integrity migration.
	policyID = "22222222-2222-2222-2222-222222222222"
	versionID = "33333333-3333-3333-3333-333333333333"
	capRevID := "44444444-4444-4444-4444-444444444444"

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)

	// 1) insert the (initially candidate) platform cap revision.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO platform_quota_cap_revisions
			(id,max_vms,max_vcpu,max_ram_mb,max_disk_gb,state,revision,note)
		VALUES ($1,$2,$3,$4,$5,'candidate',1,'harness-wide-cap')`,
		capRevID, 100000, 100000, 8*1024*1024, 100000)
	require.NoError(t, err, "insert platform cap revision")

	// 2) activate it along the singleton control-plane row (candidate -> active
	//    requires activated_at; the deferred coherence check fires at COMMIT).
	_, err = tx.ExecContext(ctx, `
		UPDATE platform_quota_cap_revisions
			SET state='active', activated_at=now() WHERE id=$1`, capRevID)
	require.NoError(t, err, "activate platform cap revision")
	_, err = tx.ExecContext(ctx, `
		UPDATE platform_quota_cap_state
			SET state='active', active_revision_id=$1 WHERE singleton_key='A'`, capRevID)
	require.NoError(t, err, "point singleton at active cap")

	// 3) now the policy + cap-bound version are valid (within the wide cap).
	_, err = tx.ExecContext(ctx, `
		INSERT INTO quota_policies (id,name,lifecycle) VALUES ($1,'default','active')`, policyID)
	require.NoError(t, err, "insert quota policy")
	_, err = tx.ExecContext(ctx, `
		INSERT INTO quota_policy_versions
			(id,policy_id,version,max_vms,max_vcpu,max_ram_mb,max_disk_gb)
		VALUES ($1,$2,1,5,4,8192,100)`, versionID, policyID)
	require.NoError(t, err, "insert quota policy version")

	require.NoError(t, tx.Commit(), "commit seeded quota policy + active cap")

	// Self-check: the version must be cap-bound (037 stamps cap_revision_id) so
	// 039's snapshot integrity holds. Fail closed if the invariant slipped.
	var capBound string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT cap_revision_id FROM quota_policy_versions WHERE id=$1`, versionID).
		Scan(&capBound))
	require.NotEmpty(t, capBound, "seeded policy version must be bound to an active cap")
	return
}

// ===================== Reset-shape guard (033a) =====================

func TestPG_ResetShapeGuardStates(t *testing.T) {
	db := openPG(t)
	defer db.Close()

	cases := []struct {
		name   string
		setup  func(t *testing.T, db *sql.DB)
		accept bool
	}{
		{
			name:   "canonical_hash_only_present",
			accept: true, // 034 created canonical table; 033a no-ops
			setup:  func(t *testing.T, db *sql.DB) {},
		},
		{
			name: "mixed_token_and_token_hash",
			setup: func(t *testing.T, db *sql.DB) {
				exec(t, db, `DROP TABLE IF EXISTS password_reset_tokens`)
				exec(t, db, `CREATE TABLE password_reset_tokens (
					id uuid primary key, user_id uuid, token_hash varchar(64), token varchar(255), expires_at timestamptz)`)
			},
			accept: false,
		},
		{
			name: "unknown_extra_shape",
			setup: func(t *testing.T, db *sql.DB) {
				exec(t, db, `DROP TABLE IF EXISTS password_reset_tokens`)
				exec(t, db, `CREATE TABLE password_reset_tokens (
					id uuid primary key, user_id uuid, token varchar(255), expires_at timestamptz, used boolean, created_at timestamptz, extra_col int)`)
			},
			accept: false,
		},
		{
			name: "exact_legacy_shape_034_not_recorded",
			setup: func(t *testing.T, db *sql.DB) {
				exec(t, db, `DROP TABLE IF EXISTS password_reset_tokens`)
				exec(t, db, `CREATE TABLE password_reset_tokens (
					id uuid primary key, user_id uuid, token varchar(255) unique not null,
					expires_at timestamptz not null, used boolean default false,
					created_at timestamptz not null default now())`)
				exec(t, db, `DELETE FROM schema_migrations WHERE version='034_enrollment_control_plane'`)
			},
			accept: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetDB(t, db)
			seedUsers(t, db)
			seedQuotaPolicy(t, db)
			tc.setup(t, db)

			contents, err := os.ReadFile(filepath.Join(migrationsDir(t), "033a_reset_shape_guard.up.sql"))
			require.NoError(t, err)
			_, appErr := db.ExecContext(context.Background(), string(contents))
			if tc.accept {
				require.NoError(t, appErr, "033a should accept this shape")
			} else {
				require.Error(t, appErr, "033a should reject this shape")
			}
		})
	}
}

// ===================== Control plane: activation / replacement / retirement =====================

func seedRevisions(t *testing.T, db *sql.DB, urlRev, smtpRev string, urlRevNo, smtpRevNo int) {
	t.Helper()
	exec(t, db, `INSERT INTO public_url_revisions (id,origin,revision,state) VALUES ($1,'https://a.example.com',$2,'candidate')`, urlRev, urlRevNo)
	exec(t, db, `INSERT INTO smtp_config_revisions (id,host,port,from_address,transport,password_ciphertext,password_nonce,envelope_key_version,revision,state)
		VALUES ($1,'smtp','587','n@e.com','starttls','cipher','123456789012',1,$2,'candidate')`, smtpRev, smtpRevNo)
}

func TestPG_ControlPlaneActivationReplacementRetirement(t *testing.T) {
	db := openPG(t)
	defer db.Close()
	resetDB(t, db)
	seedUsers(t, db)
	seedQuotaPolicy(t, db)

	urlA := "aaaaaaaa-0000-0000-0000-000000000001"
	urlB := "bbbbbbbb-0000-0000-0000-000000000002"
	smtpA := "aaaaaaaa-0000-0000-0000-000000000003"
	smtpB := "bbbbbbbb-0000-0000-0000-000000000004"
	seedRevisions(t, db, urlA, smtpA, 1, 1)
	seedRevisions(t, db, urlB, smtpB, 2, 2)

	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE public_url_revisions SET state='active' WHERE id=$1`, urlA)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE public_url_state SET active_revision_id=$1, state='active' WHERE singleton_key='A'`, urlA)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	var st string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT state FROM public_url_state WHERE singleton_key='A'`).Scan(&st))
	require.Equal(t, "active", st)

	// Replacement: retire urlA then activate urlB in one txn.
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE public_url_revisions SET state='retired' WHERE id=$1`, urlA)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE public_url_revisions SET state='active' WHERE id=$1`, urlB)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE public_url_state SET active_revision_id=$1 WHERE singleton_key='A'`, urlB)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Orphan rejection: point singleton at a RETIRED revision (urlA) while urlB
	// is still active -> deferred constraint must fail at commit.
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE public_url_state SET active_revision_id=$1 WHERE singleton_key='A'`, urlA)
	require.NoError(t, err)
	require.Error(t, tx.Commit(), "orphan pointer (retired revision) must be rejected")

	// Orphan-active-state rejection: active state with zero active revisions.
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE public_url_revisions SET state='retired' WHERE id=$1`, urlB)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE public_url_state SET state='active', active_revision_id=$1 WHERE singleton_key='A'`, urlB)
	require.NoError(t, err)
	require.Error(t, tx.Commit(), "active state with zero active revisions must be rejected")
}

// ===================== Reset + invite lifecycle =====================

func TestPG_ResetLifecycleDBBackstops(t *testing.T) {
	db := openPG(t)
	defer db.Close()
	resetDB(t, db)
	uid := seedUsers(t, db)

	ctx := context.Background()
	tok := "aaaaaaaa-0000-0000-0000-000000000010"
	th := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	exec(t, db, `INSERT INTO password_reset_tokens (id,user_id,token_hash,expires_at)
		VALUES ($1,$2,$3,now()+interval '1 hour')`, tok, uid, th)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at=now() WHERE id=$1`, tok)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at=now()+interval '1 hour' WHERE id=$1`, tok)
	require.Error(t, tx.Commit(), "consumed_at immutable after set")

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET attempt_count=attempt_count+1 WHERE id=$1`, tok)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET attempt_count=attempt_count-5 WHERE id=$1`, tok)
	require.Error(t, tx.Commit(), "attempt_count must be monotonic")
}

func TestPG_InviteLifecycle72hAndTerminalStates(t *testing.T) {
	db := openPG(t)
	defer db.Close()
	resetDB(t, db)
	uid := seedUsers(t, db)
	_, qv := seedQuotaPolicy(t, db)
	urlRev := "aaaaaaaa-0000-0000-0000-000000000020"
	smtpRev := "aaaaaaaa-0000-0000-0000-000000000021"
	seedRevisions(t, db, urlRev, smtpRev, 1, 1)
	ctx := context.Background()

	inv := "aaaaaaaa-0000-0000-0000-000000000030"
	th := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	exec(t, db, `INSERT INTO registration_invites
		(id,recipient_email,recipient_role,creator_id,quota_policy_version_id,url_revision_id,smtp_revision_id,token_hash,state,expires_at)
		VALUES ($1,'i@example.com','client',$2,$3,$4,$5,$6,'pending_delivery',now()+interval '24 hours')`,
		inv, uid, qv, urlRev, smtpRev, th)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE registration_invites SET state='active' WHERE id=$1`, inv)
	require.Error(t, tx.Commit(), "active requires sent_at (CHECK)")

	now := time.Now()
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`UPDATE registration_invites SET state='active', sent_at=$1::timestamptz, expires_at=$1::timestamptz+interval '72 hours' WHERE id=$2`,
		now, inv)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	var exp time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT expires_at FROM registration_invites WHERE id=$1`, inv).Scan(&exp))
	require.Equal(t, now.Add(72*time.Hour).Truncate(time.Second), exp.Truncate(time.Second))

	fail := "aaaaaaaa-0000-0000-0000-000000000031"
	exec(t, db, `INSERT INTO registration_invites
		(id,recipient_email,recipient_role,creator_id,quota_policy_version_id,url_revision_id,smtp_revision_id,token_hash,state,expires_at)
		VALUES ($1,'f@example.com','client',$2,$3,$4,$5,$6,'pending_delivery',now()+interval '24 hours')`,
		fail, uid, qv, urlRev, smtpRev, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	exec(t, db, `UPDATE registration_invites SET state='delivery_failed' WHERE id=$1`, fail)
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE registration_invites SET state='revoked' WHERE id=$1`, fail)
	require.Error(t, tx.Commit(), "delivery_failed is terminal and not revocable")

	// Gap 3 proof: a pending_delivery row can NEVER satisfy the active coherence
	// CHECK, so it is never consumable. Attempting to mark it consumed while
	// still pending (no sent_at) must fail the active_coherent CHECK, proving the
	// stale pre-delivery expires_at is not a security boundary.
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`UPDATE registration_invites SET state='consumed', consumed_at=now() WHERE id=$1`, fail)
	require.Error(t, tx.Commit(), "pending/delivery_failed invite cannot become consumed (not active)")

	// registration_invites_pending_contract: a pending row must not carry
	// sent_at/consumed_at. Setting sent_at on a pending row must be rejected.
	p2 := "aaaaaaaa-0000-0000-0000-000000000032"
	exec(t, db, `INSERT INTO registration_invites
		(id,recipient_email,recipient_role,creator_id,quota_policy_version_id,url_revision_id,smtp_revision_id,token_hash,state,expires_at)
		VALUES ($1,'p2@example.com','client',$2,$3,$4,$5,$6,'pending_delivery',now()+interval '24 hours')`,
		p2, uid, qv, urlRev, smtpRev, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE registration_invites SET sent_at=now() WHERE id=$1`, p2)
	require.Error(t, tx.Commit(), "pending invite must not carry sent_at (pending_contract CHECK)")
}

// TestPG_ResetInsertInvariants pins Gap 2: reset tokens must be inserted
// unconsumed (consumed_at NULL), with attempt_count = 0 and last_attempt_at
// NULL; a non-null last_attempt_at must correspond to attempt_count > 0.
func TestPG_ResetInsertInvariants(t *testing.T) {
	db := openPG(t)
	defer db.Close()
	resetDB(t, db)
	uid := seedUsers(t, db)
	ctx := context.Background()
	th := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// Insert already-consumed -> rejected.
	tokC := "aaaaaaaa-0000-0000-0000-000000000050"
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (id,user_id,token_hash,expires_at,consumed_at)
		 VALUES ($1,$2,$3,now()+interval '1 hour',now())`, tokC, uid, th)
	require.Error(t, tx.Commit(), "reset insert with consumed_at must be rejected")

	// Insert with non-zero attempt_count -> rejected.
	tokA := "aaaaaaaa-0000-0000-0000-000000000051"
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (id,user_id,token_hash,expires_at,attempt_count)
		 VALUES ($1,$2,$3,now()+interval '1 hour',3)`, tokA, uid, th)
	require.Error(t, tx.Commit(), "reset insert with attempt_count<>0 must be rejected")

	// Insert with last_attempt_at but zero attempts -> rejected (incoherent).
	tokL := "aaaaaaaa-0000-0000-0000-000000000052"
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO password_reset_tokens (id,user_id,token_hash,expires_at,attempt_count,last_attempt_at)
		 VALUES ($1,$2,$3,now()+interval '1 hour',0,now())`, tokL, uid, th)
	require.Error(t, tx.Commit(), "reset insert with last_attempt_at but zero attempts must be rejected")

	// Valid insert (unconsumed, zero attempts, null last_attempt) -> accepted.
	tokOK := "aaaaaaaa-0000-0000-0000-000000000053"
	exec(t, db, `INSERT INTO password_reset_tokens (id,user_id,token_hash,expires_at)
		VALUES ($1,$2,$3,now()+interval '1 hour')`, tokOK, uid, th)
	var consumed sql.NullTime
	var attempts int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT consumed_at, attempt_count FROM password_reset_tokens WHERE id=$1`, tokOK).
		Scan(&consumed, &attempts))
	require.False(t, consumed.Valid, "freshly inserted reset token must be unconsumed")
	require.Equal(t, 0, attempts, "freshly inserted reset token must have zero attempts")
}

// ===================== Outer transaction: row lock + rollback =====================

func TestPG_OuterTransactionConsumeReset(t *testing.T) {
	db := openPG(t)
	defer db.Close()
	resetDB(t, db)
	uid := seedUsers(t, db)
	ctx := context.Background()
	tok := "aaaaaaaa-0000-0000-0000-000000000040"
	th := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	exec(t, db, `INSERT INTO password_reset_tokens (id,user_id,token_hash,expires_at)
		VALUES ($1,$2,$3,now()+interval '1 hour')`, tok, uid, th)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at=now(), attempt_count=attempt_count+1 WHERE id=$1`, tok)
	require.NoError(t, err)
	_ = tx.Rollback()

	var consumed sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `SELECT consumed_at FROM password_reset_tokens WHERE id=$1`, tok).Scan(&consumed))
	require.False(t, consumed.Valid, "rollback must leave token unconsumed")

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at=now(), attempt_count=attempt_count+1 WHERE id=$1`, tok)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	require.NoError(t, db.QueryRowContext(ctx, `SELECT consumed_at FROM password_reset_tokens WHERE id=$1`, tok).Scan(&consumed))
	require.True(t, consumed.Valid, "committed outer tx consumes token")
}

// ===================== 038: invite consumption lifecycle =====================

func seedActiveInvite(t *testing.T, db *sql.DB, id, th, uid, qv, urlRev, smtpRev string) {
	t.Helper()
	exec(t, db, `INSERT INTO registration_invites
		(id,recipient_email,recipient_role,creator_id,quota_policy_version_id,url_revision_id,smtp_revision_id,token_hash,state,expires_at)
		VALUES ($1,'c@example.com','client',$2,$3,$4,$5,$6,'pending_delivery',now()+interval '24 hours')`,
		id, uid, qv, urlRev, smtpRev, th)
	now := time.Now()
	exec(t, db, `UPDATE registration_invites
		SET state='active', sent_at=$1::timestamptz, expires_at=$1::timestamptz+interval '72 hours' WHERE id=$2`,
		now, id)
}

// TestPG_InviteConsumeLifecycle038 exercises the 038 fix end-to-end against
// the full lexical migration chain (now through 039): valid committed consume,
// outer-transaction rollback, repeat/race conflict, direct timestamp/state
// tampering rejection, and valid revocation/delivery paths. The DB-level
// ec_invite_lifecycle trigger (replaced by 038) is what makes the
// active -> consumed transition legal; the repo ConsumeInviteTx issues the
// same shape of conditional UPDATE.
func TestPG_InviteConsumeLifecycle038(t *testing.T) {
	db := openPG(t)
	defer db.Close()
	resetDB(t, db)
	uid := seedUsers(t, db)
	_, qv := seedQuotaPolicy(t, db)
	urlRev := "aaaaaaaa-0000-0000-0000-000000000060"
	smtpRev := "aaaaaaaa-0000-0000-0000-000000000061"
	seedRevisions(t, db, urlRev, smtpRev, 1, 1)
	ctx := context.Background()

	// Valid committed consume (active -> consumed with a real timestamp).
	inv := "aaaaaaaa-0000-0000-0000-000000000070"
	th := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	seedActiveInvite(t, db, inv, th, uid, qv, urlRev, smtpRev)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`UPDATE registration_invites SET state='consumed', consumed_at=now() WHERE id=$1 AND state='active' AND consumed_at IS NULL`, inv)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	var st string
	var consumed sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT state, consumed_at FROM registration_invites WHERE id=$1`, inv).Scan(&st, &consumed))
	require.Equal(t, "consumed", st)
	require.True(t, consumed.Valid, "valid consume sets consumed_at")

	// Once consumed, consumed_at is immutable: a second write must be rejected.
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`UPDATE registration_invites SET consumed_at=now()+interval '1 hour' WHERE id=$1`, inv)
	require.Error(t, tx.Commit(), "consumed_at immutable once set")

	// Repeat/race conflict: a second conditional consume matches 0 rows.
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	r, err := tx.ExecContext(ctx,
		`UPDATE registration_invites SET state='consumed', consumed_at=now() WHERE id=$1 AND state='active' AND consumed_at IS NULL`, inv)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	affected, _ := r.RowsAffected()
	require.Equal(t, int64(0), affected, "second consume is a no-op conflict")

	// Direct tampering: active -> consumed with NULL consumed_at must be rejected.
	inv2 := "aaaaaaaa-0000-0000-0000-000000000071"
	th2 := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	seedActiveInvite(t, db, inv2, th2, uid, qv, urlRev, smtpRev)
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`UPDATE registration_invites SET state='consumed' WHERE id=$1 AND state='active'`, inv2)
	require.Error(t, tx.Commit(), "active->consumed with NULL consumed_at rejected")

	// Direct tampering: pending/delivery_failed rows cannot be marked consumed.
	pend := "aaaaaaaa-0000-0000-0000-000000000072"
	thp := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	exec(t, db, `INSERT INTO registration_invites
		(id,recipient_email,recipient_role,creator_id,quota_policy_version_id,url_revision_id,smtp_revision_id,token_hash,state,expires_at)
		VALUES ($1,'p@example.com','client',$2,$3,$4,$5,$6,'pending_delivery',now()+interval '24 hours')`,
		pend, uid, qv, urlRev, smtpRev, thp)
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`UPDATE registration_invites SET state='consumed', consumed_at=now() WHERE id=$1`, pend)
	require.Error(t, tx.Commit(), "pending invite cannot be consumed")

	// Valid revocation path: pending -> revoked is allowed by 038.
	pendR := "aaaaaaaa-0000-0000-0000-000000000073"
	thr := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	exec(t, db, `INSERT INTO registration_invites
		(id,recipient_email,recipient_role,creator_id,quota_policy_version_id,url_revision_id,smtp_revision_id,token_hash,state,expires_at)
		VALUES ($1,'r@example.com','client',$2,$3,$4,$5,$6,'pending_delivery',now()+interval '24 hours')`,
		pendR, uid, qv, urlRev, smtpRev, thr)
	exec(t, db, `UPDATE registration_invites SET state='revoked' WHERE id=$1`, pendR)
	var stR string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT state FROM registration_invites WHERE id=$1`, pendR).Scan(&stR))
	require.Equal(t, "revoked", stR)

	// Valid delivery path: pending -> delivery_failed is allowed by 038.
	pendF := "aaaaaaaa-0000-0000-0000-000000000074"
	thf := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	exec(t, db, `INSERT INTO registration_invites
		(id,recipient_email,recipient_role,creator_id,quota_policy_version_id,url_revision_id,smtp_revision_id,token_hash,state,expires_at)
		VALUES ($1,'f@example.com','client',$2,$3,$4,$5,$6,'pending_delivery',now()+interval '24 hours')`,
		pendF, uid, qv, urlRev, smtpRev, thf)
	exec(t, db, `UPDATE registration_invites SET state='delivery_failed' WHERE id=$1`, pendF)
	var stF string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT state FROM registration_invites WHERE id=$1`, pendF).Scan(&stF))
	require.Equal(t, "delivery_failed", stF)
}

// TestPG_HarnessIncludes039QuotaMode proves the migration harness applies the
// complete lexical chain through at least 039 and that its users.quota_mode
// dependency (the column introduced by 037, consumed by 039's cross-table
// coherence trigger) is present and writable. This is a harness-scope guard, not
// a migration behavior change; it would have failed before the 037 skip was
// removed. No hardcoded migration count is asserted (the harness owns the full
// lexical chain), only the 039 artifacts that the test fixtures depend on.
func TestPG_HarnessIncludes039QuotaMode(t *testing.T) {
	db := openPG(t)
	defer db.Close()
	resetDB(t, db)
	ctx := context.Background()

	// 039 introduced these functions/triggers; their presence proves 039 ran.
	for _, obj := range []string{
		"validate_user_managed_quota_coherence",
		"trg_user_quota_managed_snapshot_integrity",
		"trg_users_managed_quota_coherence",
	} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT 1 FROM pg_proc WHERE proname=$1`, obj).Scan(&n),
			"039 artifact %s must exist after full-chain apply", obj)
	}

	// users.quota_mode (column added by 037) must be present and accept a managed
	// write, which is exactly the dependency 039's coherence trigger relies on.
	uid := "99999999-9999-9999-9999-999999999999"
	exec(t, db, `INSERT INTO users (id,email,password_hash,role,quota_mode)
		VALUES ($1,'harness039@example.com','h','client','legacy')`, uid)
	var mode string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT quota_mode FROM users WHERE id=$1`, uid).Scan(&mode))
	require.Equal(t, "legacy", mode, "users.quota_mode must be readable after full chain")
}
