package peertube

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// APIError is returned when the PeerTube API responds with a non-2xx status.
//
// PeerTube reports errors either as a plain body or as an RFC 7807
// problem-details document; the fields below are populated best-effort.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Code is the machine-readable error code when present
	// (e.g. "quota_reached", "invalid_grant", "max_file_size_reached").
	Code string
	// Detail is a human-readable message.
	Detail string
	// Body is the raw response body (truncated), for diagnostics.
	Body string
}

// Error implements error.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "peertube: http %d", e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, " (%s)", e.Code)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, ": %s", e.Detail)
	} else if e.Body != "" {
		fmt.Fprintf(&b, ": %s", e.Body)
	}
	return b.String()
}

// problemDetails mirrors the subset of PeerTube's JSON error bodies we use.
// PeerTube uses "code"/"error"/"detail"/"title" across its endpoints.
type problemDetails struct {
	Code   json.RawMessage `json:"code"`
	Error  string          `json:"error"`
	Detail string          `json:"detail"`
	Title  string          `json:"title"`
}

// newAPIError reads resp.Body and builds an APIError. It does not close the body.
func newAPIError(resp *http.Response) *APIError {
	const maxBody = 8 << 10 // 8 KiB is plenty for an error payload.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))

	e := &APIError{
		Status: resp.StatusCode,
		Body:   strings.TrimSpace(string(raw)),
	}

	var pd problemDetails
	if json.Unmarshal(raw, &pd) == nil {
		e.Code = decodeCode(pd.Code)
		switch {
		case pd.Detail != "":
			e.Detail = pd.Detail
		case pd.Error != "":
			e.Detail = pd.Error
		case pd.Title != "":
			e.Detail = pd.Title
		}
	}
	return e
}

// decodeCode normalizes the "code" field, which PeerTube sends either as a
// string ("quota_reached") or a number (legacy numeric codes).
func decodeCode(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}
