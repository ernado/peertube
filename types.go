package peertube

// Privacy is the visibility of a video (VideoPrivacySet in the API).
type Privacy int

// Video privacy levels.
const (
	PrivacyPublic            Privacy = 1
	PrivacyUnlisted          Privacy = 2
	PrivacyPrivate           Privacy = 3
	PrivacyInternal          Privacy = 4
	PrivacyPasswordProtected Privacy = 5
)

// CommentsPolicy controls who may comment on a video (VideoCommentsPolicySet).
type CommentsPolicy int

// Comment policies.
const (
	CommentsEnabled         CommentsPolicy = 1
	CommentsDisabled        CommentsPolicy = 2
	CommentsRequireApproval CommentsPolicy = 3
)

// UploadParams holds the metadata common to both the legacy and resumable
// uploads. Only Name and ChannelID are required; zero-valued optional fields
// are omitted from the request so the instance defaults apply.
type UploadParams struct {
	// Name is the video title (3..120 chars). Required.
	Name string
	// ChannelID is the channel that will own the video. Required.
	ChannelID int

	// Privacy is the visibility. Zero means "let the server decide".
	Privacy Privacy
	// Category, Licence identifiers as defined by the instance (0 = unset).
	Category int
	Licence  int
	// Language is an ISO 639 code (e.g. "en"); empty = unset.
	Language string

	Description string
	Support     string
	// Tags: up to 5, each 2..30 chars.
	Tags []string

	// Pointer booleans distinguish "unset" from an explicit false.
	NSFW                  *bool
	WaitTranscoding       *bool
	GenerateTranscription *bool
	DownloadEnabled       *bool

	CommentsPolicy CommentsPolicy
	// OriginallyPublishedAt is an RFC 3339 timestamp; empty = unset.
	OriginallyPublishedAt string
}

// UploadedVideo identifies a successfully uploaded video (VideoUploadResponse).
type UploadedVideo struct {
	ID        int    `json:"id"`
	UUID      string `json:"uuid"`
	ShortUUID string `json:"shortUUID"`
}

// videoUploadResponse is the wire shape returned by the upload endpoints.
type videoUploadResponse struct {
	Video UploadedVideo `json:"video"`
}
