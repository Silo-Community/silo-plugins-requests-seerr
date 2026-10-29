package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"
)

func TestCreateRequestSendsBodyAndParsesResponse(t *testing.T) {
	var body CreateRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":55,"status":2,"is4k":true,"media":{"status":3,"tmdbId":42}}`))
	}))
	defer srv.Close()

	mr, err := CreateRequest(context.Background(), httpclient.New(srv.URL, "k", nil), CreateRequestBody{
		MediaType: "tv", MediaID: 42, Is4K: true, Seasons: "all",
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if body.MediaType != "tv" || body.MediaID != 42 || !body.Is4K {
		t.Fatalf("sent body wrong: %+v", body)
	}
	if body.Seasons != "all" {
		t.Fatalf("seasons: want all got %v", body.Seasons)
	}
	if mr.ID != 55 || mr.Status != 2 || mr.Media.Status != 3 {
		t.Fatalf("parsed wrong: %+v", mr)
	}
}

func TestFindExistingRequestFallsBackToRecentRequests(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		detail     string
		noMatching bool
	}{
		{name: "unavailable", status: http.StatusInternalServerError},
		{name: "malformed", detail: `{"mediaInfo":`},
		{name: "missing media", detail: `{}`},
		{name: "missing requests", detail: `{"mediaInfo":{"tmdbId":42,"mediaType":"movie"}}`},
		{name: "wrong tier", detail: `{"mediaInfo":{"tmdbId":42,"mediaType":"movie","requests":[{"id":3,"is4k":false}]}}`},
		{name: "wrong media", detail: `{"mediaInfo":{"tmdbId":99,"mediaType":"tv","requests":[{"id":3,"is4k":true}]}}`},
		{name: "invalid id", detail: `{"mediaInfo":{"tmdbId":42,"mediaType":"movie","requests":[{"id":0,"is4k":true}]}}`},
		{name: "no valid request", detail: `{}`, noMatching: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/movie/42":
					if tc.status != 0 {
						w.WriteHeader(tc.status)
					}
					fmt.Fprint(w, tc.detail)
				case "/api/v1/request":
					if r.URL.Query().Get("take") != "100" || r.URL.Query().Get("sort") != "added" {
						t.Errorf("unexpected fallback query: %s", r.URL.RawQuery)
					}
					results := []MediaRequest{
						{ID: 90, Is4K: true, Media: MediaInfo{TMDBID: 42, MediaType: "tv"}},
						{ID: 91, Is4K: true, Media: MediaInfo{TMDBID: 99, MediaType: "movie"}},
						{ID: 92, Is4K: false, Media: MediaInfo{TMDBID: 42, MediaType: "movie"}},
						{ID: 0, Is4K: true, Media: MediaInfo{TMDBID: 42, MediaType: "movie"}},
					}
					if !tc.noMatching {
						results = append(results, MediaRequest{ID: 2, Status: StatusRequestApproved, Is4K: true,
							Media: MediaInfo{TMDBID: 42, MediaType: "movie"}})
					}
					_ = json.NewEncoder(w).Encode(requestPage{Results: results})
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			mr, err := FindExistingRequest(t.Context(), httpclient.New(srv.URL, "k", nil), "movie", 42, true)
			if tc.noMatching {
				if mr != nil || !errors.Is(err, ErrNotFound) {
					t.Fatalf("want no match, got request=%v err=%v", mr, err)
				}
				return
			}
			if err != nil || mr == nil || mr.ID != 2 {
				t.Fatalf("want matching 4K movie request 2, got request=%v err=%v", mr, err)
			}
		})
	}
}

func TestMapStatus(t *testing.T) {
	cases := []struct {
		req, media int
		want       string
	}{
		{StatusRequestDeclined, MediaStatusAvailable, "failed"},
		{StatusRequestFailed, MediaStatusPending, "failed"},
		{StatusRequestApproved, MediaStatusAvailable, "completed"},
		{StatusRequestCompleted, MediaStatusUnknown, "completed"},
		{StatusRequestApproved, MediaStatusProcessing, "downloading"},
		{StatusRequestApproved, MediaStatusPartiallyAvailable, "downloading"},
		{StatusRequestApproved, MediaStatusPending, "queued"},
		{StatusRequestPending, MediaStatusUnknown, "queued"},
	}
	for _, c := range cases {
		if got := MapStatus(c.req, c.media); got != c.want {
			t.Fatalf("MapStatus(%d,%d): want %q got %q", c.req, c.media, c.want, got)
		}
	}
}

// Seerr keeps each tier's status on the one media record; a request reads its
// own tier's.
func TestMediaStatusReadsTheRequestTier(t *testing.T) {
	media := MediaInfo{Status: MediaStatusAvailable, Status4K: MediaStatusProcessing}
	if got := (&MediaRequest{Media: media}).MediaStatus(); got != MediaStatusAvailable {
		t.Fatalf("HD request: want %d got %d", MediaStatusAvailable, got)
	}
	if got := (&MediaRequest{Is4K: true, Media: media}).MediaStatus(); got != MediaStatusProcessing {
		t.Fatalf("4K request: want %d got %d", MediaStatusProcessing, got)
	}
}
