package store

import (
	"context"
	"testing"
)

func TestDualStore_GetByTopicKey_ReadsFromPrimary(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	saved, err := ds.primary.SaveObservation(ctx, SaveParams{
		Type: TypeSession, Title: "digest", Content: "resumen", Project: "p", TopicKey: "session/s1",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := ds.GetByTopicKey(ctx, "p", "session/s1")
	if err != nil {
		t.Fatalf("GetByTopicKey: %v", err)
	}
	if got == nil || got.ID != saved.ID {
		t.Errorf("GetByTopicKey = %+v, want ID=%d (de primary)", got, saved.ID)
	}
}

func TestDualStore_GetByTopicKey_FallsBackToBuffer(t *testing.T) {
	ds := newTestDualStore(t)
	ctx := context.Background()

	saved, err := ds.buffer.SaveObservation(ctx, SaveParams{
		Type: TypeSession, Title: "digest", Content: "resumen solo en buffer", Project: "p", TopicKey: "session/s1",
	})
	if err != nil {
		t.Fatal(err)
	}

	ds.primary.Close()
	ds.down = false

	got, err := ds.GetByTopicKey(ctx, "p", "session/s1")
	if err != nil {
		t.Fatalf("GetByTopicKey: %v", err)
	}
	if got == nil || got.ID != saved.ID {
		t.Errorf("GetByTopicKey no cayó al buffer — got %+v", got)
	}
	if !ds.isPrimaryDown() {
		t.Error("primary debería marcarse down tras el error real")
	}
}
