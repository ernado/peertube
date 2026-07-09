package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain isolates every test from the user's real config file by default.
// Tests needing their own file call withTempConfig to override further.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "peertube-cli-test")
	if err != nil {
		panic(err)
	}
	configPathFn = func() (string, error) { return filepath.Join(dir, "config.json"), nil }
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestValidateMissingFlags(t *testing.T) {
	err := options{}.validate()
	if err == nil {
		t.Fatal("expected error for empty options")
	}
	// channel-id is auto-discovered, so it must NOT be reported as missing.
	if strings.Contains(err.Error(), "channel-id") {
		t.Errorf("channel-id should not be required: %v", err)
	}
	for _, want := range []string{"--url", "--username", "--password", "--file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

// execViaCmd builds the cobra command and runs it with the given args.
func execViaCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return execViaCmdStdin(t, "", args...)
}

// execViaCmdStdin is execViaCmd with a canned stdin for interactive prompts.
func execViaCmdStdin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// mockServer returns a PeerTube-like server. channels controls what
// /users/me reports for channel auto-discovery.
func mockServer(t *testing.T, channelsJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth-clients/local", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"client_id":"cid","client_secret":"csec"}`)
	})
	mux.HandleFunc("/api/v1/users/token", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"atok"}`)
	})
	mux.HandleFunc("/api/v1/users/me", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"videoChannels":`+channelsJSON+`}`)
	})
	mux.HandleFunc("/api/v1/video-channels", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %s", r.Method)
		}
		io.WriteString(w, `{"videoChannel":{"id":123}}`)
	})
	mux.HandleFunc("/api/v1/video-channels/my_channel/avatar/pick", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"avatars":[{"fileUrl":"https://h/a.png","width":48}]}`)
	})
	mux.HandleFunc("/api/v1/video-channels/my_channel/banner/pick", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"banners":[{"fileUrl":"https://h/b.png","width":1920}]}`)
	})
	mux.HandleFunc("/api/v1/videos/upload-resumable", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if r.Header.Get("Authorization") != "Bearer atok" {
				t.Errorf("missing bearer token")
			}
			w.Header().Set("Location", "/api/v1/videos/upload-resumable?upload_id=s1")
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			io.Copy(io.Discard, r.Body)
			io.WriteString(w, `{"video":{"id":99,"uuid":"u","shortUUID":"s"}}`)
		}
	})
	return httptest.NewServer(mux)
}

