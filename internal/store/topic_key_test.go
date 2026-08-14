package store_test

import (
	"context"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func TestGetByTopicKey_Exists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "v1", Content: "contenido", Project: "p", TopicKey: "arch/db",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetByTopicKey(ctx, "p", "arch/db")
	if err != nil {
		t.Fatalf("GetByTopicKey: %v", err)
	}
	if got == nil || got.ID != saved.ID {
		t.Errorf("GetByTopicKey = %+v, want ID=%d", got, saved.ID)
	}
}

func TestGetByTopicKey_NotFound_ReturnsNilNoError(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetByTopicKey(context.Background(), "p", "no/existe")
	if err != nil {
		t.Fatalf("GetByTopicKey: %v", err)
	}
	if got != nil {
		t.Errorf("esperaba nil, got %+v", got)
	}
}

func TestGetByTopicKey_ScopedByProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.SaveObservation(ctx, store.SaveParams{
		Type: store.TypeDecision, Title: "v1", Content: "c", Project: "proyecto-a", TopicKey: "arch/db",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetByTopicKey(ctx, "proyecto-b", "arch/db")
	if err != nil {
		t.Fatalf("GetByTopicKey: %v", err)
	}
	if got != nil {
		t.Error("un topic_key de otro proyecto no debería matchear")
	}
}
