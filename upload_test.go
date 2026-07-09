package peertube

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUploadValidation(t *testing.T) {
	c := mustClient(t, "https://x.example", WithToken("tok"))
	tests := []struct {
		name   string
		params UploadParams
		fname  string
	}{
		{"short name", UploadParams{Name: "ab", ChannelID: 1}, "v.mp4"},
		{"no channel", UploadParams{Name: "valid name", ChannelID: 0}, "v.mp4"},
		{"too many tags", UploadParams{Name: "valid name", ChannelID: 1, Tags: []string{"aa", "bb", "cc", "dd", "ee", "ff"}}, "v.mp4"},
		{"bad tag len", UploadParams{Name: "valid name", ChannelID: 1, Tags: []string{"a"}}, "v.mp4"},
		{"no filename", UploadParams{Name: "valid name", ChannelID: 1}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.Upload(context.Background(), tt.params, tt.fname, strings.NewReader("data"))
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestUploadRequiresAuth(t *testing.T) {
	c := mustClient(t, "https://x.example")
	_, err := c.Upload(context.Background(), UploadParams{Name: "valid name", ChannelID: 1}, "v.mp4", strings.NewReader("d"))
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("expected auth error, got %v", err)
	}
}

func TestUploadSuccess(t *testing.T) {
	const content = "fake-video-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/videos/upload" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth = %q", got)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("multipart: %v", err)
		}
		fields := map[string]string{}
		var tags []string
		var videoData string
		var videoFilename string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(part)
			switch {
			case part.FormName() == "tags[]":
				tags = append(tags, string(data))
			case part.FileName() != "":
				videoFilename = part.FileName()
				videoData = string(data)
			default:
				fields[part.FormName()] = string(data)
			}
		}
		if fields["name"] != "My Video" || fields["channelId"] != "3" {
			t.Errorf("unexpected fields: %+v", fields)
		}
		if fields["privacy"] != "1" {
			t.Errorf("privacy = %q", fields["privacy"])
		}
		if len(tags) != 2 || tags[0] != "go" || tags[1] != "test" {
			t.Errorf("tags = %v", tags)
		}
		if videoFilename != "clip.mp4" || videoData != content {
			t.Errorf("video part = %q / %q", videoFilename, videoData)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"video":{"id":42,"uuid":"uuid-1","shortUUID":"su-1"}}`)
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL, WithToken("tok"))
	res, err := c.Upload(context.Background(), UploadParams{
		Name:      "My Video",
		ChannelID: 3,
		Privacy:   PrivacyPublic,
		Tags:      []string{"go", "test"},
	}, "clip.mp4", strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != 42 || res.UUID != "uuid-1" || res.ShortUUID != "su-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestUploadAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body so the client's pipe writer completes.
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		io.WriteString(w, `{"code":"quota_reached","error":"quota exceeded"}`)
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL, WithToken("tok"))
	_, err := c.Upload(context.Background(), UploadParams{Name: "valid name", ChannelID: 1}, "v.mp4", strings.NewReader("d"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %v", err)
	}
	if apiErr.Status != http.StatusRequestEntityTooLarge || apiErr.Code != "quota_reached" {
		t.Fatalf("unexpected APIError: %+v", apiErr)
	}
}

// ensure the multipart body is actually parseable end-to-end (guards the pipe).
func TestUploadMultipartWellFormed(t *testing.T) {
	var boundary string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		boundary = params["boundary"]
		mr := multipart.NewReader(r.Body, boundary)
		if _, err := mr.ReadForm(1 << 20); err != nil {
			t.Fatalf("ReadForm: %v", err)
		}
		io.WriteString(w, `{"video":{"id":1}}`)
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL, WithToken("tok"))
	if _, err := c.Upload(context.Background(), UploadParams{Name: "valid name", ChannelID: 1}, "v.mp4", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if boundary == "" {
		t.Fatal("no boundary parsed")
	}
}
