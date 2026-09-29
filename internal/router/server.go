// Package router implements the request_router.v1 RPCs over a Seerr
// (Overseerr/Jellyseerr) backend. The Server holds no state and stores no
// credentials; every call carries its own connections.
package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Silo-Community/silo-plugins-requests-seerr/internal/seerr"
)

// Server implements the request_router.v1 RPCs. It is stateless.
type Server struct {
	pluginv1.UnimplementedRequestRouterServer
}

// New returns a ready-to-serve request router.
func New() *Server { return &Server{} }

// mediaTypeAndID translates the Silo descriptor into Seerr's mediaType + TMDB
// id. Seerr uses "tv" for series and identifies media by TMDB id only.
func mediaTypeAndID(d *pluginv1.RequestDescriptor) (mediaType string, tmdbID int, err error) {
	mediaType = "movie"
	if d.GetMediaType() == "series" {
		mediaType = "tv"
	}
	raw := d.GetExternalIds()["tmdb"]
	tmdbID, convErr := strconv.Atoi(raw)
	if raw == "" || convErr != nil || tmdbID <= 0 {
		return "", 0, errors.New("request has no TMDB id; Seerr requires a TMDB id")
	}
	return mediaType, tmdbID, nil
}

// userPermissions builds a created Seerr user's Overseerr permission bitfield:
// everyone may request 1080p; REQUEST_4K when the request carries a 4K quality
// (host-decided per the requester's max quality / global force-dual);
// AUTO_APPROVE(+4K) when the connection auto-approves.
func userPermissions(conn Connection, requestHas4K bool) int {
	bits := seerr.PermRequest
	if requestHas4K {
		bits |= seerr.PermRequest4K
	}
	if conn.AutoApprove {
		bits |= seerr.PermAutoApprove
		if requestHas4K {
			bits |= seerr.PermAutoApprove4K
		}
	}
	return bits
}

// resolveRequesterUserID returns the Seerr user id to attribute the request to.
// mapFailed is true only in mapped mode when no user could be resolved/created
// (missing email or a lookup/create error); the caller decides whether that means
// admin fallback (userID 0) or a hard request failure. Resolved once per request
// against the first connection's Seerr.
func resolveRequesterUserID(ctx context.Context, client *httpclient.Client, conn Connection, email string, requestHas4K bool) (userID int, mapFailed bool) {
	if !conn.Mapped {
		return 0, false
	}
	if strings.TrimSpace(email) == "" {
		return 0, true
	}
	user, err := seerr.FindUserByEmail(ctx, client, email)
	if err != nil {
		log.Printf("seerr: user lookup failed for %q: %v", email, err)
		return 0, true
	}
	if user != nil {
		return user.ID, false
	}
	created, err := seerr.CreateUser(ctx, client, email, userPermissions(conn, requestHas4K))
	if err != nil {
		log.Printf("seerr: user create failed for %q: %v", email, err)
		return 0, true
	}
	return created.ID, false
}

// Fulfill submits one Seerr request per requested quality (is4k from the tier),
// returning one FulfillmentTarget each. Per-target containment: one failing
// quality/connection never aborts the others.
func (s *Server) Fulfill(ctx context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error) {
	d := req.GetRequest()
	mediaType, tmdbID, idErr := mediaTypeAndID(d)
	if idErr != nil {
		// One request-level failure, not N identical per-target failures.
		return &pluginv1.FulfillResponse{Message: idErr.Error()}, nil
	}

	requestHas4K := false
	for _, q := range req.GetQualities() {
		if q.GetIs4K() {
			requestHas4K = true
			break
		}
	}

	userID := 0
	if len(req.GetConnections()) > 0 {
		first := connectionFromRouter(req.GetConnections()[0])
		fc := httpclient.New(first.BaseURL, first.APIKey, nil)
		id, mapFailed := resolveRequesterUserID(ctx, fc, first, d.GetRequesterEmail(), requestHas4K)
		if mapFailed && first.RequireMappedUser {
			return &pluginv1.FulfillResponse{Message: fmt.Sprintf("could not map requester %q to a Seerr user", d.GetRequesterEmail())}, nil
		}
		userID = id
	}

	var targets []*pluginv1.FulfillmentTarget
	for _, c := range req.GetConnections() {
		conn := connectionFromRouter(c)
		client := httpclient.New(conn.BaseURL, conn.APIKey, nil)
		for _, q := range req.GetQualities() {
			is4k := q.GetIs4K()
			if is4k && !conn.Supports4K {
				continue // this connection does not fulfill 4K
			}
			targets = append(targets, s.fulfillOne(ctx, client, conn.ID, q.GetId(), mediaType, tmdbID, is4k, userID))
		}
	}
	if len(targets) == 0 {
		return &pluginv1.FulfillResponse{Message: "no Seerr connection fulfills the requested quality"}, nil
	}
	return &pluginv1.FulfillResponse{Targets: targets}, nil
}

