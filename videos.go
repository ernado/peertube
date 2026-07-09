package peertube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/go-faster/errors"
)

// Video is a video item returned when listing a channel's videos.
type Video struct {
	ID          int       `json:"id"`
	UUID        string    `json:"uuid"`
	ShortUUID   string    `json:"shortUUID"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"publishedAt"`
	CreatedAt   time.Time `json:"createdAt"`
	IsLive      bool      `json:"isLive"`
}

// channelVideosPageSize is the API's maximum page size for video listings.
const channelVideosPageSize = 100

// ChannelVideos returns all videos in the channel identified by handle, newest
// first, paginating through the API. If the client is authenticated the listing
// reflects what that account may see.
func (c *Client) ChannelVideos(ctx context.Context, handle string) ([]Video, error) {
	if handle == "" {
		return nil, errors.New("channel handle is required")
	}
	var all []Video
	for start := 0; ; start += channelVideosPageSize {
		page, total, err := c.channelVideosPage(ctx, handle, start, channelVideosPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) == 0 || len(all) >= total {
			break
		}
	}
	return all, nil
}

func (c *Client) channelVideosPage(ctx context.Context, handle string, start, count int) ([]Video, int, error) {
	q := url.Values{
		"start": {strconv.Itoa(start)},
		"count": {strconv.Itoa(count)},
		"sort":  {"-publishedAt"},
	}
	endpoint := c.apiURL("video-channels/"+url.PathEscape(handle)+"/videos") + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, 0, errors.Wrap(err, "build request")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, newAPIError(resp)
	}
	var out struct {
		Total int     `json:"total"`
		Data  []Video `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, errors.Wrap(err, "decode response")
	}
	return out.Data, out.Total, nil
}

// DeleteVideo deletes a video by its numeric id (DELETE /videos/{id}).
func (c *Client) DeleteVideo(ctx context.Context, id int) error {
	if c.token == "" {
		return errors.New("not authenticated: call Login or WithToken first")
	}
	endpoint := "videos/" + strconv.Itoa(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.apiURL(endpoint), http.NoBody)
	if err != nil {
		return errors.Wrap(err, "build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return newAPIError(resp)
	}
	return nil
}

// PruneOptions selects which of a channel's videos to prune (delete).
type PruneOptions struct {
	// OlderThan prunes videos published before now-OlderThan. Zero disables the
	// age filter.
	OlderThan time.Duration
	// KeepLast always keeps the N most recently published videos. Zero disables
	// the count limit.
	KeepLast int
}

// SelectPrunable returns the videos to prune given opts, evaluated against now.
//
// The N most recently published videos (KeepLast) are always kept. Among the
// rest, a video is selected when it was published before now-OlderThan; if
// OlderThan is zero, all non-kept videos are selected. Videos are matched by
// publishedAt. With both options zero, every video is selected, so callers
// should require at least one criterion.
func SelectPrunable(videos []Video, opts PruneOptions, now time.Time) []Video {
	sorted := make([]Video, len(videos))
	copy(sorted, videos)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].PublishedAt.After(sorted[j].PublishedAt)
	})

	cutoff := now.Add(-opts.OlderThan)
	var prune []Video
	for i, v := range sorted {
		if opts.KeepLast > 0 && i < opts.KeepLast {
			continue // protected: among the newest KeepLast
		}
		if opts.OlderThan > 0 && !v.PublishedAt.Before(cutoff) {
			continue // not old enough
		}
		prune = append(prune, v)
	}
	return prune
}
