package state

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	// File-backed so we test migration + real persistence paths, not
	// only :memory: shortcuts.
	path := filepath.Join(t.TempDir(), "kenny.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestBeginLifeIncrementsMonotonically(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for i := int64(1); i <= 3; i++ {
		got, err := s.BeginLife(ctx)
		if err != nil {
			t.Fatalf("BeginLife: %v", err)
		}
		if got != i {
			t.Fatalf("BeginLife #%d = %d, want %d", i, got, i)
		}
	}
}

func TestMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, ok, err := s.GetMetadata(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
	if err := s.SetMetadata(ctx, "foo", "bar"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	v, ok, err := s.GetMetadata(ctx, "foo")
	if err != nil || !ok || v != "bar" {
		t.Fatalf("GetMetadata after set: v=%q ok=%v err=%v", v, ok, err)
	}
	// Overwrite.
	if err := s.SetMetadata(ctx, "foo", "baz"); err != nil {
		t.Fatalf("SetMetadata overwrite: %v", err)
	}
	v, _, _ = s.GetMetadata(ctx, "foo")
	if v != "baz" {
		t.Fatalf("overwrite didn't stick: %q", v)
	}
}

func TestJournalAppendAndRecent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for range 5 {
		if err := s.AppendJournal(ctx, 1, "boot", "entry"); err != nil {
			t.Fatalf("AppendJournal: %v", err)
		}
	}
	entries, err := s.RecentJournal(ctx, 3)
	if err != nil {
		t.Fatalf("RecentJournal: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	// DESC order.
	if entries[0].ID < entries[1].ID {
		t.Fatalf("expected DESC by id, got %+v", entries)
	}

	n, err := s.CountJournalEntries(ctx)
	if err != nil || n != 5 {
		t.Fatalf("CountJournalEntries = %d err=%v, want 5", n, err)
	}
}

func TestInflightLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.MarkInflight(ctx, 1, "self_mod", `{"goal":"something"}`)
	if err != nil {
		t.Fatalf("MarkInflight: %v", err)
	}
	n, err := s.CountInflight(ctx)
	if err != nil || n != 1 {
		t.Fatalf("CountInflight after mark = %d err=%v, want 1", n, err)
	}

	list, err := s.ListInflight(ctx)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListInflight = %+v err=%v", list, err)
	}

	if err := s.ClearInflight(ctx, id); err != nil {
		t.Fatalf("ClearInflight: %v", err)
	}
	n, _ = s.CountInflight(ctx)
	if n != 0 {
		t.Fatalf("CountInflight after clear = %d, want 0", n)
	}
}

func TestSessionUpsert(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, ok, _ := s.GetSession(ctx, "main"); ok {
		t.Fatalf("unexpected session present")
	}
	if err := s.PutSession(ctx, "main", "sess-1"); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	id, ok, err := s.GetSession(ctx, "main")
	if err != nil || !ok || id != "sess-1" {
		t.Fatalf("after put: id=%q ok=%v err=%v", id, ok, err)
	}
	if err := s.PutSession(ctx, "main", "sess-2"); err != nil {
		t.Fatalf("PutSession update: %v", err)
	}
	id, _, _ = s.GetSession(ctx, "main")
	if id != "sess-2" {
		t.Fatalf("upsert didn't update: %q", id)
	}
}

func TestSecretsCRUDAndListing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.PutSecret(ctx, "alpha", "A"); err != nil {
		t.Fatalf("PutSecret alpha: %v", err)
	}
	if err := s.PutSecret(ctx, "beta", "B"); err != nil {
		t.Fatalf("PutSecret beta: %v", err)
	}

	v, ok, err := s.GetSecret(ctx, "alpha")
	if err != nil || !ok || v != "A" {
		t.Fatalf("GetSecret alpha: v=%q ok=%v err=%v", v, ok, err)
	}

	keys, err := s.ListSecretKeys(ctx)
	if err != nil || len(keys) != 2 || keys[0] != "alpha" || keys[1] != "beta" {
		t.Fatalf("ListSecretKeys = %v err=%v", keys, err)
	}

	if err := s.DeleteSecret(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, ok, _ := s.GetSecret(ctx, "alpha"); ok {
		t.Fatalf("secret still present after delete")
	}
}

func TestPing(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestMessagesLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	pending, err := s.PendingMessages(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("PendingMessages empty: got %d err=%v", len(pending), err)
	}

	if err := s.AddMessage(ctx, "hello from user"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if err := s.AddMessage(ctx, "second message"); err != nil {
		t.Fatalf("AddMessage 2: %v", err)
	}

	pending, err = s.PendingMessages(ctx)
	if err != nil || len(pending) != 2 {
		t.Fatalf("PendingMessages got %d err=%v, want 2", len(pending), err)
	}
	if pending[0].Content != "hello from user" || pending[1].Content != "second message" {
		t.Fatalf("unexpected content: %+v", pending)
	}

	if err := s.ConsumeMessages(ctx); err != nil {
		t.Fatalf("ConsumeMessages: %v", err)
	}

	pending, err = s.PendingMessages(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("PendingMessages after consume: got %d err=%v, want 0", len(pending), err)
	}
}
