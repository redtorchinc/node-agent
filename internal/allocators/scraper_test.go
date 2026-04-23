package allocators

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestScrapeOnce_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"allocated_mb":1864.8,"reserved_mb":1890.0,"max_allocated_mb":1874.7}`)
	}))
	defer srv.Close()

	store := NewStore()
	s := New(ServiceConfig{
		Name: "gliner2-service", URL: srv.URL,
		ThresholdWarnMB: 4096, ThresholdCritMB: 10240,
	}, store)
	s.now = func() time.Time { return time.Unix(1713820000, 0) }
	s.ScrapeOnce(context.Background())

	snap := store.Snapshot()
	if len(snap) != 1 || !snap[0].ScrapeOK {
		t.Fatalf("want 1 ok entry, got %+v", snap)
	}
	if snap[0].AllocatedMB != 1864.8 || snap[0].ReservedMB != 1890.0 {
		t.Errorf("wrong values: %+v", snap[0])
	}
	if got := snap[0].LastScrapeTS; got != 1713820000 {
		t.Errorf("LastScrapeTS = %d", got)
	}
}

func TestScrapeOnce_Failure(t *testing.T) {
	store := NewStore()
	s := New(ServiceConfig{Name: "x", URL: "http://127.0.0.1:1"}, store)
	s.ScrapeOnce(context.Background())
	snap := store.Snapshot()
	if len(snap) != 1 || snap[0].ScrapeOK {
		t.Fatalf("want failed entry, got %+v", snap)
	}
}

// TestGliner2Incident_2026_04_22 encodes the real incident that motivated
// this scraper: reserved_mb=16 GB while allocated_mb=2 GB. ratio=8.0, well
// above the 3.0 critical threshold. See SPEC.md §degraded_reasons.
func TestGliner2Incident_2026_04_22(t *testing.T) {
	s := Scraped{
		Name:        "gliner2-service",
		ScrapeOK:    true,
		AllocatedMB: 2048,
		ReservedMB:  16384,
	}
	if got := s.CreepRatio(); got < 7.9 || got > 8.1 {
		t.Errorf("CreepRatio = %.2f, want ~8.0", got)
	}
}
