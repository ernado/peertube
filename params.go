package peertube

import (
	"strconv"

	"github.com/go-faster/errors"
)

// validate checks the required fields and simple constraints.
func (p UploadParams) validate() error {
	if l := len(p.Name); l < 3 || l > 120 {
		return errors.Errorf("name must be 3..120 chars, got %d", l)
	}
	if p.ChannelID <= 0 {
		return errors.New("channelID is required and must be positive")
	}
	if len(p.Tags) > 5 {
		return errors.Errorf("at most 5 tags allowed, got %d", len(p.Tags))
	}
	for _, t := range p.Tags {
		if l := len(t); l < 2 || l > 30 {
			return errors.Errorf("tag %q must be 2..30 chars", t)
		}
	}
	return nil
}

// formFields returns the scalar metadata as string form fields, used for the
// legacy multipart upload. Array tags are handled separately by the caller.
func (p UploadParams) formFields() map[string]string {
	f := map[string]string{
		"name":      p.Name,
		"channelId": strconv.Itoa(p.ChannelID),
	}
	if p.Privacy != 0 {
		f["privacy"] = strconv.Itoa(int(p.Privacy))
	}
	if p.Category != 0 {
		f["category"] = strconv.Itoa(p.Category)
	}
	if p.Licence != 0 {
		f["licence"] = strconv.Itoa(p.Licence)
	}
	if p.Language != "" {
		f["language"] = p.Language
	}
	if p.Description != "" {
		f["description"] = p.Description
	}
	if p.Support != "" {
		f["support"] = p.Support
	}
	if p.CommentsPolicy != 0 {
		f["commentsPolicy"] = strconv.Itoa(int(p.CommentsPolicy))
	}
	if p.OriginallyPublishedAt != "" {
		f["originallyPublishedAt"] = p.OriginallyPublishedAt
	}
	if p.NSFW != nil {
		f["nsfw"] = strconv.FormatBool(*p.NSFW)
	}
	if p.WaitTranscoding != nil {
		f["waitTranscoding"] = strconv.FormatBool(*p.WaitTranscoding)
	}
	if p.GenerateTranscription != nil {
		f["generateTranscription"] = strconv.FormatBool(*p.GenerateTranscription)
	}
	if p.DownloadEnabled != nil {
		f["downloadEnabled"] = strconv.FormatBool(*p.DownloadEnabled)
	}
	return f
}

// jsonMap returns the metadata as a JSON-serializable map, used to initialize a
// resumable upload. filename is the required video filename (with extension).
func (p UploadParams) jsonMap(filename string) map[string]any {
	m := map[string]any{
		"name":      p.Name,
		"channelId": p.ChannelID,
		"filename":  filename,
	}
	if p.Privacy != 0 {
		m["privacy"] = int(p.Privacy)
	}
	if p.Category != 0 {
		m["category"] = p.Category
	}
	if p.Licence != 0 {
		m["licence"] = p.Licence
	}
	if p.Language != "" {
		m["language"] = p.Language
	}
	if p.Description != "" {
		m["description"] = p.Description
	}
	if p.Support != "" {
		m["support"] = p.Support
	}
	if p.CommentsPolicy != 0 {
		m["commentsPolicy"] = int(p.CommentsPolicy)
	}
	if p.OriginallyPublishedAt != "" {
		m["originallyPublishedAt"] = p.OriginallyPublishedAt
	}
	if len(p.Tags) > 0 {
		m["tags"] = p.Tags
	}
	if p.NSFW != nil {
		m["nsfw"] = *p.NSFW
	}
	if p.WaitTranscoding != nil {
		m["waitTranscoding"] = *p.WaitTranscoding
	}
	if p.GenerateTranscription != nil {
		m["generateTranscription"] = *p.GenerateTranscription
	}
	if p.DownloadEnabled != nil {
		m["downloadEnabled"] = *p.DownloadEnabled
	}
	return m
}
