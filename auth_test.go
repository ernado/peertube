package peertube

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOAuthClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/oauth-clients/local" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"client_id":"id123","client_secret":"secret456"}`)
	}))
	defer srv.Close()

	c := mustClient(t, srv.URL)
	oc, err := c.OAuthClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if oc.ClientID != "id123" || oc.ClientSecret != "secret456" {
		t.Fatalf("unexpected credentials: %+v", oc)
	}
}

func TestLoginFlow(t *testing.T) {
	var gotForm url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth-clients/local", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"client_id":"cid","client_secret":"csecret"}`)
	})
	mux.HandleFunc("/api/v1/users/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("bad content type %q", r.Header.Get("Content-Type"))
		}
		_ = r.ParseForm()
		gotForm = r.PostForm
		io.WriteString(w, `{"token_type":"Bearer","access_token":"atok","refresh_token":"rtok","expires_in":3600}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := mustClient(t, srv.URL)
	tok, err := c.Login(context.Background(), "alice", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "atok" || tok.RefreshToken != "rtok" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	if c.Token() != "atok" {
		t.Fatalf("token not stored on client: %q", c.Token())
	}
	// Verify the password grant form was sent correctly.
	for k, want := range map[string]string{
		"client_id": "cid", "client_secret": "csecret",
		"grant_type": "password", "username": "alice", "password": "pw",
	} {
		if got := gotForm.Get(k); got != want {
			t.Errorf("form[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestLoginWithPresetClientAndOTP(t *testing.T) {
	var otp string
	var hitClientEndpoint bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth-clients/local", func(w http.ResponseWriter, r *http.Request) {
		hitClientEndpoint = true
	})
	mux.HandleFunc("/api/v1/users/token", func(w http.ResponseWriter, r *http.Request) {
		otp = r.Header.Get("x-peertube-otp")
		io.WriteString(w, `{"access_token":"atok"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := mustClient(t, srv.URL)
	_, err := c.Login(context.Background(), "u", "p", LoginOptions{
		OTP:    "123456",
		Client: &OAuthClient{ClientID: "x", ClientSecret: "y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hitClientEndpoint {
		t.Error("should not fetch oauth client when preset")
	}
	if otp != "123456" {
		t.Errorf("otp header = %q", otp)
	}
}

func TestLoginError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth-clients/local", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"client_id":"cid","client_secret":"csecret"}`)
	})
	mux.HandleFunc("/api/v1/users/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":"invalid_grant","error":"credentials are invalid"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := mustClient(t, srv.URL)
	_, err := c.Login(context.Background(), "u", "bad")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_grant" {
		t.Fatalf("unexpected APIError: %+v", apiErr)
	}
}

func mustClient(t *testing.T, baseURL string, opts ...Option) *Client {
	t.Helper()
	c, err := NewClient(baseURL, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
