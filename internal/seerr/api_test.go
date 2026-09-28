package seerr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestFindExistingRequestMatchesTMDBAnd4K(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"results":[
			{"id":1,"is4k":false,"media":{"tmdbId":42}},
			{"id":2,"is4k":true,"media":{"tmdbId":42}}
		]}`))
	}))
	defer srv.Close()
	c := httpclient.New(srv.URL, "k", nil)

	mr, err := FindExistingRequest(context.Background(), c, 42, true)
	if err != nil {
		t.Fatalf("FindExistingRequest: %v", err)
	}
	if mr.ID != 2 {
		t.Fatalf("want id 2 (the 4k match), got %d", mr.ID)
	}
	if !strings.Contains(gotQuery, "sort=added") {
		t.Fatalf("want query to pin sort=added, got %q", gotQuery)
	}
	if _, err := FindExistingRequest(context.Background(), c, 999, false); err != ErrNotFound {
		t.Fatalf("want ErrNotFound for unknown tmdb, got %v", err)
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
