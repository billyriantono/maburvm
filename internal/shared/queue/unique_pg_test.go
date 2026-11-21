//go:build pg

package queue

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// A repeated VM request is folded into the one still queued, but a request
// with new parameters (another password, another template) is not.
func TestVMOperationUniquenessOnPostgres(t *testing.T) {
	base := os.Getenv("MABURVM_TEST_PG_DSN")
	if base == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	name := fmt.Sprintf("maburvm_unique_%d", time.Now().UnixNano()%1_000_000_000)
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsnForDatabase(t, base, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		a, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`)
	})
	if err := RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	rc, err := river.NewClient[pgx.Tx](riverpgxv5.New(pool), &river.Config{}) // insert-only
	if err != nil {
		t.Fatal(err)
	}
	const vm = "55555555-5555-5555-5555-555555555555"
	insert := func(op VMOperationType, params string) (int64, bool) {
		t.Helper()
		job := VMOperationJob{VMID: vm, Operation: op, NodeID: "n1"}
		if params != "" {
			job.Params = []byte(params)
		}
		res, err := rc.Insert(ctx, job, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Job.ID, res.UniqueSkippedAsDuplicate
	}

	first, _ := insert(VMOpResetPassword, `{"root_password":"one"}`)
	if again, dup := insert(VMOpResetPassword, `{"root_password":"one"}`); !dup || again != first {
		t.Fatal("the same request twice must return the job already queued")
	}
	if _, dup := insert(VMOpResetPassword, `{"root_password":"two"}`); dup {
		t.Fatal("a reset to another password was dropped as a duplicate of the first")
	}
	start, _ := insert(VMOpStart, "")
	if again, dup := insert(VMOpStart, ""); !dup || again != start {
		t.Fatal("a second start while one is queued must not queue another")
	}
}
