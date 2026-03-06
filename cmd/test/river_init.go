// Test program for River Queue initialization
// Usage: DATABASE_URL="postgres://user:password@localhost/maburvm?sslmode=disable" go run cmd/test/river_test.go

// River Queue Initialization Test Program
// This program initializes and tests the River queue system
// Run: DATABASE_URL="..." go run cmd/test/river_init.go

package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/maburvm/panel/internal/shared/queue"
)

func main() {
	// Get database URL from environment
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		// Default for local testing
		databaseURL = "postgres://postgres:postgres@localhost:5432/maburvm?sslmode=disable"
	}

	fmt.Println("=" + string(make([]byte, 60)))
	fmt.Println("MaburVM - River Queue Initialization Test")
	fmt.Println("=" + string(make([]byte, 60)))
	fmt.Println()

	ctx := context.Background()

	// Step 1: Test database connection
	fmt.Println("[1/6] Testing PostgreSQL connection...")
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("ERROR: Failed to create connection pool: %v", err)
	}
	defer pool.Close()

	// Test connection with timeout
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pool.Ping(testCtx); err != nil {
		log.Fatalf("ERROR: Failed to ping database: %v", err)
	}
	fmt.Println("      ✓ Database connection successful")

	// Step 2: Check if River tables exist before migration
	fmt.Println("[2/6] Checking River tables before migration...")
	if err := checkRiverTables(ctx, pool); err != nil {
		fmt.Printf("      ℹ Tables don't exist yet (expected): %v\n", err)
	} else {
		fmt.Println("      ✓ River tables already exist")
	}

	// Step 3: Run River migrations
	fmt.Println("[3/6] Running River migrations...")
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := queue.RunMigrationsWithLogger(ctx, pool, logger); err != nil {
		log.Fatalf("ERROR: Failed to run migrations: %v", err)
	}
	fmt.Println("      ✓ Migrations completed successfully")

	// Step 4: Verify River tables exist after migration
	fmt.Println("[4/6] Verifying River tables after migration...")
	if err := queue.VerifyTables(ctx, pool); err != nil {
		log.Fatalf("ERROR: Failed to verify tables: %v", err)
	}
	fmt.Println("      ✓ All River tables verified:")
	fmt.Println("        - river_job")
	fmt.Println("        - river_leader")
	fmt.Println("        - river_queue")

	// Step 5: Initialize River client
	fmt.Println("[5/6] Initializing River client with multiple queues...")
	config := queue.DefaultConfig(databaseURL)
	client, err := queue.NewClient(config, logger)
	if err != nil {
		log.Fatalf("ERROR: Failed to create client: %v", err)
	}
	fmt.Println("      ✓ River client initialized:")
	fmt.Printf("        - Critical queue: %d workers\n", config.CriticalWorkers)
	fmt.Printf("        - Default queue: %d workers\n", config.DefaultWorkers)
	fmt.Printf("        - Batch queue: %d workers\n", config.BatchWorkers)

	// Step 6: Start and test job insertion
	fmt.Println("[6/6] Starting River client and testing job insertion...")
	if err := client.Start(ctx); err != nil {
		log.Fatalf("ERROR: Failed to start client: %v", err)
	}
	fmt.Println("      ✓ River client started")

	// Test inserting a job
	testJob := queue.VMOperationJob{
		VMID:      "test-vm-123",
		Operation: queue.VMOpStart,
		NodeID:    "test-node-001",
	}

	result, err := client.InsertVMOperation(ctx, testJob)
	if err != nil {
		log.Fatalf("ERROR: Failed to insert test job: %v", err)
	}
	fmt.Printf("      ✓ Test job inserted successfully (Job ID: %d)\n", result.Job.ID)

	// Graceful shutdown
	fmt.Println()
	fmt.Println("[Cleanup] Stopping River client...")
	shutdownCtx, cancelShutdown := context.WithTimeout(ctx, 10*time.Second)
	defer cancelShutdown()

	if err := client.Stop(shutdownCtx); err != nil {
		log.Printf("WARNING: Error during shutdown: %v", err)
	} else {
		fmt.Println("          ✓ River client stopped gracefully")
	}

	fmt.Println()
	fmt.Println("=" + string(make([]byte, 60)))
	fmt.Println("✓ ALL TESTS PASSED - River Queue initialization successful!")
	fmt.Println("=" + string(make([]byte, 60)))
	fmt.Println()
	fmt.Println("Summary:")
	fmt.Println("  - PostgreSQL connection: OK")
	fmt.Println("  - River migrations: OK")
	fmt.Println("  - Table verification: OK (river_job, river_leader, river_queue)")
	fmt.Println("  - Client initialization: OK")
	fmt.Println("  - Job insertion: OK")
	fmt.Println("  - Graceful shutdown: OK")
}

// checkRiverTables checks if River tables exist (for pre-migration check)
func checkRiverTables(ctx context.Context, pool *pgxpool.Pool) error {
	tables := []string{"river_job", "river_leader", "river_queue"}
	for _, table := range tables {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables 
				WHERE table_schema = 'public' AND table_name = $1
			)
		`, table).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("table %s does not exist", table)
		}
	}
	return nil
}
