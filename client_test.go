package peertube

import (
	"net/http"
	"testing"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{"ok", "https://peertube.example.org", false},
		{"ok trailing slash", "https://peertube.example.org/", false},
		{"empty", "", true},
		{"relative", "/api/v1", true},
		{"no host", "https://", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(tt.baseURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewClient(%q) err = %v, wantErr = %v", tt.baseURL, err, tt.wantErr)
			}
		})
	}
}

func TestClientAPIURL(t *testing.T) {
	c, err := NewClient("https://peertube.example.org/")
	if err != nil {
		t.Fatal(err)
	}
	got := c.apiURL("videos/upload")
	want := "https://peertube.example.org/api/v1/videos/upload"
	if got != want {
		t.Fatalf("apiURL = %q, want %q", got, want)
	}
	// Leading slash on the element must not double up.
	if got := c.apiURL("/users/token"); got != "https://peertube.example.org/api/v1/users/token" {
		t.Fatalf("apiURL with leading slash = %q", got)
	}
}

func TestWithToken(t *testing.T) {
	c, err := NewClient("https://x.example", WithToken("tok"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Token() != "tok" {
		t.Fatalf("Token() = %q, want tok", c.Token())
	}
	c.SetToken("other")
	if c.Token() != "other" {
		t.Fatalf("SetToken not applied: %q", c.Token())
	}
}

func TestWithHTTPClientNilKeepsDefault(t *testing.T) {
	c, err := NewClient("https://x.example", WithHTTPClient(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.http != http.DefaultClient {
		t.Fatal("nil Doer should leave DefaultClient in place")
	}
}
