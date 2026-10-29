package seerr

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"
)

// CreateRequest submits a new media request. A 409 surfaces as a
// *httpclient.StatusError{StatusCode: 409}; an empty 2xx body yields a
// zero-value MediaRequest (ID 0) with nil error.
func CreateRequest(ctx context.Context, c *httpclient.Client, body CreateRequestBody) (*MediaRequest, error) {
	var out MediaRequest
	if err := c.PostJSON(ctx, "/api/v1/request", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRequest fetches a single request by its Seerr id.
func GetRequest(ctx context.Context, c *httpclient.Client, id int) (*MediaRequest, error) {
	var out MediaRequest
	if err := c.GetJSON(ctx, fmt.Sprintf("/api/v1/request/%d", id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindExistingRequest looks up the title's requests, regardless of their age.
// If the detail endpoint fails or has no match, fall back to the recent list.
// An explicit server ID requires a matching stored selection; a legacy default
// cannot establish which server the request was sent to.
func FindExistingRequest(ctx context.Context, c *httpclient.Client, mediaType string, tmdbID int, is4k bool, serverID *int) (*MediaRequest, error) {
	var details struct {
		MediaInfo struct {
			MediaInfo
			Requests []MediaRequest `json:"requests"`
		} `json:"mediaInfo"`
	}
	if err := c.GetJSON(ctx, fmt.Sprintf("/api/v1/%s/%d", mediaType, tmdbID), &details); err == nil {
		// Nested requests can omit their media back-reference. The enclosing
		// record supplies the identity and the status of each quality tier.
		for i := range details.MediaInfo.Requests {
			details.MediaInfo.Requests[i].Media = details.MediaInfo.MediaInfo
		}
		if found := selectExistingRequest(details.MediaInfo.Requests, mediaType, tmdbID, is4k, serverID); found != nil {
			return found, nil
		}
	}

	var page requestPage
	if err := c.GetJSON(ctx, "/api/v1/request?take=100&sort=added", &page); err != nil {
		return nil, err
	}
	if found := selectExistingRequest(page.Results, mediaType, tmdbID, is4k, serverID); found != nil {
		return found, nil
	}
	return nil, ErrNotFound
}

func selectExistingRequest(requests []MediaRequest, mediaType string, tmdbID int, is4k bool, serverID *int) *MediaRequest {
	var best *MediaRequest
	for i := range requests {
		r := &requests[i]
		if r.ID <= 0 || r.Media.TMDBID != tmdbID || r.Media.MediaType != mediaType || r.Is4K != is4k {
			continue
		}
		if serverID != nil && (r.ServerID == nil || *r.ServerID != *serverID) {
			continue
		}
		if best == nil || requestPriority(r.Status) > requestPriority(best.Status) ||
			(requestPriority(r.Status) == requestPriority(best.Status) && r.ID > best.ID) {
			best = r
		}
	}
	return best
}

// Prefer active requests over terminal ones, then the newest ID within a state.
func requestPriority(status int) int {
	switch status {
	case StatusRequestPending, StatusRequestApproved:
		return 3
	case StatusRequestCompleted:
		return 2
	case StatusRequestFailed:
		return 1
	default:
		return 0
	}
}

// Me validates the base URL + API key by calling the authenticated /auth/me and
// confirming it returns a real user (a 200 from a login wall / proxy with a
// non-user body must NOT pass).
func Me(ctx context.Context, c *httpclient.Client) error {
	var me struct {
		ID int `json:"id"`
	}
	if err := c.GetJSON(ctx, "/api/v1/auth/me", &me); err != nil {
		return err
	}
	if me.ID <= 0 {
		return fmt.Errorf("seerr: /auth/me did not return an authenticated user")
	}
	return nil
}
