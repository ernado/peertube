package peertube

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/go-faster/errors"
)

// Upload publishes a video in a single multipart request
// (POST /api/v1/videos/upload).
//
// It is the simplest path and is well suited to small/medium files. For large
// files or unreliable networks prefer [Client.UploadResumable], which can
// resume after an interruption.
//
// filename is the original file name (used to derive the content type on the
// server); video streams the file contents. The body is streamed, not buffered,
// so memory use stays constant regardless of file size.
func (c *Client) Upload(ctx context.Context, params UploadParams, filename string, video io.Reader) (*UploadedVideo, error) {
	if err := params.validate(); err != nil {
		return nil, err
	}
	if c.token == "" {
		return nil, errors.New("not authenticated: call Login or WithToken first")
	}
	if filename == "" {
		return nil, errors.New("filename is required")
	}

	// Stream the multipart body through a pipe so the file is never fully
	// held in memory.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		err := writeMultipart(mw, params, filename, video)
		// Closing the writer flushes the trailing boundary before we signal EOF.
		if cerr := mw.Close(); err == nil {
			err = cerr
		}
		_ = pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("videos/upload"), pr)
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "do request")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(resp)
	}
	var out videoUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, errors.Wrap(err, "decode response")
	}
	return &out.Video, nil
}

// writeMultipart writes metadata fields, tags and the video file part.
func writeMultipart(mw *multipart.Writer, params UploadParams, filename string, video io.Reader) error {
	for k, v := range params.formFields() {
		if err := mw.WriteField(k, v); err != nil {
			return errors.Wrapf(err, "write field %q", k)
		}
	}
	// PeerTube expects array fields as repeated "tags[]" entries.
	for _, tag := range params.Tags {
		if err := mw.WriteField("tags[]", tag); err != nil {
			return errors.Wrap(err, "write tag")
		}
	}
	part, err := mw.CreateFormFile("videofile", filename)
	if err != nil {
		return errors.Wrap(err, "create videofile part")
	}
	if _, err := io.Copy(part, video); err != nil {
		return errors.Wrap(err, "copy video")
	}
	return nil
}
