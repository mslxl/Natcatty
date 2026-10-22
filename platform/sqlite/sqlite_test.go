package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestConsistentExcludesWrites proves a logical snapshot taken from several
// statements cannot interleave with a mutation: while the callback holds the
// gate, an Execute started inside it must not complete.
func TestConsistentExcludesWrites(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "consistency.db"), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Execute(ctx, `CREATE TABLE items (id INTEGER PRIMARY KEY, label TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Execute(ctx, `INSERT INTO items (id, label) VALUES (1, 'first')`); err != nil {
		t.Fatal(err)
	}

	writeStarted := make(chan struct{})
	writeDone := make(chan error, 1)
	err = db.Consistent(ctx, func(ctx context.Context, query QueryFunc) error {
		rows, err := query(ctx, `SELECT COUNT(*) FROM items`)
		if err != nil {
			return err
		}
		if got := rows.Values[0][0]; got != int64(1) {
			t.Fatalf("count = %v, want 1", got)
		}
		go func() {
			close(writeStarted)
			_, err := db.Execute(context.Background(),
				`INSERT INTO items (id, label) VALUES (2, 'second')`)
			writeDone <- err
		}()
		<-writeStarted
		// The write goroutine cannot finish: Execute needs the shared gate,
		// which this callback holds exclusively. A completed write here would
		// mean the snapshot could observe a half-applied mutation.
		select {
		case err := <-writeDone:
			t.Fatalf("the write interleaved with the snapshot: %v", err)
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued write never ran after the snapshot released the gate")
	}

	rows, err := db.Query(ctx, `SELECT COUNT(*) FROM items`)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows.Values[0][0]; got != int64(2) {
		t.Fatalf("final count = %v, want 2", got)
	}
}
