package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ernado/peertube"
)

// instance holds saved credentials for a single PeerTube instance.
type instance struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// Token is the cached OAuth2 token pair, reused across commands so every
	// invocation does not have to re-run the password grant.
	Token *savedToken `json:"token,omitempty"`
}

// savedToken is a persisted OAuth2 token pair plus the local OAuth client it
// was issued for, which is required to redeem the refresh token later.
type savedToken struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitzero"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
	ClientID         string    `json:"client_id,omitempty"`
	ClientSecret     string    `json:"client_secret,omitempty"`
}

// tokenSkew is how long before the stated expiry a token is already treated as
// expired, so a request is not issued with a token that dies in flight.
const tokenSkew = time.Minute

// accessUsable reports whether the access token can still be used at now.
// A token with no known expiry is not trusted: it would be retried forever.
func (t *savedToken) accessUsable(now time.Time) bool {
	return t != nil && t.AccessToken != "" && !t.ExpiresAt.IsZero() &&
		now.Add(tokenSkew).Before(t.ExpiresAt)
}

// refreshUsable reports whether the refresh token can be redeemed at now. The
// OAuth client id/secret are part of the grant, so they must be known too.
func (t *savedToken) refreshUsable(now time.Time) bool {
	return t != nil && t.RefreshToken != "" && t.ClientID != "" && t.ClientSecret != "" &&
		!t.RefreshExpiresAt.IsZero() && now.Add(tokenSkew).Before(t.RefreshExpiresAt)
}

// config is the persisted CLI state: known instances and the default one.
type config struct {
	Default   string              `json:"default,omitempty"`
	Instances map[string]instance `json:"instances,omitempty"`
}

// configPathFn resolves the config file location; overridable in tests.
var configPathFn = defaultConfigPath

func defaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "peertube", "config.json"), nil
}

// loadConfig reads the config file. A missing file yields an empty config.
func loadConfig() (*config, error) {
	path, err := configPathFn()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is the app's own config location, not user input
	if os.IsNotExist(err) {
		return &config{Instances: map[string]instance{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if c.Instances == nil {
		c.Instances = map[string]instance{}
	}
	return &c, nil
}

// save writes the config with owner-only permissions (it holds passwords).
func (c *config) save() error {
	path, err := configPathFn()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// resolveCredentials fills unset auth fields from, in order of precedence:
// command-line flags (already present), environment variables, then the saved
// config (its default instance when --url is omitted). Config errors are
// ignored here so a corrupt file never blocks explicit flags.
func (o *options) resolveCredentials() {
	if o.username == "" {
		o.username = os.Getenv("PEERTUBE_USER")
	}
	if o.password == "" {
		o.password = os.Getenv("PEERTUBE_PASSWORD")
	}

	cfg, err := loadConfig()
	if err != nil {
		return
	}
	if o.url == "" {
		o.url = cfg.Default
	}
	if inst, ok := cfg.Instances[o.url]; ok {
		if o.username == "" {
			o.username = inst.Username
		}
		if o.password == "" {
			o.password = inst.Password
		}
		o.token = inst.Token
	}
}

// newSavedToken converts a freshly issued token pair into its persisted form,
// resolving the relative expiries against now. It returns nil when the token
// carries no usable lifetime, so an un-cacheable token is simply not stored.
func newSavedToken(tok peertube.Token, oc peertube.OAuthClient, now time.Time) *savedToken {
	if tok.AccessToken == "" || tok.ExpiresIn <= 0 {
		return nil
	}
	st := &savedToken{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(tok.ExpiresIn) * time.Second),
		ClientID:     oc.ClientID,
		ClientSecret: oc.ClientSecret,
	}
	if tok.RefreshTokenExpiresIn > 0 {
		st.RefreshExpiresAt = now.Add(time.Duration(tok.RefreshTokenExpiresIn) * time.Second)
	}
	return st
}

// storeToken caches tok for url, leaving the rest of the instance untouched.
// It is best-effort: a config that cannot be written only costs a re-login, so
// the error is returned for logging rather than failing the command.
func storeToken(url string, tok *savedToken) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	inst := cfg.Instances[url]
	inst.Token = tok
	if cfg.Instances == nil {
		cfg.Instances = map[string]instance{}
	}
	cfg.Instances[url] = inst
	return cfg.save()
}

// set records credentials for url, optionally marking it the default.
func (c *config) set(url string, inst instance, makeDefault bool) {
	if c.Instances == nil {
		c.Instances = map[string]instance{}
	}
	// Keep any cached token: this call carries credentials, not tokens, and
	// dropping it would force the next command to log in again.
	if inst.Token == nil {
		inst.Token = c.Instances[url].Token
	}
	c.Instances[url] = inst
	// The first saved instance, or an explicit request, becomes the default.
	if makeDefault || len(c.Instances) == 1 {
		c.Default = url
	}
}
