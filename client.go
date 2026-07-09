package peertube

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-faster/errors"
)

// Doer executes HTTP requests. *http.Client implements it.
//
// Depending on it (instead of a concrete *http.Client) keeps the client
// testable: tests can supply a stub that returns canned responses without
// touching the network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is a PeerTube API client scoped to a single instance.
//
// A Client is safe for concurrent use as long as the access token is not
// mutated concurrently (Login and SetToken are not synchronized).
type Client struct {
	baseURL *url.URL
	http    Doer
	token   string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying HTTP transport. Use it to inject timeouts,
// custom transports, or a mock in tests. Defaults to http.DefaultClient.
func WithHTTPClient(d Doer) Option {
	return func(c *Client) {
		if d != nil {
			c.http = d
		}
	}
}

// WithToken presets the OAuth2 access token, skipping the need to Login (for
// example when reusing a previously obtained token).
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// NewClient returns a Client for the PeerTube instance at baseURL
// (e.g. "https://peertube.example.org"). The /api/v1 prefix is added
// automatically, so it must not be part of baseURL.
func NewClient(baseURL string, opts ...Option) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("baseURL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.Wrap(err, "parse baseURL")
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.Errorf("baseURL %q must be absolute", baseURL)
	}
	// Normalize: drop any trailing slash so path joining is predictable.
	u.Path = strings.TrimRight(u.Path, "/")

	c := &Client{
		baseURL: u,
		http:    http.DefaultClient,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Token returns the access token currently in use (empty if not authenticated).
func (c *Client) Token() string { return c.token }

// SetToken sets the OAuth2 access token used to authenticate requests.
func (c *Client) SetToken(token string) { c.token = token }

// apiURL builds an absolute URL for the given API path (relative to /api/v1).
func (c *Client) apiURL(elem string) string {
	u := *c.baseURL
	u.Path = u.Path + "/api/v1/" + strings.TrimLeft(elem, "/")
	return u.String()
}
