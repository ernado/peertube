package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// instance holds saved credentials for a single PeerTube instance.
type instance struct {
	Username string `json:"username"`
	Password string `json:"password"`
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
	}
}

// set records credentials for url, optionally marking it the default.
func (c *config) set(url string, inst instance, makeDefault bool) {
	if c.Instances == nil {
		c.Instances = map[string]instance{}
	}
	c.Instances[url] = inst
	// The first saved instance, or an explicit request, becomes the default.
	if makeDefault || len(c.Instances) == 1 {
		c.Default = url
	}
}
