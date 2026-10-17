package ledger

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A second Open must wait out a writer that is mid-transaction rather than
// fail with SQLITE_BUSY, since concurrent `loom ingest` hooks do exactly this.
func TestOpenWaitsForABriefWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()

	ctx := context.Background()
	conn, err := first.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(ctx, "COMMIT")
			_ = conn.Close()
		})
	}
	// Deferred after first.Close, so it runs before it: a failing Open must not
	// leave the lock held, or Close blocks on the busy connection and the test
	// hangs instead of failing.
	defer release()
	go func() {
		time.Sleep(300 * time.Millisecond)
		release()
	}()

	start := time.Now()
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open failed instead of waiting: %v", err)
	}
	_ = second.Close()
	if time.Since(start) < 200*time.Millisecond {
		t.Error("second Open returned before the lock was released: the test did not hold the lock")
	}
}
