package peertube

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-faster/errors"
)

// VideoSize returns the total storage occupied by a video in bytes, summing
// every stored file: the plain webtorrent files and the files of each HLS
// streaming playlist. Instances with HLS-only transcoding report no plain
// files at all, so both sources must be counted.
//
// The listing endpoints do not carry file sizes, so this costs one request per
// video (GET /videos/{id}).
func (c *Client) VideoSize(ctx context.Context, id int) (int64, error) {
	endpoint := "videos/" + strconv.Itoa(id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(endpoint), http.NoBody)
	if err != nil {
		return 0, errors.Wrap(err, "build request")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, newAPIError(resp)
	}

	type videoFile struct {
		Size int64 `json:"size"`
	}
	var out struct {
		Files              []videoFile `json:"files"`
		StreamingPlaylists []struct {
			Files []videoFile `json:"files"`
		} `json:"streamingPlaylists"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, errors.Wrap(err, "decode response")
	}

	var total int64
	for _, f := range out.Files {
		total += f.Size
	}
	for _, p := range out.StreamingPlaylists {
		for _, f := range p.Files {
			total += f.Size
		}
	}
	return total, nil
}

// SizedVideo is a video annotated with its owning channel and storage size, as
// consumed by SelectToFit.
type SizedVideo struct {
	Video

	// Channel is the handle of the channel the video belongs to.
	Channel string
	// Size is the video's storage footprint in bytes.
	Size int64
}

// TotalSize sums the sizes of videos.
func TotalSize(videos []SizedVideo) int64 {
	var total int64
	for i := range videos {
		total += videos[i].Size
	}
	return total
}

// SelectToFit picks videos to delete so that the total storage occupied by the
// remainder drops to targetBytes or below, balancing channels against each
// other: at each step it takes the oldest video from whichever channel
// currently occupies the most bytes.
//
// The effect is that large channels are trimmed first and small ones are left
// untouched until the channels are comparable in size, rather than any single
// channel being emptied. Within a channel, the oldest videos go first.
//
// keepPerChannel, when positive, protects the newest N videos of every channel
// from deletion; this can leave the result above targetBytes, which the caller
// should report. Videos are returned in deletion order. A nil result means
// nothing needs to be deleted.
func SelectToFit(videos []SizedVideo, targetBytes int64, keepPerChannel int) []SizedVideo {
	total := TotalSize(videos)
	if total <= targetBytes {
		return nil
	}

	// Group by channel, oldest first, dropping the newest keepPerChannel of
	// each channel from consideration.
	type channelState struct {
		handle    string
		remaining int64        // bytes still held by this channel, including protected videos
		queue     []SizedVideo // deletable, oldest first
	}
	byHandle := make(map[string]*channelState)
	var order []string // stable iteration: first-seen channel order
	for i := range videos {
		v := videos[i]
		st, ok := byHandle[v.Channel]
		if !ok {
			st = &channelState{handle: v.Channel}
			byHandle[v.Channel] = st
			order = append(order, v.Channel)
		}
		st.remaining += v.Size
		st.queue = append(st.queue, v)
	}
	for _, st := range byHandle {
		// Newest first, so the protected prefix is the newest keepPerChannel...
		sort.SliceStable(st.queue, func(i, j int) bool {
			return st.queue[i].PublishedAt.After(st.queue[j].PublishedAt)
		})
		if keepPerChannel > 0 {
			if keepPerChannel >= len(st.queue) {
				st.queue = nil
				continue
			}
			st.queue = st.queue[keepPerChannel:]
		}
		// ...then reverse to oldest-first deletion order.
		for i, j := 0, len(st.queue)-1; i < j; i, j = i+1, j-1 {
			st.queue[i], st.queue[j] = st.queue[j], st.queue[i]
		}
	}

	var selected []SizedVideo
	for total > targetBytes {
		// Pick the channel holding the most bytes that still has a deletable
		// video. Ties break on first-seen channel order for determinism.
		var victim *channelState
		for _, h := range order {
			st := byHandle[h]
			if len(st.queue) == 0 {
				continue
			}
			if victim == nil || st.remaining > victim.remaining {
				victim = st
			}
		}
		if victim == nil {
			break // everything left is protected; caller reports the shortfall
		}
		v := victim.queue[0]
		victim.queue = victim.queue[1:]
		victim.remaining -= v.Size
		total -= v.Size
		selected = append(selected, v)
	}
	return selected
}
