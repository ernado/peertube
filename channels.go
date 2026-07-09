package peertube

import (
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