// failingLoginServer rejects the token request, simulating bad credentials.
func failingLoginServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth-clients/local", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"client_id":"cid","client_secret":"csec"}`)
	})
	mux.HandleFunc("/api/v1/users/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":"invalid_grant","error":"bad credentials"}`)
	})
	return httptest.NewServer(mux)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeVideo(t *testing.T) string {
	t.Helper()
	return writeTempFile(t, "clip.mp4", "hello video")
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWithExplicitChannel(t *testing.T) {
	srv := mockServer(t, `[]`)
	defer srv.Close()

	out, err := execViaCmd(t, "upload",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
		"--channel-id", "3", "--file", writeVideo(t), "--tags", "go,peertube",
	)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "uuid=u") {
		t.Errorf("missing success output: %s", out)
	}
}

func TestRunAutoDiscoverSingleChannel(t *testing.T) {
	srv := mockServer(t, `[{"id":7,"name":"main","displayName":"Main"}]`)
	defer srv.Close()

	out, err := execViaCmd(t, "upload",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
		"--file", writeVideo(t),
	)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Auto-selected channel 7") {
		t.Errorf("expected auto-selection message: %s", out)
	}
	if !strings.Contains(out, "channel 7") {
		t.Errorf("expected upload to channel 7: %s", out)
	}
}

func TestRunAutoDiscoverMultipleChannelsFails(t *testing.T) {
	srv := mockServer(t, `[{"id":7,"name":"a"},{"id":8,"name":"b"}]`)
	defer srv.Close()

	out, err := execViaCmd(t, "upload",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
		"--file", writeVideo(t),
	)
	if err == nil {
		t.Fatalf("expected error for ambiguous channels, out: %s", out)
	}
	if !strings.Contains(err.Error(), "--channel-id") {
		t.Errorf("error should ask for --channel-id: %v", err)
	}
	for _, id := range []string{"7", "8"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error should list channel %s: %v", id, err)
		}
	}
}

func TestRunAutoDiscoverNoChannelsFails(t *testing.T) {
	srv := mockServer(t, `[]`)
	defer srv.Close()

	_, err := execViaCmd(t, "upload",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
		"--file", writeVideo(t),
	)
	if err == nil || !strings.Contains(err.Error(), "no video channels") {
		t.Fatalf("expected no-channels error, got %v", err)
	}
}

func TestCredentialsFromEnv(t *testing.T) {
	srv := mockServer(t, `[]`)
	defer srv.Close()

	// Neither --username nor --password given; both come from the environment.
	t.Setenv("PEERTUBE_USER", "alice")
	t.Setenv("PEERTUBE_PASSWORD", "envpw")
	out, err := execViaCmd(t, "upload",
		"--url", srv.URL,
		"--channel-id", "3", "--file", writeVideo(t),
	)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
}

func TestChannelList(t *testing.T) {
	srv := mockServer(t, `[
		{"id":7,"name":"main","displayName":"Main channel"},
		{"id":8,"name":"second","displayName":"Second"}
	]`)
	defer srv.Close()

	out, err := execViaCmd(t, "channel", "list",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
	)
	if err != nil {
		t.Fatalf("channel list: %v\n%s", err, out)
	}
	for _, want := range []string{"ID", "NAME", "DISPLAY NAME", "7", "main", "Main channel", "8", "second"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q:\n%s", want, out)
		}
	}
}

func TestChannelCreate(t *testing.T) {
	srv := mockServer(t, `[]`)
	defer srv.Close()

	out, err := execViaCmd(t, "channel", "create",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
		"--name", "my_channel", "--display-name", "My Channel",
	)
	if err != nil {
		t.Fatalf("channel create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "id=123") || !strings.Contains(out, "my_channel") {
		t.Errorf("expected created channel output: %s", out)
	}
}

func TestChannelCreateWithImages(t *testing.T) {
	srv := mockServer(t, `[]`)
	defer srv.Close()

	out, err := execViaCmd(t, "channel", "create",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
		"--name", "my_channel", "--display-name", "My Channel",
		"--avatar", writeTempFile(t, "a.png", "PNG"),
		"--banner", writeTempFile(t, "b.png", "PNG"),
	)
	if err != nil {
		t.Fatalf("channel create: %v\n%s", err, out)
	}
	for _, want := range []string{"Created channel", "Set avatar for my_channel", "Set banner for my_channel"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q:\n%s", want, out)
		}
	}
}

func TestChannelSetAvatar(t *testing.T) {
	srv := mockServer(t, `[]`)
	defer srv.Close()

	out, err := execViaCmd(t, "channel", "set-avatar",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
		"--channel", "my_channel", "--file", writeTempFile(t, "a.png", "PNG"),
	)
	if err != nil {
		t.Fatalf("set-avatar: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Set avatar for my_channel") || !strings.Contains(out, "https://h/a.png") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestChannelSetBanner(t *testing.T) {
	srv := mockServer(t, `[]`)
	defer srv.Close()

	out, err := execViaCmd(t, "channel", "set-banner",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
		"--channel", "my_channel", "--file", writeTempFile(t, "b.png", "PNG"),
	)
	if err != nil {
		t.Fatalf("set-banner: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Set banner for my_channel") {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestChannelSetAvatarRequiresFlags(t *testing.T) {
	out, err := execViaCmd(t, "channel", "set-avatar",
		"--url", "https://x.example", "--username", "a", "--password", "b",
		"--channel", "my_channel",
	)
	if err == nil {
		t.Fatalf("expected error, out: %s", out)
	}
	if !strings.Contains(err.Error(), "--file") {
		t.Errorf("error should mention --file: %v", err)
	}
}

func TestChannelCreateRequiresNameAndDisplayName(t *testing.T) {
	out, err := execViaCmd(t, "channel", "create",
		"--url", "https://x.example", "--username", "alice", "--password", "pw",
		"--name", "only_name",
	)
	if err == nil {
		t.Fatalf("expected error, out: %s", out)
	}
	if !strings.Contains(err.Error(), "--display-name") {
		t.Errorf("error should mention --display-name: %v", err)
	}
}

func TestChannelCreateRequiresAuth(t *testing.T) {
	_, err := execViaCmd(t, "channel", "create", "--url", "https://x.example",
		"--name", "a", "--display-name", "b")
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !strings.Contains(err.Error(), "--password") {
		t.Errorf("error should mention missing auth: %v", err)
	}
}

func TestChannelListEmpty(t *testing.T) {
	srv := mockServer(t, `[]`)
	defer srv.Close()

	out, err := execViaCmd(t, "channel", "list",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
	)
	if err != nil {
		t.Fatalf("channel list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No video channels") {
		t.Errorf("expected empty message: %s", out)
	}
}

func TestChannelListRequiresAuth(t *testing.T) {
	out, err := execViaCmd(t, "channel", "list", "--url", "https://x.example")
	if err == nil {
		t.Fatalf("expected auth error, out: %s", out)
	}
	for _, want := range []string{"--username", "--password"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestChannelListCredentialsFromEnv(t *testing.T) {
	srv := mockServer(t, `[{"id":7,"name":"main","displayName":"Main"}]`)
	defer srv.Close()

	t.Setenv("PEERTUBE_USER", "alice")
	t.Setenv("PEERTUBE_PASSWORD", "envpw")
	out, err := execViaCmd(t, "channel", "list", "--url", srv.URL)
	if err != nil {
		t.Fatalf("channel list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("expected channel listing: %s", out)
	}
}

func TestMissingCredentialsMentionEnv(t *testing.T) {
	err := options{}.validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"PEERTUBE_USER", "PEERTUBE_PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}
