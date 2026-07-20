package peertube

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sized builds a SizedVideo published daysAgo days before a fixed epoch.
func sized(id int, channel string, daysAgo int, size int64) SizedVideo {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return SizedVideo{
		Video:   Video{ID: id, UUID: "uuid-" + channel + "-" + itoa(id), PublishedAt: base.AddDate(0, 0, -daysAgo)},
		Channel: channel,
		Size:    size,
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func ids(vs []SizedVideo) []int {
	out := make([]int, 0, len(vs))
	for i := range vs {
		out = append(out, vs[i].ID)
	}
	return out
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

const gb = int64(1) << 30

func TestSelectToFitBalancesChannels(t *testing.T) {
	// A=12GB (6x2GB), B=6GB (3x2GB), C=2GB (1x2GB); total 20GB.
	videos := make([]SizedVideo, 0, 10)
	id := 0
	for i := range 6 {
		id++
		videos = append(videos, sized(id, "a", 60-i*10, 2*gb))
	}
	for i := range 3 {
		id++
		videos = append(videos, sized(id, "b", 60-i*10, 2*gb))
	}
	id++
	videos = append(videos, sized(id, "c", 60, 2*gb))

	got := SelectToFit(videos, 10*gb, 0)
	if remaining := TotalSize(videos) - TotalSize(got); remaining > 10*gb {
		t.Fatalf("remaining %d bytes exceeds target %d", remaining, 10*gb)
	}
	// 5 deletions of 2GB take 20GB -> 10GB. All should come from A (12GB),
	// which only drops below B once four are gone; the fifth ties A and B at
	// 4GB/6GB so B is not yet larger... A stays the victim until A < B.
	// A: 12->10->8->6 (3 deletions), then B(6) ties A(6): A first-seen wins ->
	// A=4, then B(6) > A(4) -> B=4. Total 20-10=10GB.
	want := []int{1, 2, 3, 4, 7}
	if !equalInts(ids(got), want) {
		t.Fatalf("deleted ids %v, want %v", ids(got), want)
	}
	// Channel C, the smallest, must be untouched.
	for _, v := range got {
		if v.Channel == "c" {
			t.Errorf("smallest channel c should not be pruned: %v", ids(got))
		}
	}
}

func TestSelectToFitOldestFirstWithinChannel(t *testing.T) {
	videos := []SizedVideo{
		sized(1, "a", 1, gb),  // newest
		sized(2, "a", 50, gb), // oldest
		sized(3, "a", 25, gb),
	}
	got := SelectToFit(videos, gb, 0)
	if want := []int{2, 3}; !equalInts(ids(got), want) {
		t.Fatalf("deleted ids %v, want %v (oldest first)", ids(got), want)
	}
}

func TestSelectToFitAlreadyUnderTarget(t *testing.T) {
	videos := []SizedVideo{sized(1, "a", 10, gb), sized(2, "b", 10, gb)}
	if got := SelectToFit(videos, 5*gb, 0); got != nil {
		t.Fatalf("nothing should be pruned, got %v", ids(got))
	}
	// Exactly at the target is also fine.
	if got := SelectToFit(videos, 2*gb, 0); got != nil {
		t.Fatalf("at target nothing should be pruned, got %v", ids(got))
	}
}

func TestSelectToFitKeepPerChannelProtects(t *testing.T) {
	videos := []SizedVideo{
		sized(1, "a", 1, gb), sized(2, "a", 20, gb), sized(3, "a", 30, gb),
		sized(4, "b", 2, gb), sized(5, "b", 40, gb),
	}
	// Target 0 would delete everything, but the newest 1 of each is protected.
	got := SelectToFit(videos, 0, 1)
	want := []int{3, 2, 5} // a is bigger (3GB) so it goes first, oldest-first
	if !equalInts(ids(got), want) {
		t.Fatalf("deleted ids %v, want %v", ids(got), want)
	}
	// Protection may leave us above target; that is expected, not an error.
	if remaining := TotalSize(videos) - TotalSize(got); remaining != 2*gb {
		t.Fatalf("remaining %d, want %d (two protected videos)", remaining, 2*gb)
	}
}

func TestSelectToFitKeepPerChannelExceedsCount(t *testing.T) {
	videos := []SizedVideo{sized(1, "a", 1, gb), sized(2, "b", 1, gb)}
	if got := SelectToFit(videos, 0, 5); got != nil {
		t.Fatalf("all videos protected, got %v", ids(got))
	}
}

func TestVideoSizeSumsFilesAndPlaylists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/videos/42" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		io.WriteString(w, `{"id":42,"files":[{"size":100},{"size":200}],
			"streamingPlaylists":[{"files":[{"size":300},{"size":400}]}]}`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.VideoSize(context.Background(), 42)
	if err != nil {
		t.Fatalf("VideoSize: %v", err)
	}
	if want := int64(1000); got != want {
		t.Fatalf("VideoSize = %d, want %d", got, want)
	}
}

// HLS-only instances report no plain files; the size must come from the
// streaming playlists alone.
func TestVideoSizeHLSOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":7,"files":[],"streamingPlaylists":[{"files":[{"size":4096}]}]}`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.VideoSize(context.Background(), 7)
	if err != nil {
		t.Fatalf("VideoSize: %v", err)
	}
	if got != 4096 {
		t.Fatalf("VideoSize = %d, want 4096", got)
	}
}

func TestVideoSizeAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"not found"}`)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.VideoSize(context.Background(), 1); err == nil {
		t.Fatal("expected an error for 404")
	} else if !strings.Contains(err.Error(), "404") {
		t.Fatalf("error %v should mention the status", err)
	}
}
