package store_test

import (
	"context"
	"testing"
)

func TestIncrementSearchCount_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateSession(ctx, "sess-sc-basic", "proj", "/tmp")

	for i := 0; i < 3; i++ {
		if err := s.IncrementSearchCount(ctx, "sess-sc-basic"); err != nil {
			t.Fatalf("IncrementSearchCount iteration %d: %v", i, err)
		}
	}

	sess, err := s.GetSession(ctx, "sess-sc-basic")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess == nil {
		t.Fatal("GetSession returned nil")
	}
	if sess.SearchCount != 3 {
		t.Errorf("SearchCount = %d, want 3", sess.SearchCount)
	}
}

func TestIncrementSearchCount_UnknownSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Should not return an error — fail-open contract.
	if err := s.IncrementSearchCount(ctx, "nonexistent-session-id"); err != nil {
		t.Errorf("IncrementSearchCount on unknown session should not error, got: %v", err)
	}
}

func TestGetSession_SearchCount_Default(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.CreateSession(ctx, "sess-sc-default", "proj", "/tmp")

	sess, err := s.GetSession(ctx, "sess-sc-default")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess == nil {
		t.Fatal("GetSession returned nil")
	}
	if sess.SearchCount != 0 {
		t.Errorf("SearchCount = %d, want 0 (default)", sess.SearchCount)
	}
}
