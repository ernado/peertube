package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// withTempConfig points configPathFn at a fresh temp file for the test.
func withTempConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	old := configPathFn
	configPathFn = func() (string, error) { return path, nil }
	t.Cleanup(func() { configPathFn = old })
	return path
}

func TestConfigSaveLoadRoundTrip(t *testing.T) {
	withTempConfig(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Instances) != 0 {
		t.Fatalf("expected empty config, got %+v", cfg)
	}

	cfg.set("https://a.example", instance{Username: "alice", Password: "pw"}, false)
	if cfg.Default != "https://a.example" {
		t.Errorf("first instance should become default, got %q", cfg.Default)
	}
	cfg.set("https://b.example", instance{Username: "bob", Password: "pw2"}, false)
	if cfg.Default != "https://a.example" {
		t.Errorf("default should not change without makeDefault, got %q", cfg.Default)
	}
	cfg.set("https://b.example", instance{Username: "bob", Password: "pw2"}, true)
	if cfg.Default != "https://b.example" {
		t.Errorf("makeDefault should switch default, got %q", cfg.Default)
	}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}

	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != "https://b.example" || got.Instances["https://a.example"].Username != "alice" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestLoginCommandPersists(t *testing.T) {
	withTempConfig(t)
	srv := mockServer(t, `[]`)
	defer srv.Close()

	out, err := execViaCmd(t, "login",
		"--url", srv.URL, "--username", "alice", "--password", "pw",
	)
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Saved credentials") {
		t.Errorf("expected save confirmation: %s", out)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := cfg.Instances[srv.URL]
	if !ok || inst.Username != "alice" || inst.Password != "pw" {
		t.Fatalf("credentials not persisted: %+v", cfg)
	}
	if cfg.Default != srv.URL {
		t.Errorf("first login should set default, got %q", cfg.Default)
	}
}

// After login, a bare command resolves url + credentials from the config.
func TestSavedLoginUsedByChannelList(t *testing.T) {
	withTempConfig(t)
	srv := mockServer(t, `[{"id":7,"name":"main","displayName":"Main"}]`)
	defer srv.Close()

	if out, err := execViaCmd(t, "login",
		"--url", srv.URL, "--username", "alice", "--password", "pw"); err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}

	// No --url/--username/--password: everything comes from the saved default.
	out, err := execViaCmd(t, "channel", "list")
	if err != nil {
		t.Fatalf("channel list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("expected channel listing from saved login: %s", out)
	}
}

func TestLoginPromptsForCredentials(t *testing.T) {
	withTempConfig(t)
	srv := mockServer(t, `[]`)
	defer srv.Close()

	// Only --url given; username and password are typed at the prompt.
	out, err := execViaCmdStdin(t, "alice\npw\n", "login", "--url", srv.URL)
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if !strings.Contains(out, "PeerTube username:") || !strings.Contains(out, "PeerTube password:") {
		t.Errorf("expected prompts in output: %s", out)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	inst := cfg.Instances[srv.URL]
	if inst.Username != "alice" || inst.Password != "pw" {
		t.Fatalf("prompted credentials not persisted: %+v", cfg)
	}
}

func TestLoginPromptsPasswordOnly(t *testing.T) {
	withTempConfig(t)
	srv := mockServer(t, `[]`)
	defer srv.Close()

	// Username via flag; only the password is prompted.
	out, err := execViaCmdStdin(t, "secret\n", "login", "--url", srv.URL, "--username", "bob")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	cfg, _ := loadConfig()
	inst := cfg.Instances[srv.URL]
	if inst.Username != "bob" || inst.Password != "secret" {
		t.Fatalf("unexpected persisted credentials: %+v", inst)
	}
}

func TestLoginFailureNotPersisted(t *testing.T) {
	path := withTempConfig(t)
	srv := failingLoginServer(t)
	defer srv.Close()

	_, err := execViaCmd(t, "login",
		"--url", srv.URL, "--username", "alice", "--password", "bad",
	)
	if err == nil {
		t.Fatal("expected login failure")
	}
	// Config file must not have been written.
	if _, err := loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if fileExists(path) {
		t.Errorf("config should not be written on failed login")
	}
}
