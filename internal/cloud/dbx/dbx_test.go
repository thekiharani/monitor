package dbx

import (
	"context"
	"testing"
	"time"
)

func TestCleanupSurvivesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, release := Cleanup(parent)
	defer release()

	cancel()

	select {
	case <-ctx.Done():
		t.Fatal("cleanup context was cancelled with its parent")
	default:
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("cleanup context error=%v, want nil", err)
	}
}

func TestCleanupCarriesItsOwnDeadline(t *testing.T) {
	ctx, release := Cleanup(context.Background())
	defer release()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("cleanup context has no deadline, so a hung undo would block forever")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > CleanupTimeout+time.Second {
		t.Fatalf("deadline is %v away, want about %v", remaining, CleanupTimeout)
	}
}

func TestCleanupPreservesParentValues(t *testing.T) {
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "carried")
	ctx, release := Cleanup(parent)
	defer release()

	if ctx.Value(key{}) != "carried" {
		t.Fatal("cleanup context dropped a parent value")
	}
}
