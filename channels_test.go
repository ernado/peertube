package peertube

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMyChannels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/me" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"id":1,"videoChannels":[
			{"id":7,"name":"main","displayName":"Main channel"},
			{"id":8,"name":"second","displayName":"Second"}
		]}`)
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL, WithToken("tok"))
	channels, err := c.MyChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}
	if channels[0].ID != 7 || channels[0].Name != "main" || channels[0].DisplayName != "Main channel" {
		t.Fatalf("unexpected channel: %+v", channels[0])
	}
}

func TestMyChannelsRequiresAuth(t *testing.T) {
	c := mustClient(t, "https://x.example")
	if _, err := c.MyChannels(context.Background()); err == nil {
		t.Fatal("expected auth error")
	}
}