// fulfillOne submits a single (connection, quality) target.
func (s *Server) fulfillOne(ctx context.Context, client *httpclient.Client, connID, quality, mediaType string, tmdbID int, is4k bool, userID int) *pluginv1.FulfillmentTarget {
	t := &pluginv1.FulfillmentTarget{Quality: quality, ConnectionId: connID}
	body := seerr.CreateRequestBody{MediaType: mediaType, MediaID: tmdbID, Is4K: is4k}
	if mediaType == "tv" {
		body.Seasons = "all"
	}
	if userID > 0 {
		body.UserID = userID
	}

	created, err := seerr.CreateRequest(ctx, client, body)
	var se *httpclient.StatusError
	switch {
	case err == nil && created.ID > 0:
		t.Status = "queued"
		t.ExternalId = itoa(created.ID)
		t.ExternalStatus = itoa(created.MediaStatus())
	case err == nil:
		// 2xx but no usable id (empty body): recover the id from the list,
		// the same way the duplicate path does. Never emit a queued target
		// with a zero external id.
		recoverExisting(ctx, client, t, tmdbID, is4k, false)
	case errors.As(err, &se) && se.StatusCode == http.StatusConflict:
		recoverExisting(ctx, client, t, tmdbID, is4k, true)
	default:
		t.Status = "failed"
		t.Message = err.Error()
	}
	return t
}

// recoverExisting resolves an existing Seerr request id by scanning the recent
// request list, used when a create returned no usable id (empty body) or 409
// duplicate. duplicate selects the wording. On no match the target fails with a
// clear message rather than queuing with a zero external id.
func recoverExisting(ctx context.Context, client *httpclient.Client, t *pluginv1.FulfillmentTarget, tmdbID int, is4k, duplicate bool) {
	if found, ferr := seerr.FindExistingRequest(ctx, client, tmdbID, is4k); ferr == nil && found != nil {
		t.Status = "queued"
		t.ExternalId = itoa(found.ID)
		t.ExternalStatus = itoa(found.MediaStatus())
		if duplicate {
			t.Message = "already requested in Seerr"
		} else {
			t.Message = "created in Seerr"
		}
		return
	}
	t.Status = "failed"
	if duplicate {
		t.Message = "already requested in Seerr but its request id could not be resolved"
	} else {
		t.Message = "request created in Seerr but its id could not be resolved"
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// CheckStatus probes each target's Seerr request id and maps the status back.
// Queued and downloading targets also carry the download progress Seerr last
// read from its Radarr/Sonarr queue. Targets whose connection is missing, or
// whose probe errors, are skipped so one unreachable connection does not blank
// the whole response.
func (s *Server) CheckStatus(ctx context.Context, req *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error) {
	// Build one Seerr client per connection up front; the target loop reuses it.
	byID := make(map[string]*httpclient.Client, len(req.GetConnections()))
	for _, c := range req.GetConnections() {
		conn := connectionFromRouter(c)
		byID[conn.ID] = httpclient.New(conn.BaseURL, conn.APIKey, nil)
	}

	var statuses []*pluginv1.TargetStatus
	for _, tref := range req.GetTargets() {
		client, ok := byID[tref.GetConnectionId()]
		if !ok {
			continue
		}
		id, err := strconv.Atoi(tref.GetExternalId())
		if err != nil || id <= 0 {
			continue
		}
		mr, err := seerr.GetRequest(ctx, client, id)
		if err != nil {
			var se *httpclient.StatusError
			if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
				// Deleted in Seerr: 404s forever. Map to a terminal failed
				// status so the target stops sticking queued.
				statuses = append(statuses, &pluginv1.TargetStatus{
					Quality:      tref.GetQuality(),
					ConnectionId: tref.GetConnectionId(),
					Status:       "failed",
					Message:      "request no longer present in Seerr",
				})
			}
			continue // transient errors: skip, retry next cycle
		}
		status := seerr.MapStatus(mr.Status, mr.MediaStatus())
		st := &pluginv1.TargetStatus{
			Quality:        tref.GetQuality(),
			ConnectionId:   tref.GetConnectionId(),
			Status:         status,
			ExternalStatus: itoa(mr.MediaStatus()),
		}
		if status == "queued" || status == "downloading" {
			st.Progress = downloadProgress(seerr.EvaluateProgress(mr.Downloads()))
		}
		statuses = append(statuses, st)
	}
	return &pluginv1.CheckStatusResponse{Statuses: statuses}, nil
}

// downloadProgress converts the aggregate to the wire message. Nil stays nil,
// which tells the host nothing is in flight.
func downloadProgress(p *seerr.Progress) *pluginv1.DownloadProgress {
	if p == nil {
		return nil
	}
	out := &pluginv1.DownloadProgress{
		Phase:      p.Phase,
		BytesTotal: p.BytesTotal,
		BytesLeft:  p.BytesLeft,
		Downloads:  int32(p.Downloads),
	}
	if p.EstimatedCompletion != nil {
		out.EstimatedCompletion = timestamppb.New(*p.EstimatedCompletion)
	}
	return out
}

// TestConnection verifies the base URL + API key by calling /auth/me. Never
// returns a gRPC error; failure is Ok:false + message.
func (s *Server) TestConnection(ctx context.Context, req *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error) {
	conn := connectionFromRouter(req.GetConnection())
	if err := seerr.Me(ctx, httpclient.New(conn.BaseURL, conn.APIKey, nil)); err != nil {
		return &pluginv1.TestConnectionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pluginv1.TestConnectionResponse{Ok: true, Message: "connection successful"}, nil
}

// ListConfigOptions returns no dynamic options: the Seerr connection config has
// no dynamic-options fields. Returned empty (not Unimplemented) so the host's
// options probe gets a clean answer.
func (s *Server) ListConfigOptions(ctx context.Context, req *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error) {
	return &pluginv1.ListConfigOptionsResponse{}, nil
}

// Validate has no cross-field rules to check (the only config field is a
// boolean). Returned empty so the host's save-time Validate succeeds cleanly.
func (s *Server) Validate(ctx context.Context, req *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error) {
	return &pluginv1.ValidateResponse{}, nil
}
