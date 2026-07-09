package peertube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-faster/errors"
)

// OAuth2 form field names and grant types.
const (
	paramClientID     = "client_id"
	paramClientSecret = "client_secret"
	paramGrantType    = "grant_type"
	paramUsername     = "username"
	paramPassword     = "password"
	paramRefreshToken = "refresh_token"

	grantPassword = "password"
	grantRefresh  = "refresh_token"
)

// OAuthClient holds the local OAuth client credentials required before login.
type OAuthClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// Token is an OAuth2 token pair returned by Login.
type Token struct {
	TokenType             string `json:"token_type"`
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
}

// OAuthClient fetches the instance's local OAuth client id/secret
// (GET /oauth-clients/local). Login calls this automatically; it is exported
// for callers who cache credentials.
func (c *Client) OAuthClient(ctx context.Context) (OAuthClient, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL("oauth-clients/local"), http.NoBody)
	if err != nil {
		return OAuthClient{}, errors.Wrap(err, "build request")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return OAuthClient{}, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return OAuthClient{}, newAPIError(resp)
	}
	var oc OAuthClient
	if err := json.NewDecoder(resp.Body).Decode(&oc); err != nil {
		return OAuthClient{}, errors.Wrap(err, "decode response")
	}
	if oc.ClientID == "" || oc.ClientSecret == "" {
		return OAuthClient{}, errors.New("empty oauth client credentials")
	}
	return oc, nil
}

// LoginOptions tunes the login request.
type LoginOptions struct {
	// OTP is the two-factor code, required when the account has 2FA enabled.
	OTP string
	// Client, when set, is used instead of fetching /oauth-clients/local.
	Client *OAuthClient
}

// Login performs the OAuth2 password grant and, on success, stores the access
// token on the client so subsequent uploads are authenticated. The obtained
// token is also returned so callers may persist the refresh token.
func (c *Client) Login(ctx context.Context, username, password string, opts ...LoginOptions) (Token, error) {
	var opt LoginOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	oc := opt.Client
	if oc == nil {
		fetched, err := c.OAuthClient(ctx)
		if err != nil {
			return Token{}, errors.Wrap(err, "get oauth client")
		}
		oc = &fetched
	}

	form := url.Values{
		paramClientID:     {oc.ClientID},
		paramClientSecret: {oc.ClientSecret},
		paramGrantType:    {grantPassword},
		paramUsername:     {username},
		paramPassword:     {password},
	}

	tok, err := c.token2FA(ctx, form, opt.OTP)
	if err != nil {
		return Token{}, err
	}
	c.token = tok.AccessToken
	return tok, nil
}

// Refresh exchanges a refresh token for a new token pair and stores the new
// access token on the client.
func (c *Client) Refresh(ctx context.Context, client OAuthClient, refreshToken string) (Token, error) {
	form := url.Values{
		paramClientID:     {client.ClientID},
		paramClientSecret: {client.ClientSecret},
		paramGrantType:    {grantRefresh},
		paramRefreshToken: {refreshToken},
	}
	tok, err := c.token2FA(ctx, form, "")
	if err != nil {
		return Token{}, err
	}
	c.token = tok.AccessToken
	return tok, nil
}

// token2FA posts the token form and decodes the response.
func (c *Client) token2FA(ctx context.Context, form url.Values, otp string) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("users/token"), strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, errors.Wrap(err, "build request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Length", strconv.Itoa(len(form.Encode())))
	if otp != "" {
		req.Header.Set("x-peertube-otp", otp)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Token{}, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Token{}, newAPIError(resp)
	}
	var tok Token
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return Token{}, errors.Wrap(err, "decode response")
	}
	if tok.AccessToken == "" {
		return Token{}, errors.New("empty access token in response")
	}
	return tok, nil
}
