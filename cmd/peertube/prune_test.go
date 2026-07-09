package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// pruneServer serves login, a channel video listing, and video deletion,
// recording which video ids were deleted.
type pruneServer struct {
	mu      sync.Mutex
	deleted []int
	srv     *httptest.Server
}

func newPruneServer(t *testing.T, videoCount int) *pruneServer {
	t.Helper()
	ps := &pruneServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth-clients/local", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"client_id":"cid","client_secret":"csec"}`)
	})
	mux.HandleFunc("/api/v1/users/token", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"access_token":"atok"}`)
	})
	// Newest first: id 1 is newest (1 day old), id N is oldest (N days old).
	mux.HandleFunc("/api/v1/video-channels/kellepourier/videos", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC()
		fmt.Fprintf(w, `{"total":%d,"data":[`, videoCount)
		for i := 1; i <= videoCount; i++ {
			if i > 1 {
				io.WriteString(w, ",")
			}
			published := now.AddDate(0, 0, -i).Format(time.RFC3339)
			fmt.Fprintf(w, `{"id":%d,"uuid":"uuid-%d","name":"video %d","publishedAt":"%s"}`, i, i, i, published)
		}
		io.WriteString(w, "]}")
	})
	mux.HandleFunc("/api/v1/videos/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method %s", r.Method)
		}
		idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/videos/")
		id, _ := strconv.Atoi(idStr)
		ps.mu.Lock()
		ps.deleted = append(ps.deleted, id)
		ps.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	ps.srv = httptest.NewServer(mux)
	t.Cleanup(ps.srv.Close)
	return ps
}

func (ps *pruneServer) deletedIDs() []int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := append([]int(nil), ps.deleted...)
	sort.Ints(out)
	return out
}

func TestChannelPruneDryRun(t *testing.T) {
	ps := newPruneServer(t, 5)

	out, err := execViaCmd(t, "channel", "prune",
		"--url", ps.srv.URL, "--username", "a", "--password", "b",
		"--channel", "kellepourier", "--keep-last", "2",
	)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	// Dry run must not delete anything.
	if got := ps.deletedIDs(); len(got) != 0 {
		t.Fatalf("dry run deleted %v, want none", got)
	}
	// Should list the 3 oldest (ids 3,4,5) and mention dry run.
	for _, want := range []string{"uuid-3", "uuid-4", "uuid-5", "Dry run"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "uuid-1") || strings.Contains(out, "uuid-2") {
		t.Errorf("kept videos should not be listed:\n%s", out)
	}
}

func TestChannelPruneKeepLastYes(t *testing.T) {
	ps := newPruneServer(t, 5)

	out, err := execViaCmd(t, "channel", "prune",
		"--url", ps.srv.URL, "--username", "a", "--password", "b",
		"--channel", "kellepourier", "--keep-last", "2", "--yes",
	)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if got := ps.deletedIDs(); !equalInts(got, []int{3, 4, 5}) {
		t.Fatalf("deleted %v, want [3 4 5]", got)
	}
	if !strings.Contains(out, "Deleted 3/3") {
		t.Errorf("expected deletion summary:\n%s", out)
	}
}

func TestChannelPruneOlderThanYes(t *testing.T) {
	ps := newPruneServer(t, 5)
	// Videos are i days old; older-than 3.5d prunes ids 4 and 5 (4d, 5d), while
	// id 3 (3d) stays under the threshold regardless of clock skew.
	out, err := execViaCmd(t, "channel", "prune",
		"--url", ps.srv.URL, "--username", "a", "--password", "b",
		"--channel", "kellepourier", "--older-than", "3.5d", "--yes",
	)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if got := ps.deletedIDs(); !equalInts(got, []int{4, 5}) {
		t.Fatalf("deleted %v, want [4 5]", got)
	}
}

func TestChannelPruneRequiresCriterion(t *testing.T) {
	ps := newPruneServer(t, 3)
	_, err := execViaCmd(t, "channel", "prune",
		"--url", ps.srv.URL, "--username", "a", "--password", "b",
		"--channel", "kellepourier",
	)
	if err == nil || !strings.Contains(err.Error(), "at least one of") {
		t.Fatalf("expected criterion error, got %v", err)
	}
}

func TestParseAge(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"30d", 30 * day, true},
		{"2w", 14 * day, true},
		{"6mo", 180 * day, true},
		{"1y", 365 * day, true},
		{"48h", 48 * time.Hour, true},
		{"12H", 12 * time.Hour, true},
		{"1.5d", 36 * time.Hour, true},
		{"", 0, false},
		{"abc", 0, false},
		{"-5d", 0, false},
	}
	for _, tt := range tests {
		got, err := parseAge(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("parseAge(%q) err=%v, ok=%v", tt.in, err, tt.ok)
			continue
		}
		if tt.ok && got != tt.want {
			t.Errorf("parseAge(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
