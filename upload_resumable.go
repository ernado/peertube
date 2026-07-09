package peertube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/go-faster/errors"
)

// DefaultChunkSize is the chunk size used by UploadResumable when none is set.
// It must be a multiple of 1024 (a node-uploadx requirement for non-final
// chunks); 5 MiB is a good default balance of overhead and retry cost.
const DefaultChunkSize = 5 << 20

// ResumableOptions tunes a resumable upload.
type ResumableOptions struct {
	// ChunkSize is the size of each PUT chunk in bytes. It must be a positive
	// multiple of 1024. Zero uses DefaultChunkSize.
	ChunkSize int64
	// ContentType is the video MIME type (e.g. "video/mp4"). When empty it is
	// inferred from the filename extension, defaulting to
	// "application/octet-stream".
	ContentType string
}

// UploadResumable publishes a video using PeerTube's resumable (chunked)
// protocol. Unlike [Client.Upload] it needs the total size up front and sends
// the file in chunks, which lets large uploads survive transient failures.
//
// The total size must be known and video is read sequentially. If a chunk
// fails, the server-side session is canceled (best-effort) so it does not
// linger.
func (c *Client) UploadResumable(
	ctx context.Context,
	params UploadParams,
	filename string,
	video io.Reader,
	size int64,
	opts ...ResumableOptions,
) (*UploadedVideo, error) {
	if err := params.validate(); err != nil {
		return nil, err
	}
	if c.token == "" {
		return nil, errors.New("not authenticated: call Login or WithToken first")
	}
	if filename == "" {
		return nil, errors.New("filename is required")
	}
	if size <= 0 {
		return nil, errors.New("size must be positive")
	}

	var opt ResumableOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	chunkSize := opt.ChunkSize
	if chunkSize == 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize%1024 != 0 {
		return nil, errors.Errorf("chunk size must be a positive multiple of 1024, got %d", chunkSize)
	}
	contentType := opt.ContentType
	if contentType == "" {
		contentType = detectContentType(filename)
	}

	uploadID, err := c.initResumable(ctx, params, filename, size, contentType)
	if err != nil {
		return nil, errors.Wrap(err, "init resumable upload")
	}

	uploaded, err := c.sendChunks(ctx, uploadID, video, size, chunkSize)
	if err != nil {
		// Best-effort cleanup of the dangling session; keep the original error.
		c.cancelResumable(context.WithoutCancel(ctx), uploadID)
		return nil, err
	}
	return uploaded, nil
}

// initResumable performs the POST that opens a resumable session and returns
// its upload id.
func (c *Client) initResumable(ctx context.Context, params UploadParams, filename string, size int64, contentType string) (string, error) {
	body, err := json.Marshal(params.jsonMap(filename))
	if err != nil {
		return "", errors.Wrap(err, "marshal metadata")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("videos/upload-resumable"), bytes.NewReader(body))
	if err != nil {
		return "", errors.Wrap(err, "build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	req.Header.Set("X-Upload-Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	// 201 Created is the normal path; 200 means the server already has the file
	// and expects a resume — for a fresh reader we still need the location.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", newAPIError(resp)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("missing Location header in resumable init response")
	}
	id, err := uploadIDFromLocation(loc)
	if err != nil {
		return "", err
	}
	return id, nil
}

// sendChunks streams the file in chunks and returns the created video once the
// server acknowledges the final chunk with 200.
func (c *Client) sendChunks(ctx context.Context, uploadID string, video io.Reader, size, chunkSize int64) (*UploadedVideo, error) {
	buf := make([]byte, chunkSize)
	var offset int64

	for offset < size {
		n, err := io.ReadFull(video, buf[:min(chunkSize, size-offset)])
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errors.Wrap(err, "read chunk")
		}
		if n == 0 {
			return nil, errors.Errorf("video ended early: read %d of %d bytes", offset, size)
		}
		chunk := buf[:n]
		start := offset
		end := offset + int64(n) - 1

		uploaded, done, err := c.sendChunk(ctx, uploadID, chunk, start, end, size)
		if err != nil {
			return nil, errors.Wrapf(err, "send chunk %d-%d", start, end)
		}
		offset = end + 1
		if done {
			if uploaded == nil {
				return nil, errors.New("server reported completion without a video")
			}
			return uploaded, nil
		}
	}
	return nil, errors.Errorf("upload finished at %d bytes without server confirmation", offset)
}

// sendChunk PUTs a single chunk. It returns the created video and done=true
// when the server responds 200 (last chunk accepted); done=false on 308.
func (c *Client) sendChunk(ctx context.Context, uploadID string, chunk []byte, start, end, total int64) (*UploadedVideo, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.resumableURL(uploadID), bytes.NewReader(chunk))
	if err != nil {
		return nil, false, errors.Wrap(err, "build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	req.ContentLength = int64(len(chunk))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK: // last chunk accepted, video created.
		var out videoUploadResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, false, errors.Wrap(err, "decode response")
		}
		return &out.Video, true, nil
	case statusResumeIncomplete: // 308: more chunks expected.
		return nil, false, nil
	default:
		return nil, false, newAPIError(resp)
	}
}

// cancelResumable deletes an in-progress session (best-effort; errors ignored).
func (c *Client) cancelResumable(ctx context.Context, uploadID string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.resumableURL(uploadID), http.NoBody)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Length", "0")
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// statusResumeIncomplete is HTTP 308 Permanent Redirect, reused by node-uploadx
// to mean "resume incomplete".
const statusResumeIncomplete = 308

// resumableURL returns the chunk endpoint for the given upload id.
func (c *Client) resumableURL(uploadID string) string {
	return c.apiURL("videos/upload-resumable") + "?upload_id=" + url.QueryEscape(uploadID)
}

// uploadIDFromLocation extracts the upload_id query parameter from the Location
// header returned by the init call (which may be a relative URL).
func uploadIDFromLocation(loc string) (string, error) {
	u, err := url.Parse(loc)
	if err != nil {
		return "", errors.Wrap(err, "parse Location")
	}
	id := u.Query().Get("upload_id")
	if id == "" {
		return "", errors.Errorf("no upload_id in Location %q", loc)
	}
	return id, nil
}

// detectContentType infers a video MIME type from the file extension.
func detectContentType(filename string) string {
	if ct := mime.TypeByExtension(path.Ext(filename)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
