package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ernado/peertube"
)

// tokenServer records how each token was obtained (password grant vs refresh
// grant) and which bearer tokens were presented on authenticated requests.
type tokenServer struct {
	mu       sync.Mutex
	grants   []string // grant_type of each /users/token call, in order
	bearers  []string // Authorization tokens seen on /users/me
	issued   int      // number of access tokens minted
	expires  int      // expires_in served for access tokens
	refrExpr int      // refresh_token_expires_in served
	// lastRefresh is the only refresh token the server still accepts.
	lastRefresh string
	// revoked, when set, makes /users/me reject this bearer token with a 401.
	revoked string
	srv     *httptest.Server
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{expires: 3600, refrExpr: 14 * 24 * 3600}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/oauth-clients/local", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"client_id":"cid","client_secret":"csec"}`)
	})
	mux.HandleFunc("/api/v1/users/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ts.mu.Lock()
		grant := r.FormValue("grant_type")
		ts.grants = append(ts.grants, grant)
		// Only the most recently issued refresh token is redeemable, as on a
		// real instance; anything else is rejected so the caller must fall back.
		if grant == "refresh_token" && r.FormValue("refresh_token") != ts.lastRefresh {
			ts.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid refresh token"}`)
			return
		}
		ts.issued++
		access := fmt.Sprintf("access-%d", ts.issued)
		refresh := fmt.Sprintf("refresh-%d", ts.issued)
		ts.lastRefresh = refresh
		expires, refrExpr := ts.expires, ts.refrExpr
		ts.mu.Unlock()
		fmt.Fprintf(w, `{"token_type":"Bearer","access_token":%q,"refresh_token":%q,
			"expires_in":%d,"refresh_token_expires_in":%d}`, access, refresh, expires, refrExpr)
	})
	mux.HandleFunc("/api/v1/users/me", func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		ts.mu.Lock()
		ts.bearers = append(ts.bearers, bearer)
		revoked := ts.revoked
		ts.mu.Unlock()
		if revoked != "" && bearer == revoked {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"expired token"}`)
			return
		}
		io.WriteString(w, `{"videoChannels":[{"id":1,"name":"chan","displayName":"chan"}]}`)
	})
	ts.srv = httptest.NewServer(mux)
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *tokenServer) grantTypes() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.grants...)
}

func (ts *tokenServer) seenBearers() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.bearers...)
}

// listArgs builds a "channel list" invocation against url, the cheapest
// authenticated command, plus any extra flags.
func listArgs(url string, extra ...string) []string {
	return append([]string{channel, list, "--url", url, "--username", "a", "--password", "b"}, extra...)
}

// readToken returns the token cached in the config for url.
func readToken(t *testing.T, url string) *savedToken {
	t.Helper()
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg.Instances[url].Token
}

// A second command must reuse the cached access token instead of logging in.
func TestTokenCachedAcrossCommands(t *testing.T) {
	withTempConfig(t)
	ts := newTokenServer(t)
	args := listArgs(ts.srv.URL)

	if out, err := execViaCmd(t, args...); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	if got := ts.grantTypes(); len(got) != 1 || got[0] != "password" {
		t.Fatalf("first run grants = %v, want one password grant", got)
	}
	st := readToken(t, ts.srv.URL)
	if st == nil || st.AccessToken != "access-1" {
		t.Fatalf("token not cached: %+v", st)
	}
	if st.ExpiresAt.IsZero() || st.ClientID != "cid" || st.ClientSecret != "csec" {
		t.Fatalf("cached token missing expiry or oauth client: %+v", st)
	}

	out, err := execViaCmd(t, args...)
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if got := ts.grantTypes(); len(got) != 1 {
		t.Fatalf("second run issued another grant %v, want the cached token reused", got)
	}
	if strings.Contains(out, "Logging in") {
		t.Errorf("second run should not announce a login:\n%s", out)
	}
	if got := ts.seenBearers(); len(got) != 2 || got[1] != "access-1" {
		t.Errorf("bearers = %v, want the cached access-1 reused", got)
	}
}

// An expired access token with a live refresh token must use the refresh grant,
// which never sends the password.
func TestTokenRefreshedWhenAccessExpired(t *testing.T) {
	withTempConfig(t)
	ts := newTokenServer(t)
	args := listArgs(ts.srv.URL)

	if out, err := execViaCmd(t, args...); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	expireAccess(t, ts.srv.URL)

	out, err := execViaCmd(t, args...)
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	want := []string{"password", "refresh_token"}
	if got := ts.grantTypes(); len(got) != 2 || got[1] != want[1] {
		t.Fatalf("grants = %v, want %v", got, want)
	}
	// The refreshed token must be cached in turn, replacing the stale one.
	if st := readToken(t, ts.srv.URL); st == nil || st.AccessToken != "access-2" {
		t.Fatalf("refreshed token not cached: %+v", st)
	}
	if got := ts.seenBearers(); got[len(got)-1] != "access-2" {
		t.Errorf("bearers = %v, want the refreshed access-2 used", got)
	}
}

// With both tokens expired there is nothing to reuse, so the password grant
// runs again.
func TestTokenFullLoginWhenRefreshExpired(t *testing.T) {
	withTempConfig(t)
	ts := newTokenServer(t)
	args := listArgs(ts.srv.URL)

	if out, err := execViaCmd(t, args...); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	mutateToken(t, ts.srv.URL, func(st *savedToken) {
		st.ExpiresAt = time.Now().Add(-time.Hour)
		st.RefreshExpiresAt = time.Now().Add(-time.Hour)
	})

	if out, err := execViaCmd(t, args...); err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if got := ts.grantTypes(); len(got) != 2 || got[1] != "password" {
		t.Fatalf("grants = %v, want a second password grant", got)
	}
}

// A failing refresh must fall back to the password grant, not abort.
func TestTokenRefreshFailureFallsBackToLogin(t *testing.T) {
	withTempConfig(t)
	ts := newTokenServer(t)
	args := listArgs(ts.srv.URL)

	if out, err := execViaCmd(t, args...); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	// Expire the access token and corrupt the refresh grant's client secret so
	// the instance rejects the exchange.
	mutateToken(t, ts.srv.URL, func(st *savedToken) {
		st.ExpiresAt = time.Now().Add(-time.Hour)
		st.RefreshToken = "bogus"
	})
	ts.mu.Lock()
	ts.expires = 0 // also exercise a token with no lifetime: not cacheable
	ts.mu.Unlock()

	out, err := execViaCmd(t, args...)
	if err != nil {
		t.Fatalf("second run should recover via login: %v\n%s", err, out)
	}
	got := ts.grantTypes()
	if len(got) != 3 || got[1] != "refresh_token" || got[2] != "password" {
		t.Fatalf("grants = %v, want password, refresh_token, password", got)
	}
	// expires_in of 0 means the token cannot be cached; it must be dropped
	// rather than stored with a bogus expiry.
	if st := readToken(t, ts.srv.URL); st != nil {
		t.Errorf("token without a lifetime should not be cached: %+v", st)
	}
}

// --relogin must bypass a perfectly valid cached token.
func TestTokenReloginForcesPasswordGrant(t *testing.T) {
	withTempConfig(t)
	ts := newTokenServer(t)
	args := listArgs(ts.srv.URL)

	if out, err := execViaCmd(t, args...); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	if out, err := execViaCmd(t, append(args, "--relogin")...); err != nil {
		t.Fatalf("relogin run: %v\n%s", err, out)
	}
	got := ts.grantTypes()
	if len(got) != 2 || got[1] != "password" {
		t.Fatalf("grants = %v, want --relogin to force a password grant", got)
	}
	if st := readToken(t, ts.srv.URL); st == nil || st.AccessToken != "access-2" {
		t.Fatalf("relogin should refresh the cache: %+v", st)
	}
}

// The login command verifies the credentials it saves, so it must not accept a
// cached token in place of an actual password grant.
func TestLoginCommandAlwaysVerifiesCredentials(t *testing.T) {
	withTempConfig(t)
	ts := newTokenServer(t)

	for i := range 2 {
		out, err := execViaCmd(t, "login", "--url", ts.srv.URL, "--username", "a", "--password", "b")
		if err != nil {
			t.Fatalf("login %d: %v\n%s", i, err, out)
		}
	}
	got := ts.grantTypes()
	if len(got) != 2 || got[0] != "password" || got[1] != "password" {
		t.Fatalf("grants = %v, want both logins to run the password grant", got)
	}
	// Saving credentials must not discard the token just obtained.
	if st := readToken(t, ts.srv.URL); st == nil || st.AccessToken != "access-2" {
		t.Fatalf("login should leave a usable cached token: %+v", st)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if inst := cfg.Instances[ts.srv.URL]; inst.Username != "a" || inst.Password != "b" {
		t.Errorf("credentials not saved: %+v", inst)
	}
}

// A cached token that is still unexpired but was revoked server-side cannot be
// detected without spending a request, so it surfaces as a 401 on the actual
// call. This documents that it fails cleanly and that --relogin recovers.
func TestTokenRevokedServerSide(t *testing.T) {
	withTempConfig(t)
	ts := newTokenServer(t)

	if out, err := execViaCmd(t, listArgs(ts.srv.URL)...); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	ts.mu.Lock()
	ts.revoked = "access-1"
	ts.mu.Unlock()

	out, err := execViaCmd(t, listArgs(ts.srv.URL)...)
	if err == nil {
		t.Fatalf("expected a failure with a revoked token:\n%s", out)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %v should report the rejected token", err)
	}
	// The recovery path must work without touching the config by hand.
	if out, err := execViaCmd(t, listArgs(ts.srv.URL, "--relogin")...); err != nil {
		t.Fatalf("--relogin should recover: %v\n%s", err, out)
	}
	if got := ts.grantTypes(); len(got) != 2 || got[1] != "password" {
		t.Fatalf("grants = %v, want --relogin to re-run the password grant", got)
	}
}

// The token file holds credentials, so it must stay owner-only.
func TestTokenFilePermissions(t *testing.T) {
	path := withTempConfig(t)
	ts := newTokenServer(t)

	if out, err := execViaCmd(t, listArgs(ts.srv.URL)...); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config mode = %o, want 600", perm)
	}
}

func TestSavedTokenUsability(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	full := func() *savedToken {
		return &savedToken{
			AccessToken: "a", RefreshToken: "r",
			ExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(24 * time.Hour),
			ClientID: "cid", ClientSecret: "csec",
		}
	}
	if !full().accessUsable(now) {
		t.Error("a live access token should be usable")
	}
	if !full().refreshUsable(now) {
		t.Error("a live refresh token should be usable")
	}
	// A nil token must be safe to interrogate: it is the no-cache case.
	var nilTok *savedToken
	if nilTok.accessUsable(now) || nilTok.refreshUsable(now) {
		t.Error("a nil token must not be usable")
	}
	// Expiring within the skew window counts as already expired.
	st := full()
	st.ExpiresAt = now.Add(tokenSkew / 2)
	if st.accessUsable(now) {
		t.Error("a token expiring inside the skew window must not be used")
	}
	// An unknown expiry is not trusted, or it would be retried forever.
	st = full()
	st.ExpiresAt = time.Time{}
	if st.accessUsable(now) {
		t.Error("a token with no expiry must not be used")
	}
	// The oauth client is part of the refresh grant.
	st = full()
	st.ClientSecret = ""
	if st.refreshUsable(now) {
		t.Error("refresh without the oauth client must not be attempted")
	}
}

func TestNewSavedToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	oc := peertube.OAuthClient{ClientID: "cid", ClientSecret: "csec"}

	st := newSavedToken(peertube.Token{
		AccessToken: "a", RefreshToken: "r", ExpiresIn: 3600, RefreshTokenExpiresIn: 7200,
	}, oc, now)
	if st == nil {
		t.Fatal("expected a token")
	}
	if !st.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("ExpiresAt = %v, want %v", st.ExpiresAt, now.Add(time.Hour))
	}
	if !st.RefreshExpiresAt.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("RefreshExpiresAt = %v, want %v", st.RefreshExpiresAt, now.Add(2*time.Hour))
	}

	// Without a lifetime there is nothing safe to cache.
	if got := newSavedToken(peertube.Token{AccessToken: "a"}, oc, now); got != nil {
		t.Errorf("token without expires_in should not be cached: %+v", got)
	}
	if got := newSavedToken(peertube.Token{ExpiresIn: 3600}, oc, now); got != nil {
		t.Errorf("token without an access token should not be cached: %+v", got)
	}
	// A missing refresh lifetime leaves the refresh unusable but still caches
	// the access token.
	st = newSavedToken(peertube.Token{AccessToken: "a", RefreshToken: "r", ExpiresIn: 3600}, oc, now)
	if st == nil || !st.RefreshExpiresAt.IsZero() || st.refreshUsable(now) {
		t.Errorf("unknown refresh expiry should not be usable: %+v", st)
	}
}

// expireAccess marks only the access token as expired, leaving the refresh
// token live.
func expireAccess(t *testing.T, url string) {
	t.Helper()
	mutateToken(t, url, func(st *savedToken) { st.ExpiresAt = time.Now().Add(-time.Hour) })
}

// mutateToken rewrites the cached token for url through fn.
func mutateToken(t *testing.T, url string, fn func(*savedToken)) {
	t.Helper()
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	inst := cfg.Instances[url]
	if inst.Token == nil {
		t.Fatalf("no cached token for %s", url)
	}
	fn(inst.Token)
	cfg.Instances[url] = inst
	if err := cfg.save(); err != nil {
		t.Fatalf("save config: %v", err)
	}
	// Guard against a silent no-op if the config shape changes.
	if data, err := os.ReadFile(cfgPath(t)); err == nil {
		var check config
		if json.Unmarshal(data, &check) == nil && check.Instances[url].Token == nil {
			t.Fatal("token disappeared from config after mutation")
		}
	}
}

func cfgPath(t *testing.T) string {
	t.Helper()
	p, err := configPathFn()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
