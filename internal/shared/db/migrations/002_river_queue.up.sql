-- River Queue Migration
-- This migration creates the required River queue tables
-- Run via: go run ./cmd/test/main.go or programmatically via queue.RunMigrations()

-- Note: River tables are created automatically when running queue.RunMigrations()
-- The following is documentation of the tables that will be created:

-- river_job: Stores all jobs in the queue
--   - id: bigint primary key
--   - args: jsonb - job arguments
--   - attempt: smallint - number of attempts made
--   - attempted_at: timestamptz - last attempt time
--   - attempted_by: text[] - worker IDs that attempted
--   - created_at: timestamptz - creation time
--   - errors: jsonb[] - error history
--   - finalized_at: timestamptz - when job completed/failed
--   - kind: text - job type (e.g., "vm_operation")
--   - max_attempts: smallint - max retry attempts
--   - metadata: jsonb - additional metadata
--   - priority: smallint - job priority (1 = highest)
--   - queue: text - queue name (critical, default, batch)
--   - scheduled_at: timestamptz - when to run
--   - state: river_job_state - job state (available, cancelled, completed, etc.)
--   - tags: varchar(255)[] - job tags
--   - unique_key: bytea - for unique jobs

-- river_leader: Leader election for River clients
--   - elected_at: timestamptz - when elected
--   - expires_at: timestamptz - when leadership expires
--   - id: bigint - leader ID
--   - inserted_at: timestamptz - when record created
--   - leader_id: text - client ID
--   - name: text - leader name

-- river_queue: Queue configuration and status
--   - name: text primary key - queue name
--   - created_at: timestamptz - creation time
--   - metadata: jsonb - queue metadata
--   - paused_at: timestamptz - when paused
--   - updated_at: timestamptz - last update

-- Note: Run migrations programmatically using:
--   import "github.com/maburvm/panel/internal/shared/queue"
--   err := queue.RunMigrations(ctx, pool)
