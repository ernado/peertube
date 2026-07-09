package peertube

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-faster/errors"
)

// Channel is a video channel owned by the authenticated user.
type Channel struct {
	// ID is the numeric channel id used as UploadParams.ChannelID.
	ID int `json:"id"`
	// Name is the channel handle (e.g. "my_channel").
	Name string `json:"name"`
	// DisplayName is the human-friendly channel name.
	DisplayName string `json:"displayName"`
}

// MyChannels returns the video channels owned by the authenticated user
// (GET /api/v1/users/me). It is useful to discover a ChannelID for uploads.
func (c *Client) MyChannels(ctx context.Context) ([]Channel, error) {
	if c.token == "" {
		return nil, errors.New("not authenticated: call Login or WithToken first")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL("users/me"), nil)
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(resp)
	}
	var me struct {
		VideoChannels []Channel `json:"videoChannels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return nil, errors.Wrap(err, "decode response")
	}
	return me.VideoChannels, nil
}

// CreateChannelParams describes a new video channel.
type CreateChannelParams struct {
	// Name is the immutable channel handle (1..50 chars, [a-zA-Z0-9-_.:]).
	// Required.
	Name string
	// DisplayName is the human-friendly name. Required.
	DisplayName string
	// Description and Support are optional free text.
	Description string
	Support     string
}

// CreateChannel creates a video channel owned by the authenticated user
// (POST /api/v1/video-channels) and returns it with the server-assigned ID.
func (c *Client) CreateChannel(ctx context.Context, p CreateChannelParams) (Channel, error) {
	if c.token == "" {
		return Channel{}, errors.New("not authenticated: call Login or WithToken first")
	}
	if l := len(p.Name); l < 1 || l > 50 {
		return Channel{}, errors.Errorf("channel name must be 1..50 chars, got %d", l)
	}
	if p.DisplayName == "" {
		return Channel{}, errors.New("channel displayName is required")
	}

	body := map[string]any{
		"name":        p.Name,
		"displayName": p.DisplayName,
	}
	if p.Description != "" {
		body["description"] = p.Description
	}
	if p.Support != "" {
		body["support"] = p.Support
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Channel{}, errors.Wrap(err, "marshal request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("video-channels"), bytes.NewReader(raw))
	if err != nil {
		return Channel{}, errors.Wrap(err, "build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Channel{}, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Channel{}, newAPIError(resp)
	}
	var out struct {
		VideoChannel struct {
			ID int `json:"id"`
		} `json:"videoChannel"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Channel{}, errors.Wrap(err, "decode response")
	}
	// The create response only returns the id; echo back the requested names.
	return Channel{
		ID:          out.VideoChannel.ID,
		Name:        p.Name,
		DisplayName: p.DisplayName,
	}, nil
}
