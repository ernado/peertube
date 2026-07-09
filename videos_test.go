package peertube

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func mkVideo(id int, daysAgo int, now time.Time) Video {
	return Video{
		ID:          id,
		UUID:        fmt.Sprintf("uuid-%d", id),
		Name:        fmt.Sprintf("video %d", id),
		PublishedAt: now.AddDate(0, 0, -daysAgo),
	}
}

func TestSelectPrunable(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	// ids by age: 1=1d, 2=10d, 3=40d, 4=100d (newest first: 1,2,3,4)
	vids := []Video{
		mkVideo(3, 40, now),
		mkVideo(1, 1, now),
		mkVideo(4, 100, now),
		mkVideo(2, 10, now),
	}
	ids := func(vs []Video) []int {
		out := make([]int, len(vs))
		for i, v := range vs {
			out[i] = v.ID
		}
		return out
	}
	eq := func(a, b []int) bool {
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

	tests := []struct {
		name string
		opts PruneOptions
		want []int // pruned ids, newest-first order
	}{
		{"older than 30d", PruneOptions{OlderThan: 30 * 24 * time.Hour}, []int{3, 4}},
		{"older than 5d", PruneOptions{OlderThan: 5 * 24 * time.Hour}, []int{2, 3, 4}},
		{"keep last 2", PruneOptions{KeepLast: 2}, []int{3, 4}},
		{"keep last 10 (more than exist)", PruneOptions{KeepLast: 10}, nil},
		{"older than 5d but keep last 3", PruneOptions{OlderThan: 5 * 24 * time.Hour, KeepLast: 3}, []int{4}},
		{"both zero prunes all", PruneOptions{}, []int{1, 2, 3, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ids(SelectPrunable(vids, tt.opts, now))
			if !eq(got, tt.want) {
				t.Fatalf("SelectPrunable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChannelVideosPagination(t *testing.T) {
	// 150 videos across two pages (100 + 50).
	total := 150
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/video-channels/ch/videos" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing auth header")
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"total":%d,"data":[`, total)
		for i := 0; i < count && start+i < total; i++ {
			if i > 0 {
				io.WriteString(w, ",")
			}
			fmt.Fprintf(w, `{"id":%d,"uuid":"u%d","name":"v%d","publishedAt":"2026-01-01T00:00:00Z"}`, start+i, start+i, start+i)
		}
		io.WriteString(w, "]}")
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL, WithToken("tok"))
	vids, err := c.ChannelVideos(context.Background(), "ch")
	if err != nil {
		t.Fatal(err)
	}
	if len(vids) != total {
		t.Fatalf("got %d videos, want %d", len(vids), total)
	}
	if vids[0].ID != 0 || vids[total-1].ID != total-1 {
		t.Fatalf("unexpected first/last: %d/%d", vids[0].ID, vids[total-1].ID)
	}
}

func TestDeleteVideo(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing auth")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL, WithToken("tok"))
	if err := c.DeleteVideo(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/v1/videos/42" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
}

func TestDeleteVideoRequiresAuth(t *testing.T) {
	c := mustClient(t, "https://x.example")
	if err := c.DeleteVideo(context.Background(), 1); err == nil {
		t.Fatal("expected auth error")
	}
}
