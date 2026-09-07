package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func quotaServer(t *testing.T, remaining string, hits *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Header().Set("x-requests-remaining", remaining)
		w.Header().Set("x-requests-used", "10")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
}

func TestQuotaGuardSkipsCallsBelowFloor(t *testing.T) {
	hits := 0
	server := quotaServer(t, "12", &hits)
	defer server.Close()

	client := newTestClient(server.URL)
	client.SetQuotaFloor(25)

	// First call goes through and learns remaining=12
	if _, _, err := client.GetOdds("baseball_mlb"); err != nil {
		t.Fatalf("first GetOdds: %v", err)
	}
	// Second call is refused locally: no HTTP hit
	_, _, err := client.GetOdds("baseball_mlb")
	if !errors.Is(err, ErrQuotaGuard) {
		t.Fatalf("err = %v, want ErrQuotaGuard", err)
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1 (guard must not call the API)", hits)
	}
}

func TestQuotaGuardAllowsCallsAboveFloor(t *testing.T) {
	hits := 0
	server := quotaServer(t, "400", &hits)
	defer server.Close()

	client := newTestClient(server.URL)
	client.SetQuotaFloor(25)
	for i := 0; i < 2; i++ {
		if _, _, err := client.GetOdds("baseball_mlb"); err != nil {
			t.Fatalf("GetOdds %d: %v", i, err)
		}
	}
	if hits != 2 {
		t.Errorf("server hits = %d, want 2", hits)
	}
}

func TestQuotaGuardSeededFromStoreAndProbesWhenStale(t *testing.T) {
	hits := 0
	server := quotaServer(t, "400", &hits)
	defer server.Close()

	client := newTestClient(server.URL)
	client.SetQuotaFloor(25)

	// A fresh persisted snapshot below the floor blocks before any call
	client.SeedQuota(5, time.Now())
	if _, _, err := client.GetOdds("baseball_mlb"); !errors.Is(err, ErrQuotaGuard) {
		t.Fatalf("seeded low quota should block, got %v", err)
	}
	// A stale snapshot (monthly reset may have happened) lets one probe through
	client.SeedQuota(5, time.Now().Add(-25*time.Hour))
	if _, _, err := client.GetOdds("baseball_mlb"); err != nil {
		t.Fatalf("stale snapshot should allow a probe: %v", err)
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want 1", hits)
	}
	// The probe learned 400 remaining, so calls flow again
	if _, _, err := client.GetOdds("baseball_mlb"); err != nil {
		t.Fatalf("after probe: %v", err)
	}
}

func TestQuotaGuardDisabledByZeroFloor(t *testing.T) {
	hits := 0
	server := quotaServer(t, "1", &hits)
	defer server.Close()

	client := newTestClient(server.URL) // no floor set
	for i := 0; i < 2; i++ {
		if _, _, err := client.GetOdds("baseball_mlb"); err != nil {
			t.Fatalf("GetOdds %d: %v", i, err)
		}
	}
	if hits != 2 {
		t.Errorf("server hits = %d, want 2 with the guard off", hits)
	}
}
