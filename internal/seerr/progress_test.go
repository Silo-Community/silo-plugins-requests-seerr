package seerr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"
)

// getFixture serves testdata/name as GET /api/v1/request/{id} and decodes it
// through GetRequest, the path CheckStatus uses.
func getFixture(t *testing.T, name string, id int) *MediaRequest {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/request/"+strconv.Itoa(id) {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	mr, err := GetRequest(context.Background(), httpclient.New(srv.URL, "k", nil), id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	return mr
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func assertProgress(t *testing.T, got *Progress, want Progress) {
	t.Helper()
	if got == nil {
		t.Fatalf("progress: want %+v, got nil", want)
	}
	if got.Phase != want.Phase || got.BytesTotal != want.BytesTotal || got.BytesLeft != want.BytesLeft || got.Downloads != want.Downloads {
		t.Fatalf("progress: want %+v, got %+v", want, *got)
	}
	switch {
	case want.EstimatedCompletion == nil && got.EstimatedCompletion != nil:
		t.Fatalf("eta: want none, got %v", *got.EstimatedCompletion)
	case want.EstimatedCompletion != nil && (got.EstimatedCompletion == nil || !got.EstimatedCompletion.Equal(*want.EstimatedCompletion)):
		t.Fatalf("eta: want %v, got %v", *want.EstimatedCompletion, got.EstimatedCompletion)
	}
}

func TestGetRequestDecodesMovieDownloadStatus(t *testing.T) {
	mr := getFixture(t, "request_movie.json", 12)

	if mr.Is4K || mr.Media.Status != MediaStatusProcessing || mr.Media.Status4K != MediaStatusUnknown {
		t.Fatalf("request: %+v", mr)
	}
	if len(mr.Media.DownloadStatus) != 1 || len(mr.Media.DownloadStatus4K) != 0 {
		t.Fatalf("lists: hd=%d 4k=%d", len(mr.Media.DownloadStatus), len(mr.Media.DownloadStatus4K))
	}
	item := mr.Media.DownloadStatus[0]
	want := DownloadItem{
		Size:                    4294967296,
		SizeLeft:                1073741823.6,
		Status:                  "downloading",
		EstimatedCompletionTime: "2026-09-28T12:34:56.000Z",
		DownloadID:              "SABnzbd_nzo_1a2b3c",
		Title:                   "Example.Movie.2026.1080p.WEB-DL",
	}
	if item != want {
		t.Fatalf("item: want %+v, got %+v", want, item)
	}

	eta := mustTime(t, "2026-09-28T12:34:56Z")
	assertProgress(t, EvaluateProgress(mr.Downloads()), Progress{
		Phase:               PhaseDownloading,
		BytesTotal:          4294967296,
		BytesLeft:           1073741824, // rounded from the decimal
		EstimatedCompletion: &eta,
		Downloads:           1,
	})
}

func TestGetRequest4KUsesDownloadStatus4K(t *testing.T) {
	mr := getFixture(t, "request_movie_4k.json", 13)

	if !mr.Is4K {
		t.Fatalf("want a 4K request")
	}
	downloads := mr.Downloads()
	if len(downloads) != 1 || downloads[0].DownloadID != "8F3C2A1B9D7E6F5A4B3C2D1E0F9A8B7C6D5E4F3A" {
		t.Fatalf("4K request should read downloadStatus4k, got %+v", downloads)
	}
	// The null estimate (a JavaScript Invalid Date) decodes as unknown.
	assertProgress(t, EvaluateProgress(downloads), Progress{
		Phase:      PhasePaused,
		BytesTotal: 64424509440,
		BytesLeft:  48318382080,
		Downloads:  1,
	})
}

func TestEvaluateProgressCountsSeasonPackOnce(t *testing.T) {
	mr := getFixture(t, "request_tv_season_pack.json", 21)

	if len(mr.Downloads()) != 4 {
		t.Fatalf("want 4 queue items (3 pack episodes + 1 single), got %d", len(mr.Downloads()))
	}
	// The pack's three episode items share a downloadId and count once. The
	// single episode's epoch estimate is Seerr's null and does not count.
	eta := mustTime(t, "2026-09-28T13:00:00Z")
	assertProgress(t, EvaluateProgress(mr.Downloads()), Progress{
		Phase:               PhaseStalled,
		BytesTotal:          12000000000 + 1500000000,
		BytesLeft:           3000000000 + 1500000000,
		EstimatedCompletion: &eta,
		Downloads:           2,
	})
}

func TestEvaluateProgressCountsHeldBackSeasonPackOnce(t *testing.T) {
	mr := getFixture(t, "request_tv_held_back_pack.json", 22)

	downloads := mr.Downloads()
	if len(downloads) != 4 {
		t.Fatalf("want 4 queue items (3 held-back pack episodes + 1 single), got %d", len(downloads))
	}
	if downloads[0].DownloadID != "" || downloads[0].Title != "Example.Show.S03.1080p.WEB-DL" {
		t.Fatalf("held-back item: want a title and no downloadId, got %+v", downloads[0])
	}
	// Sonarr has not sent the held-back pack to a download client, so its
	// three episode items carry no downloadId. They share the release's
	// title and size and count once.
	eta := mustTime(t, "2026-09-28T13:00:00Z")
	assertProgress(t, EvaluateProgress(downloads), Progress{
		Phase:               PhaseDownloading,
		BytesTotal:          9000000000 + 1500000000,
		BytesLeft:           9000000000 + 500000000,
		EstimatedCompletion: &eta,
		Downloads:           2,
	})
}

func TestMapDownloadPhase(t *testing.T) {
	cases := map[string]string{
		"paused":                    PhasePaused,
		"warning":                   PhaseStalled,
		"downloading":               PhaseDownloading,
		"Downloading":               PhaseDownloading,
		"completed":                 PhaseImporting,
		"queued":                    PhaseQueued,
		"delay":                     PhaseQueued,
		"downloadClientUnavailable": PhaseQueued,
		"fallback":                  PhaseQueued,
		"failed":                    PhaseQueued,
		"unknown":                   PhaseQueued,
		"":                          PhaseQueued,
	}
	for status, want := range cases {
		if got := MapDownloadPhase(status); got != want {
			t.Errorf("MapDownloadPhase(%q): want %q got %q", status, want, got)
		}
	}
}

func TestEvaluateProgressPhasePrecedence(t *testing.T) {
	cases := []struct {
		statuses []string
		want     string
	}{
		{[]string{"queued"}, PhaseQueued},
		{[]string{"queued", "paused"}, PhasePaused},
		{[]string{"paused", "completed"}, PhaseImporting},
		{[]string{"completed", "downloading"}, PhaseDownloading},
		{[]string{"downloading", "warning", "paused"}, PhaseStalled},
		{[]string{"delay", "fallback"}, PhaseQueued},
	}
	for _, c := range cases {
		items := make([]DownloadItem, len(c.statuses))
		for i, s := range c.statuses {
			items[i] = DownloadItem{Status: s, DownloadID: strconv.Itoa(i)}
		}
		if got := EvaluateProgress(items); got == nil || got.Phase != c.want {
			t.Errorf("%v: want %q got %+v", c.statuses, c.want, got)
		}
	}
}

func TestEvaluateProgressSizesAndETAs(t *testing.T) {
	early := mustTime(t, "2026-09-28T11:00:00Z")
	late := mustTime(t, "2026-09-28T12:00:00Z")
	past := mustTime(t, "2020-01-01T00:00:00Z")

	cases := []struct {
		name  string
		items []DownloadItem
		want  *Progress
	}{
		{name: "empty", items: nil, want: nil},
		{
			name: "latest estimate wins",
			items: []DownloadItem{
				{Status: "downloading", Size: 100, SizeLeft: 40, EstimatedCompletionTime: "2026-09-28T12:00:00.000Z", DownloadID: "a"},
				{Status: "downloading", Size: 50, SizeLeft: 10, EstimatedCompletionTime: "2026-09-28T11:00:00.000Z", DownloadID: "b"},
			},
			want: &Progress{Phase: PhaseDownloading, BytesTotal: 150, BytesLeft: 50, EstimatedCompletion: &late, Downloads: 2},
		},
		{
			name: "past estimate is still an estimate",
			items: []DownloadItem{
				{Status: "completed", Size: 10, EstimatedCompletionTime: "2020-01-01T00:00:00.000Z", DownloadID: "a"},
			},
			want: &Progress{Phase: PhaseImporting, BytesTotal: 10, EstimatedCompletion: &past, Downloads: 1},
		},
		{
			name: "missing, epoch and unparseable estimates are unknown",
			items: []DownloadItem{
				{Status: "queued", DownloadID: "a"},
				{Status: "queued", EstimatedCompletionTime: "1970-01-01T00:00:00.000Z", DownloadID: "b"},
				{Status: "queued", EstimatedCompletionTime: "+275760-09-13T00:00:00.000Z", DownloadID: "c"},
			},
			want: &Progress{Phase: PhaseQueued, Downloads: 3},
		},
		{
			name: "left clamps to size",
			items: []DownloadItem{
				{Status: "downloading", Size: 100, SizeLeft: 250, EstimatedCompletionTime: "2026-09-28T11:00:00.000Z", DownloadID: "b"},
				{Status: "downloading", Size: 100, SizeLeft: -5, DownloadID: "c"},
			},
			want: &Progress{Phase: PhaseDownloading, BytesTotal: 200, BytesLeft: 100, EstimatedCompletion: &early, Downloads: 2},
		},
		{
			// Summing only the known sizes would show 50% for a request that
			// may be far from done, so no download's size is reported. The
			// phase, estimate and download count are unaffected.
			name: "one unknown size zeroes the bytes",
			items: []DownloadItem{
				{Status: "queued", Size: 0, SizeLeft: 0, DownloadID: "a"},
				{Status: "downloading", Size: 100, SizeLeft: 50, EstimatedCompletionTime: "2026-09-28T11:00:00.000Z", DownloadID: "b"},
				{Status: "downloading", Size: 100, SizeLeft: 50, EstimatedCompletionTime: "2026-09-28T11:00:00.000Z", DownloadID: "b"},
			},
			want: &Progress{Phase: PhaseDownloading, EstimatedCompletion: &early, Downloads: 2},
		},
		{
			name: "unknown size listed last",
			items: []DownloadItem{
				{Status: "downloading", Size: 100, SizeLeft: 50, DownloadID: "a"},
				{Status: "queued", SizeLeft: 30, DownloadID: "b"},
			},
			want: &Progress{Phase: PhaseDownloading, Downloads: 2},
		},
		{
			name: "negative or sub-byte size is unknown",
			items: []DownloadItem{
				{Status: "downloading", Size: 100, SizeLeft: 50, DownloadID: "a"},
				{Status: "downloading", Size: -100, SizeLeft: 50, DownloadID: "b"},
				{Status: "downloading", Size: 0.4, SizeLeft: 0.4, DownloadID: "c"},
			},
			want: &Progress{Phase: PhaseDownloading, Downloads: 3},
		},
		{
			name: "items without a downloadId or title count separately",
			items: []DownloadItem{
				{Status: "delay", Size: 30, SizeLeft: 30},
				{Status: "delay", Size: 30, SizeLeft: 30},
			},
			want: &Progress{Phase: PhaseQueued, BytesTotal: 60, BytesLeft: 60, Downloads: 2},
		},
		{
			// Sonarr lists a release it holds back once per episode, each item
			// with the release's title and size and no downloadId.
			name: "held-back season pack counts once",
			items: []DownloadItem{
				{Status: "delay", Size: 5000, SizeLeft: 5000, Title: "Show.S02.1080p"},
				{Status: "delay", Size: 5000, SizeLeft: 5000, Title: "Show.S02.1080p"},
				{Status: "delay", Size: 5000, SizeLeft: 5000, Title: "Show.S02.1080p"},
				{Status: "downloading", Size: 1000, SizeLeft: 400, Title: "Show.S01.1080p", DownloadID: "s01"},
				{Status: "downloading", Size: 1000, SizeLeft: 400, Title: "Show.S01.1080p", DownloadID: "s01"},
			},
			want: &Progress{Phase: PhaseDownloading, BytesTotal: 6000, BytesLeft: 5400, Downloads: 2},
		},
		{
			name: "held-back releases that differ count separately",
			items: []DownloadItem{
				{Status: "delay", Size: 5000, SizeLeft: 5000, Title: "Show.S02.1080p"},
				{Status: "delay", Size: 5000, SizeLeft: 5000, Title: "Show.S03.1080p"},
				{Status: "delay", Size: 6000, SizeLeft: 6000, Title: "Show.S03.1080p"},
			},
			want: &Progress{Phase: PhaseQueued, BytesTotal: 16000, BytesLeft: 16000, Downloads: 3},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EvaluateProgress(c.items)
			if c.want == nil {
				if got != nil {
					t.Fatalf("want nil, got %+v", *got)
				}
				return
			}
			assertProgress(t, got, *c.want)
		})
	}
}
