package skills

import (
	"errors"
	"testing"
	"time"
)

func TestHostScanner_GetReturnsNilWhenEmpty(t *testing.T) {
	t.Parallel()
	s := NewHostScanner(0)
	if s.Get("anywhere") != nil {
		t.Errorf("expected nil before Refresh")
	}
}

func TestHostScanner_RefreshStoresAndReturnsFresh(t *testing.T) {
	t.Parallel()
	s := NewHostScanner(time.Minute)
	inv := &HostInventory{Host: "h1", Skills: []Skill{{Name: "alpha"}}}
	err := s.Refresh("h1", func(host string) (*HostInventory, error) {
		if host != "h1" {
			t.Errorf("unexpected host %q", host)
		}
		return inv, nil
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got := s.Get("h1")
	if got == nil || got.Host != "h1" || got.Stale {
		t.Errorf("expected fresh entry for h1, got %+v", got)
	}
}

func TestHostScanner_RefreshErrorPreservesCacheAsStale(t *testing.T) {
	t.Parallel()
	s := NewHostScanner(time.Minute)
	inv := &HostInventory{Host: "h1"}
	if err := s.Refresh("h1", func(_ string) (*HostInventory, error) { return inv, nil }); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if err := s.Refresh("h1", func(_ string) (*HostInventory, error) {
		return nil, errors.New("ssh down")
	}); err == nil {
		t.Fatal("expected error from failing Refresh")
	}
	got := s.Get("h1")
	if got == nil {
		t.Fatal("expected cached entry to survive failed Refresh")
	}
	if !got.Stale {
		t.Errorf("expected Stale=true after failed Refresh, got %+v", got)
	}
}

func TestHostScanner_TTLExpiry(t *testing.T) {
	t.Parallel()
	s := NewHostScanner(5 * time.Millisecond)
	if err := s.Refresh("h1", func(_ string) (*HostInventory, error) { return &HostInventory{Host: "h1"}, nil }); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	got := s.Get("h1")
	if got == nil {
		t.Fatal("expected entry to remain in cache (Stale=true)")
	}
	if !got.Stale {
		t.Errorf("expected Stale=true after TTL expiry")
	}
}

func TestHostScanner_SnapshotReturnsCopy(t *testing.T) {
	t.Parallel()
	s := NewHostScanner(0)
	if err := s.Refresh("h1", func(_ string) (*HostInventory, error) { return &HostInventory{Host: "h1"}, nil }); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 entry in snapshot, got %d", len(snap))
	}
	// Mutate the snapshot — should NOT affect the cache.
	delete(snap, "h1")
	if s.Get("h1") == nil {
		t.Errorf("expected internal cache to be unaffected by snapshot mutation")
	}
}
