package peertube

import (
	"context"
	"encoding/json"
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

func TestCreateChannel(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/video-channels" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, `{"videoChannel":{"id":51}}`)
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL, WithToken("tok"))
	ch, err := c.CreateChannel(context.Background(), CreateChannelParams{
		Name:        "my_channel",
		DisplayName: "My Channel",
		Description: "about",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != 51 || ch.Name != "my_channel" || ch.DisplayName != "My Channel" {
		t.Fatalf("unexpected channel: %+v", ch)
	}
	if gotBody["name"] != "my_channel" || gotBody["displayName"] != "My Channel" || gotBody["description"] != "about" {
		t.Fatalf("unexpected request body: %+v", gotBody)
	}
	if _, ok := gotBody["support"]; ok {
		t.Errorf("empty support should be omitted: %+v", gotBody)
	}
}

func TestCreateChannelValidation(t *testing.T) {
	c := mustClient(t, "https://x.example", WithToken("tok"))
	if _, err := c.CreateChannel(context.Background(), CreateChannelParams{DisplayName: "x"}); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := c.CreateChannel(context.Background(), CreateChannelParams{Name: "x"}); err == nil {
		t.Error("expected error for empty displayName")
	}
}

func TestCreateChannelRequiresAuth(t *testing.T) {
	c := mustClient(t, "https://x.example")
	if _, err := c.CreateChannel(context.Background(), CreateChannelParams{Name: "a", DisplayName: "b"}); err == nil {
		t.Fatal("expected auth error")
	}
}
