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

// fakeVideo is one video served by globalPruneServer.
type fakeVideo struct {
	id      int
	channel string
	daysAgo int
	size    int64
}

// globalPruneServer serves login, users/me with several channels, per-channel
// video listings, per-video sizes, and video deletion.
type globalPruneServer struct {
	mu      sync.Mutex
	deleted []int
	srv     *httptest.Server
}

func newGlobalPruneServer(t *testing.T, videos []fakeVideo) *globalPruneServer {
	t.Helper()
	gs := &globalPruneServer{}
	byID := make(map[int]fakeVideo, len(videos))
	byChannel := make(map[string][]fakeVideo)
	for _, v := range videos {
		byID[v.id] = v
		byChannel[v.channel] = append(byChannel[v.channel], v)
	}
	handles := make([]string, 0, len(byChannel))
	for h := range byChannel {
		handles = append(handles, h)
	}
	sort.Strings(handles)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth-clients/local", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"client_id":"cid","client_secret":"csec"}`)
	})
	mux.HandleFunc("/api/v1/users/token", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"access_token":"atok"}`)
	})
	mux.HandleFunc("/api/v1/users/me", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"videoChannels":[`)
		for i, h := range handles {
			if i > 0 {
				io.WriteString(w, ",")
			}
			fmt.Fprintf(w, `{"id":%d,"name":%q,"displayName":%q}`, i+1, h, h)
		}
		io.WriteString(w, `]}`)
	})
	for _, h := range handles {
		list := byChannel[h]
		mux.HandleFunc("/api/v1/video-channels/"+h+"/videos", func(w http.ResponseWriter, _ *http.Request) {
			now := time.Now().UTC()
			fmt.Fprintf(w, `{"total":%d,"data":[`, len(list))
			for i, v := range list {
				if i > 0 {
					io.WriteString(w, ",")
				}
				published := now.AddDate(0, 0, -v.daysAgo).Format(time.RFC3339)
				fmt.Fprintf(w, `{"id":%d,"uuid":"uuid-%d","name":"video %d","publishedAt":%q}`,
					v.id, v.id, v.id, published)
			}
			io.WriteString(w, "]}")
		})
	}
	mux.HandleFunc("/api/v1/videos/", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/api/v1/videos/"))
		switch r.Method {
		case http.MethodGet:
			v, ok := byID[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Half the bytes as a plain file, half as HLS, to exercise both.
			fmt.Fprintf(w, `{"id":%d,"files":[{"size":%d}],"streamingPlaylists":[{"files":[{"size":%d}]}]}`,
				id, v.size/2, v.size-v.size/2)
		case http.MethodDelete:
			gs.mu.Lock()
			gs.deleted = append(gs.deleted, id)
			gs.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	gs.srv = httptest.NewServer(mux)
	t.Cleanup(gs.srv.Close)
	return gs
}

func (gs *globalPruneServer) deletedIDs() []int {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	out := append([]int(nil), gs.deleted...)
	sort.Ints(out)
	return out
}

// balancedFixture: channel a=12GB (ids 1-6), b=6GB (ids 7-9), c=2GB (id 10).
// Within a channel, lower ids are older.
func balancedFixture() []fakeVideo {
	const twoGB = int64(2) << 30
	vs := make([]fakeVideo, 0, 10)
	id := 0
	for i := range 6 {
		id++
		vs = append(vs, fakeVideo{id: id, channel: "a", daysAgo: 60 - i*5, size: twoGB})
	}
	for i := range 3 {
		id++
		vs = append(vs, fakeVideo{id: id, channel: "b", daysAgo: 60 - i*5, size: twoGB})
	}
	vs = append(vs, fakeVideo{id: 10, channel: "c", daysAgo: 60, size: twoGB})
	return vs
}

func TestGlobalPruneDryRun(t *testing.T) {
	gs := newGlobalPruneServer(t, balancedFixture())

	out, err := execViaCmd(t, "prune",
		"--url", gs.srv.URL, "--username", "a", "--password", "b",
		"--max-size", "10gb",
	)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if got := gs.deletedIDs(); len(got) != 0 {
		t.Fatalf("dry run deleted %v, want none", got)
	}
	for _, want := range []string{"20.0 GB", "10.0 GB", "Dry run"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The single-video channel c must never be listed.
	if strings.Contains(out, "uuid-10") {
		t.Errorf("smallest channel should not be pruned:\n%s", out)
	}
}

func TestGlobalPruneDeletesUntilUnderTarget(t *testing.T) {
	gs := newGlobalPruneServer(t, balancedFixture())

	out, err := execViaCmd(t, "prune",
		"--url", gs.srv.URL, "--username", "a", "--password", "b",
		"--max-size", "10gb", "--yes",
	)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	// 20GB -> 10GB needs five 2GB deletions, balanced: four from a, one from b.
	got := gs.deletedIDs()
	if want := []int{1, 2, 3, 4, 7}; len(got) != len(want) {
		t.Fatalf("deleted %v, want %v", got, want)
	}
	var fromA, fromB, fromC int
	for _, id := range got {
		switch {
		case id <= 6:
			fromA++
		case id <= 9:
			fromB++
		default:
			fromC++
		}
	}
	if fromA != 4 || fromB != 1 || fromC != 0 {
		t.Fatalf("deleted a=%d b=%d c=%d, want 4/1/0 (%v)", fromA, fromB, fromC, got)
	}
	if !strings.Contains(out, "Deleted 5/5 videos") {
		t.Errorf("output missing deletion summary:\n%s", out)
	}
}

func TestGlobalPruneAlreadyUnderTarget(t *testing.T) {
	gs := newGlobalPruneServer(t, balancedFixture())

	out, err := execViaCmd(t, "prune",
		"--url", gs.srv.URL, "--username", "a", "--password", "b",
		"--max-size", "100gb", "--yes",
	)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	if got := gs.deletedIDs(); len(got) != 0 {
		t.Fatalf("deleted %v, want none", got)
	}
	if !strings.Contains(out, "Already within the target") {
		t.Errorf("output missing no-op message:\n%s", out)
	}
}

func TestGlobalPruneKeepPerChannelProtects(t *testing.T) {
	gs := newGlobalPruneServer(t, balancedFixture())

	// Target 0 would delete everything; the newest 2 per channel survive.
	out, err := execViaCmd(t, "prune",
		"--url", gs.srv.URL, "--username", "a", "--password", "b",
		"--max-size", "0", "--keep-per-channel", "2", "--yes",
	)
	if err != nil {
		t.Fatalf("prune: %v\n%s", err, out)
	}
	// a keeps 5,6; b keeps 8,9; c keeps its only video 10.
	want := []int{1, 2, 3, 4, 7}
	if got := gs.deletedIDs(); len(got) != len(want) {
		t.Fatalf("deleted %v, want %v", got, want)
	}
	for _, kept := range []int{5, 6, 8, 9, 10} {
		for _, got := range gs.deletedIDs() {
			if got == kept {
				t.Errorf("video %d was protected but deleted", kept)
			}
		}
	}
	if !strings.Contains(out, "still") || !strings.Contains(out, "over target") {
		t.Errorf("output should warn about the protected shortfall:\n%s", out)
	}
}

// A deletion that fails must be reported and counted, and must not stop the
// remaining deletions.
func TestGlobalPrunePartialDeletionFailure(t *testing.T) {
	videos := balancedFixture()
	gs := newGlobalPruneServer(t, videos)
	// Reject deletion of one video that the balancing pass is known to select.
	gs.srv.Config.Handler.(*http.ServeMux).HandleFunc(
		"/api/v1/videos/1",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `{"id":1,"files":[{"size":%d}],"streamingPlaylists":[]}`, int64(2)<<30)
		})

	out, err := execViaCmd(t, "prune",
		"--url", gs.srv.URL, "--username", "a", "--password", "b",
		"--max-size", "10gb", "--yes",
	)
	if err == nil {
		t.Fatalf("expected an error when a deletion fails:\n%s", out)
	}
	if !strings.Contains(err.Error(), "1 of 5 deletions failed") {
		t.Errorf("error %v should count the failures", err)
	}
	// The other four must still have been attempted and succeeded.
	if got := gs.deletedIDs(); len(got) != 4 {
		t.Errorf("deleted %v, want the 4 videos that did not fail", got)
	}
	if !strings.Contains(out, "Deleted 4/5 videos") {
		t.Errorf("output missing the partial summary:\n%s", out)
	}
	// The failure itself must be named, not just counted.
	if !strings.Contains(out, "uuid-1") || !strings.Contains(out, "403") {
		t.Errorf("output should identify the failed video and reason:\n%s", out)
	}
}

func TestGlobalPruneRequiresMaxSize(t *testing.T) {
	gs := newGlobalPruneServer(t, balancedFixture())

	out, err := execViaCmd(t, "prune",
		"--url", gs.srv.URL, "--username", "a", "--password", "b",
	)
	if err == nil {
		t.Fatalf("expected an error without --max-size:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--max-size") {
		t.Errorf("error %v should name the missing flag", err)
	}
}

// A video whose size cannot be read would silently count as zero bytes, so the
// command must refuse rather than prune on incomplete data.
func TestGlobalPruneRefusesOnUnreadableSize(t *testing.T) {
	videos := balancedFixture()
	gs := newGlobalPruneServer(t, videos)
	// Serve a 500 for one video's size lookup.
	broken := videos[0].id
	gs.srv.Config.Handler.(*http.ServeMux).HandleFunc(
		"/api/v1/videos/"+strconv.Itoa(broken),
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})

	out, err := execViaCmd(t, "prune",
		"--url", gs.srv.URL, "--username", "a", "--password", "b",
		"--max-size", "10gb", "--yes",
	)
	if err == nil {
		t.Fatalf("expected an error when a size lookup fails:\n%s", out)
	}
	if got := gs.deletedIDs(); len(got) != 0 {
		t.Fatalf("nothing may be deleted on incomplete sizes, got %v", got)
	}
}

func TestParseSize(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1024", 1024},
		{"1b", 1},
		{"1k", 1 << 10},
		{"1kb", 1 << 10},
		{"512mb", 512 << 20},
		{"512mib", 512 << 20},
		{"100gb", 100 << 30},
		{"1.5gb", 3 << 29},
		{"2tb", 2 << 40},
		{" 10GB ", 10 << 30},
	} {
		got, err := parseSize(tt.in)
		if err != nil {
			t.Errorf("parseSize(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
	for _, bad := range []string{"", "abc", "-5gb", "gb", "1.2.3gb"} {
		if got, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) = %d, want an error", bad, got)
		}
	}
}

func TestFormatSize(t *testing.T) {
	for _, tt := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1 << 10, "1.0 KB"},
		{1536, "1.5 KB"},
		{1 << 20, "1.0 MB"},
		{1 << 30, "1.0 GB"},
		{20 << 30, "20.0 GB"},
		{1 << 40, "1.0 TB"},
		{2048 << 40, "2048.0 TB"},
	} {
		if got := formatSize(tt.in); got != tt.want {
			t.Errorf("formatSize(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
