package seerr

// Overseerr Permission bit values (server/lib/permissions.ts).
const (
	PermRequest       = 32
	PermAutoApprove   = 128
	PermRequest4K     = 1024
	PermAutoApprove4K = 32768 // AUTO_APPROVE_4K (verified against Overseerr server/lib/permissions.ts)
)

// CreateRequestBody is the POST /api/v1/request body. serverId/profileId/
// rootFolder are intentionally omitted: Seerr default-routes those.
// UserID is set when attributing the request to a mapped Seerr user.
type CreateRequestBody struct {
	MediaType string `json:"mediaType"` // "movie" | "tv"
	MediaID   int    `json:"mediaId"`   // TMDB id
	Is4K      bool   `json:"is4k"`
	Seasons   any    `json:"seasons,omitempty"` // "all" for tv; omitted for movie
	UserID    int    `json:"userId,omitempty"`
}

// MediaInfo is the nested media record on a MediaRequest. Seerr tracks the HD
// and 4K tiers on the one record: Status and DownloadStatus for HD, Status4K
// and DownloadStatus4K for 4K. It attaches the download lists when it loads
// the media, from what its Download Sync job last read from the Radarr/Sonarr
// queues.
type MediaInfo struct {
	Status           int            `json:"status"`   // MediaStatus
	Status4K         int            `json:"status4k"` // MediaStatus
	TMDBID           int            `json:"tmdbId"`
	DownloadStatus   []DownloadItem `json:"downloadStatus"`
	DownloadStatus4K []DownloadItem `json:"downloadStatus4k"`
}

// DownloadItem is one Radarr/Sonarr queue item as Seerr's download tracker
// keeps it (server/lib/downloadtracker.ts DownloadingItem). Sonarr lists a
// season pack once per episode, each item carrying the whole pack's size.
type DownloadItem struct {
	Size     float64 `json:"size"`
	SizeLeft float64 `json:"sizeLeft"`
	Status   string  `json:"status"` // the *arr queue status
	// EstimatedCompletionTime is a JavaScript Date built from the *arr value:
	// null when the queue item omitted it, the Unix epoch when it sent null.
	// Kept as a string so an unparseable value loses the ETA, not the status.
	EstimatedCompletionTime string `json:"estimatedCompletionTime"`
	DownloadID              string `json:"downloadId"`
	// Title is the release title. It only tells apart downloads that have no
	// DownloadID yet, and is never sent to the host.
	Title string `json:"title"`
}

// MediaRequest is the Seerr request object returned by create/get/list.
type MediaRequest struct {
	ID     int       `json:"id"`
	Status int       `json:"status"` // MediaRequestStatus
	Is4K   bool      `json:"is4k"`
	Media  MediaInfo `json:"media"`
}

// MediaStatus returns the media's status for this request's tier. A 4K-only
// request leaves the HD status Unknown, and the HD tier can be available while
// the 4K download is still running.
func (r *MediaRequest) MediaStatus() int {
	if r.Is4K {
		return r.Media.Status4K
	}
	return r.Media.Status
}

// Downloads returns the media's download list for this request's tier.
func (r *MediaRequest) Downloads() []DownloadItem {
	if r.Is4K {
		return r.Media.DownloadStatus4K
	}
	return r.Media.DownloadStatus
}

// requestPage is the GET /api/v1/request list envelope.
type requestPage struct {
	Results []MediaRequest `json:"results"`
}

// User is a Seerr account (GET/POST /api/v1/user).
type User struct {
	ID          int    `json:"id"`
	Email       string `json:"email"`
	Permissions int    `json:"permissions"`
}

type userPage struct {
	Results []User `json:"results"`
}

// MediaRequestStatus values (Overseerr server/constants/media.ts).
const (
	StatusRequestPending   = 1
	StatusRequestApproved  = 2
	StatusRequestDeclined  = 3
	StatusRequestFailed    = 4
	StatusRequestCompleted = 5
)

// MediaStatus values (Overseerr server/constants/media.ts).
const (
	MediaStatusUnknown            = 1
	MediaStatusPending            = 2
	MediaStatusProcessing         = 3
	MediaStatusPartiallyAvailable = 4
	MediaStatusAvailable          = 5
	MediaStatusDeleted            = 6
)

// MapStatus maps a Seerr request's status and its tier's media status
// (MediaRequest.MediaStatus) onto Silo's target status vocabulary. Order
// matters: terminal request failures win, then availability, then in-progress,
// else queued.
func MapStatus(requestStatus, mediaStatus int) string {
	switch {
	case requestStatus == StatusRequestDeclined || requestStatus == StatusRequestFailed:
		return "failed"
	case requestStatus == StatusRequestCompleted || mediaStatus == MediaStatusAvailable:
		return "completed"
	case mediaStatus == MediaStatusProcessing || mediaStatus == MediaStatusPartiallyAvailable:
		return "downloading"
	default:
		return "queued"
	}
}
