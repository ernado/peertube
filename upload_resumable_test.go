package peertube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// resumableServer simulates PeerTube's node-uploadx resumable endpoint.
type resumableServer struct {
	mu        sync.Mutex
	assembled []byte // bytes received so far
	total     int64
	initCT    string // X-Upload-Content-Type observed on init
	chunks    int    // number of PUT chunks received
}

func (s *resumableServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/videos/upload-resumable", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			s.mu.Lock()
			s.total, _ = strconv.ParseInt(r.Header.Get("X-Upload-Content-Length"), 10, 64)
			s.initCT = r.Header.Get("X-Upload-Content-Type")
			s.assembled = nil
			s.mu.Unlock()

			// Validate metadata JSON.
			var meta map[string]any
			if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
				t.Errorf("init body: %v", err)
			}
			if meta["filename"] != "clip.mp4" {
				t.Errorf("filename = %v", meta["filename"])
			}
			w.Header().Set("Location", "/api/v1/videos/upload-resumable?upload_id=sess-1")
			w.WriteHeader(http.StatusCreated)

		case http.MethodPut:
			if r.URL.Query().Get("upload_id") != "sess-1" {
				t.Errorf("upload_id = %q", r.URL.Query().Get("upload_id"))
			}
			body, _ := io.ReadAll(r.Body)
			start, end, total := parseContentRange(t, r.Header.Get("Content-Range"))
			s.mu.Lock()
			if int64(len(s.assembled)) != start {
				t.Errorf("chunk start %d != assembled %d", start, len(s.assembled))
			}
			s.assembled = append(s.assembled, body...)
			s.chunks++
			done := end+1 == total
			s.mu.Unlock()

			if done {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"video":{"id":7,"uuid":"vid-uuid","shortUUID":"vs"}}`)
				return
			}
			w.WriteHeader(statusResumeIncomplete) // 308

		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	return mux
}

func parseContentRange(t *testing.T, v string) (start, end, total int64) {
	t.Helper()
	if _, err := fmt.Sscanf(v, "bytes %d-%d/%d", &start, &end, &total); err != nil {
		t.Fatalf("bad Content-Range %q: %v", v, err)
	}
	return start, end, total
}

func TestUploadResumableMultipleChunks(t *testing.T) {
	rs := &resumableServer{}
	srv := httptest.NewServer(rs.handler(t))
	defer srv.Close()

	// 25000 bytes with a 10 KiB (multiple of 1024) chunk size => 3 chunks.
	content := bytes.Repeat([]byte("A"), 25000)
	chunk := int64(10 * 1024)

	c := mustClient(t, srv.URL, WithToken("tok"))
	res, err := c.UploadResumable(context.Background(),
		UploadParams{Name: "valid name", ChannelID: 1},
		"clip.mp4", bytes.NewReader(content), int64(len(content)),
		ResumableOptions{ChunkSize: chunk},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != 7 || res.UUID != "vid-uuid" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if rs.chunks != 3 {
		t.Fatalf("expected 3 chunks, got %d", rs.chunks)
	}
	if !bytes.Equal(rs.assembled, content) {
		t.Fatalf("assembled bytes differ: got %d bytes", len(rs.assembled))
	}
	if rs.total != int64(len(content)) {
		t.Fatalf("advertised total = %d", rs.total)
	}
	if rs.initCT != "video/mp4" {
		t.Fatalf("content type = %q", rs.initCT)
	}
}

func TestUploadResumableSingleChunk(t *testing.T) {
	rs := &resumableServer{}
	srv := httptest.NewServer(rs.handler(t))
	defer srv.Close()

	content := []byte("small")
	c := mustClient(t, srv.URL, WithToken("tok"))
	res, err := c.UploadResumable(context.Background(),
		UploadParams{Name: "valid name", ChannelID: 1},
		"clip.mp4", bytes.NewReader(content), int64(len(content)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != 7 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if rs.chunks != 1 {
		t.Fatalf("expected 1 chunk, got %d", rs.chunks)
	}
}

func TestUploadResumableChunkError(t *testing.T) {
	var cancelled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/videos/upload-resumable", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Location", "/api/v1/videos/upload-resumable?upload_id=s")
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusConflict) // 409: chunk doesn't match range
		case http.MethodDelete:
			cancelled = true
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := mustClient(t, srv.URL, WithToken("tok"))
	_, err := c.UploadResumable(context.Background(),
		UploadParams{Name: "valid name", ChannelID: 1},
		"clip.mp4", strings.NewReader("data"), 4,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !cancelled {
		t.Error("expected session to be cancelled after failure")
	}
}

func TestUploadResumableValidation(t *testing.T) {
	c := mustClient(t, "https://x.example", WithToken("tok"))
	// Non-multiple-of-1024 chunk size.
	_, err := c.UploadResumable(context.Background(),
		UploadParams{Name: "valid name", ChannelID: 1},
		"clip.mp4", strings.NewReader("d"), 1, ResumableOptions{ChunkSize: 1000})
	if err == nil || !strings.Contains(err.Error(), "multiple of 1024") {
		t.Fatalf("expected chunk size error, got %v", err)
	}
	// Zero size.
	if _, err := c.UploadResumable(context.Background(),
		UploadParams{Name: "valid name", ChannelID: 1},
		"clip.mp4", strings.NewReader(""), 0); err == nil {
		t.Fatal("expected size error")
	}
}

func TestUploadIDFromLocation(t *testing.T) {
	tests := []struct {
		loc  string
		want string
		ok   bool
	}{
		{"/api/v1/videos/upload-resumable?upload_id=abc", "abc", true},
		{"https://h/api/v1/videos/upload-resumable?upload_id=xyz&x=1", "xyz", true},
		{"/no-query-here", "", false},
	}
	for _, tt := range tests {
		got, err := uploadIDFromLocation(tt.loc)
		if (err == nil) != tt.ok {
			t.Errorf("%q: err = %v, ok = %v", tt.loc, err, tt.ok)
		}
		if got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.loc, got, tt.want)
		}
	}
}
