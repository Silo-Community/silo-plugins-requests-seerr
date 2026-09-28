package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Community/silo-plugins-requests-seerr/internal/seerr"
)

// seerrStub records POST bodies and serves canned create responses.
type seerrStub struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (s *seerrStub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/request" && r.Method == http.MethodPost {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			s.mu.Lock()
			s.bodies = append(s.bodies, b)
			n := len(s.bodies)
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			// distinct ids so we can tell HD/4K targets apart
			_, _ = w.Write([]byte(`{"id":` + itoa(100+n) + `,"status":2,"media":{"status":2}}`))
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})
}

func conn(t *testing.T, id, baseURL string, supports4k bool) *pluginv1.RouterConnection {
	cfg, err := structpb.NewStruct(map[string]any{"supports_4k": supports4k})
	if err != nil {
		t.Fatalf("structpb: %v", err)
	}
	return &pluginv1.RouterConnection{Id: id, BaseUrl: baseURL, ApiKey: "k", Config: cfg}
}

func TestFulfillHDOnly(t *testing.T) {
	stub := &seerrStub{}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	resp, err := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if len(resp.GetTargets()) != 1 {
		t.Fatalf("want 1 target, got %d msg=%q", len(resp.GetTargets()), resp.GetMessage())
	}
	tgt := resp.GetTargets()[0]
	if tgt.GetStatus() != "queued" || tgt.GetQuality() != "1080p" || tgt.GetExternalId() != "101" {
		t.Fatalf("bad target: %+v", tgt)
	}
	if stub.bodies[0]["is4k"] != false || stub.bodies[0]["mediaType"] != "movie" || stub.bodies[0]["mediaId"] != float64(42) {
		t.Fatalf("bad body: %+v", stub.bodies[0])
	}
	if _, ok := stub.bodies[0]["seasons"]; ok {
		t.Fatalf("movie body must not carry seasons: %+v", stub.bodies[0])
	}
}

func TestFulfillHDPlus4KWhenSupported(t *testing.T) {
	stub := &seerrStub{}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "series", ExternalIds: map[string]string{"tmdb": "9"}},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}, {Id: "2160p", Is4K: true}},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
	})
	if len(resp.GetTargets()) != 2 {
		t.Fatalf("want 2 targets, got %d", len(resp.GetTargets()))
	}
	if stub.bodies[0]["is4k"] != false || stub.bodies[1]["is4k"] != true {
		t.Fatalf("is4k mapping wrong: %+v %+v", stub.bodies[0], stub.bodies[1])
	}
	if stub.bodies[0]["mediaType"] != "tv" || stub.bodies[0]["seasons"] != "all" {
		t.Fatalf("series should map to tv + seasons all: %+v", stub.bodies[0])
	}
}

func TestFulfill4KSkippedWhenUnsupported(t *testing.T) {
	stub := &seerrStub{}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "9"}},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}, {Id: "2160p", Is4K: true}},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	if len(resp.GetTargets()) != 1 || resp.GetTargets()[0].GetQuality() != "1080p" {
		t.Fatalf("4k should be skipped, got %d targets", len(resp.GetTargets()))
	}
	if len(stub.bodies) != 1 {
		t.Fatalf("only the HD request should be sent, got %d", len(stub.bodies))
	}
}

func TestFulfillMissingTMDBReturnsMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()
	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{}},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	if len(resp.GetTargets()) != 0 || resp.GetMessage() == "" {
		t.Fatalf("missing tmdb should yield zero targets + a request-level message, got %d targets msg=%q", len(resp.GetTargets()), resp.GetMessage())
	}
}

func TestFulfillDuplicateRecoversID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/request" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"message":"already requested"}`))
		case r.URL.Path == "/api/v1/request" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[{"id":314,"is4k":false,"media":{"tmdbId":42}}]}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	tgt := resp.GetTargets()[0]
	if tgt.GetStatus() != "queued" || tgt.GetExternalId() != "314" {
		t.Fatalf("409 should recover the existing id as queued: %+v", tgt)
	}
}

func TestFulfillEmptyBodyRecoversID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/request" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated) // 201 with an empty body: no usable id
		case r.URL.Path == "/api/v1/request" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[{"id":271,"is4k":false,"media":{"tmdbId":42,"status":3}}]}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	if len(resp.GetTargets()) != 1 {
		t.Fatalf("want 1 target, got %d", len(resp.GetTargets()))
	}
	tgt := resp.GetTargets()[0]
	if tgt.GetStatus() != "queued" || tgt.GetExternalId() != "271" {
		t.Fatalf("empty-body create should recover the existing id as queued: %+v", tgt)
	}
}

// A 4K target's external status is the media's status4k, on both the create
// and the duplicate-recovery paths; the HD tier's status is available here.
func TestFulfill4KExternalStatusReadsStatus4K(t *testing.T) {
	for _, dup := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/api/v1/request" && r.Method == http.MethodPost && dup:
				w.WriteHeader(http.StatusConflict)
			case r.URL.Path == "/api/v1/request" && r.Method == http.MethodPost:
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{"id":61,"status":2,"is4k":true,"media":{"tmdbId":42,"status":5,"status4k":2}}`))
			case r.URL.Path == "/api/v1/request" && r.Method == http.MethodGet:
				w.Write([]byte(`{"results":[{"id":62,"is4k":true,"media":{"tmdbId":42,"status":5,"status4k":3}}]}`))
			default:
				http.Error(w, "unexpected", http.StatusNotFound)
			}
		}))
		resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
			Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}},
			Qualities:   []*pluginv1.RequestedQuality{{Id: "2160p", Is4K: true}},
			Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
		})
		srv.Close()
		want := "2"
		if dup {
			want = "3"
		}
		if len(resp.GetTargets()) != 1 {
			t.Fatalf("dup=%t: want 1 target, got %d", dup, len(resp.GetTargets()))
		}
		if tgt := resp.GetTargets()[0]; tgt.GetStatus() != "queued" || tgt.GetExternalStatus() != want {
			t.Fatalf("dup=%t: want queued with external status %s, got %+v", dup, want, tgt)
		}
	}
}

func TestFulfillZeroTargetsReturnsMessage(t *testing.T) {
	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "2160p", Is4K: true}},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", "http://unused", false)},
	})
	if len(resp.GetTargets()) != 0 || resp.GetMessage() == "" {
		t.Fatalf("want zero targets + a message, got %d targets msg=%q", len(resp.GetTargets()), resp.GetMessage())
	}
}

func TestCheckStatusMapsAndSkipsMissingConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// request 5 -> processing (downloading); request 6 -> available (completed)
		switch r.URL.Path {
		case "/api/v1/request/5":
			w.Write([]byte(`{"id":5,"status":2,"media":{"status":3}}`))
		case "/api/v1/request/6":
			w.Write([]byte(`{"id":6,"status":2,"media":{"status":5}}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resp, err := New().CheckStatus(context.Background(), &pluginv1.CheckStatusRequest{
		Request: &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "1"}},
		Targets: []*pluginv1.TargetRef{
			{Quality: "1080p", ConnectionId: "c1", ExternalId: "5"},
			{Quality: "2160p", ConnectionId: "c1", ExternalId: "6"},
			{Quality: "1080p", ConnectionId: "missing", ExternalId: "9"}, // connection not provided -> skipped
		},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
	})
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if len(resp.GetStatuses()) != 2 {
		t.Fatalf("want 2 statuses (missing connection skipped), got %d", len(resp.GetStatuses()))
	}
	got := map[string]string{}
	for _, st := range resp.GetStatuses() {
		got[st.GetQuality()] = st.GetStatus()
	}
	if got["1080p"] != "downloading" || got["2160p"] != "completed" {
		t.Fatalf("status mapping wrong: %+v", got)
	}
}

func TestCheckStatusCarriesDownloadProgress(t *testing.T) {
	const hdItem = `{"size":1000,"sizeLeft":250,"status":"downloading","estimatedCompletionTime":"2026-09-28T12:00:00.000Z","downloadId":"hd"}`
	const uhdItem = `{"size":4000,"sizeLeft":4000,"status":"paused","estimatedCompletionTime":null,"downloadId":"uhd"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/request/5": // HD, downloading
			w.Write([]byte(`{"id":5,"status":2,"is4k":false,"media":{"status":3,"status4k":3,"downloadStatus":[` + hdItem + `],"downloadStatus4k":[` + uhdItem + `]}}`))
		case "/api/v1/request/6": // 4K, downloading: reads downloadStatus4k
			w.Write([]byte(`{"id":6,"status":2,"is4k":true,"media":{"status":3,"status4k":3,"downloadStatus":[` + hdItem + `],"downloadStatus4k":[` + uhdItem + `]}}`))
		case "/api/v1/request/7": // available: a leftover queue item reports nothing
			w.Write([]byte(`{"id":7,"status":2,"is4k":false,"media":{"status":5,"downloadStatus":[` + hdItem + `]}}`))
		case "/api/v1/request/8": // queued, nothing in the queue
			w.Write([]byte(`{"id":8,"status":2,"is4k":false,"media":{"status":2,"downloadStatus":[],"downloadStatus4k":[]}}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resp, err := New().CheckStatus(context.Background(), &pluginv1.CheckStatusRequest{
		Request: &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "1"}},
		Targets: []*pluginv1.TargetRef{
			{Quality: "hd", ConnectionId: "c1", ExternalId: "5"},
			{Quality: "uhd", ConnectionId: "c1", ExternalId: "6"},
			{Quality: "available", ConnectionId: "c1", ExternalId: "7"},
			{Quality: "idle", ConnectionId: "c1", ExternalId: "8"},
		},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
	})
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	byQuality := map[string]*pluginv1.TargetStatus{}
	for _, st := range resp.GetStatuses() {
		byQuality[st.GetQuality()] = st
	}
	if len(byQuality) != 4 {
		t.Fatalf("want 4 statuses, got %d", len(byQuality))
	}

	hd := byQuality["hd"].GetProgress()
	if hd == nil || hd.GetPhase() != "downloading" || hd.GetBytesTotal() != 1000 || hd.GetBytesLeft() != 250 || hd.GetDownloads() != 1 {
		t.Fatalf("hd progress: %+v", hd)
	}
	if want := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC); !hd.GetEstimatedCompletion().AsTime().Equal(want) {
		t.Fatalf("hd eta: want %v got %v", want, hd.GetEstimatedCompletion().AsTime())
	}

	if st := byQuality["uhd"]; st.GetStatus() != "downloading" {
		t.Fatalf("4K status: want downloading, got %q", st.GetStatus())
	}
	uhd := byQuality["uhd"].GetProgress()
	if uhd == nil || uhd.GetPhase() != "paused" || uhd.GetBytesTotal() != 4000 || uhd.GetBytesLeft() != 4000 || uhd.GetDownloads() != 1 {
		t.Fatalf("4K progress should come from downloadStatus4k: %+v", uhd)
	}
	if uhd.GetEstimatedCompletion() != nil {
		t.Fatalf("4K eta: want unset, got %v", uhd.GetEstimatedCompletion())
	}

	if st := byQuality["available"]; st.GetStatus() != "completed" || st.GetProgress() != nil {
		t.Fatalf("completed target must not carry progress: %+v", st)
	}
	if st := byQuality["idle"]; st.GetStatus() != "queued" || st.GetProgress() != nil {
		t.Fatalf("queued target with an empty queue must not carry progress: %+v", st)
	}
}

// A held-back season pack, listed per episode without a downloadId, counts
// once. Its release title is only a dedupe key and never reaches the host.
func TestCheckStatusCountsHeldBackPackOnceWithoutSendingItsTitle(t *testing.T) {
	const title = "Example.Show.S03.1080p.WEB-DL"
	const pending = `{"size":9000,"sizeLeft":9000,"status":"delay","estimatedCompletionTime":"1970-01-01T00:00:00.000Z","title":"` + title + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/request/9" {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"id":9,"status":2,"is4k":false,"media":{"status":3,"downloadStatus":[` + pending + `,` + pending + `,` + pending + `]}}`))
	}))
	defer srv.Close()

	resp, err := New().CheckStatus(context.Background(), &pluginv1.CheckStatusRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "tv", ExternalIds: map[string]string{"tmdb": "1"}},
		Targets:     []*pluginv1.TargetRef{{Quality: "hd", ConnectionId: "c1", ExternalId: "9"}},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
	})
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if len(resp.GetStatuses()) != 1 {
		t.Fatalf("want 1 status, got %d", len(resp.GetStatuses()))
	}
	p := resp.GetStatuses()[0].GetProgress()
	if p == nil || p.GetPhase() != "queued" || p.GetBytesTotal() != 9000 || p.GetBytesLeft() != 9000 || p.GetDownloads() != 1 {
		t.Fatalf("progress: want queued 9000/9000 over 1 download, got %+v", p)
	}
	wire, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(wire, []byte(title)) {
		t.Fatalf("response carries the release title: %s", prototext.Format(resp))
	}
}

// Seerr tracks HD and 4K on one media record, in status and status4k. Each
// target's status, external status and progress come from its own tier, so a
// 4K target keeps downloading after the HD tier is available, and a 4K-only
// request (HD tier Unknown) still reads as downloading.
func TestCheckStatusReadsTheRequestTiersMediaStatus(t *testing.T) {
	const hdList = `[{"size":1000,"sizeLeft":250,"status":"downloading","estimatedCompletionTime":"2026-09-28T12:00:00.000Z","downloadId":"hd"}]`
	const uhdList = `[{"size":5000,"sizeLeft":3000,"status":"downloading","estimatedCompletionTime":"2026-09-28T14:00:00.000Z","downloadId":"uhd"}]`
	cases := []struct {
		name            string
		is4k            bool
		status          int
		status4k        int
		hdList, uhdList string
		wantStatus      string
		wantTotal       int64 // 0: no progress
		wantLeft        int64
	}{
		{"4k/hd available, 4k processing", true, seerr.MediaStatusAvailable, seerr.MediaStatusProcessing, "[]", uhdList, "downloading", 5000, 3000},
		{"4k/hd unknown, 4k processing", true, seerr.MediaStatusUnknown, seerr.MediaStatusProcessing, "[]", uhdList, "downloading", 5000, 3000},
		{"4k/hd processing, 4k available", true, seerr.MediaStatusProcessing, seerr.MediaStatusAvailable, hdList, "[]", "completed", 0, 0},
		{"hd/hd processing, 4k available", false, seerr.MediaStatusProcessing, seerr.MediaStatusAvailable, hdList, "[]", "downloading", 1000, 250},
	}

	bodies := map[string]string{}
	var targets []*pluginv1.TargetRef
	for i, c := range cases {
		id := itoa(200 + i)
		bodies["/api/v1/request/"+id] = fmt.Sprintf(`{"id":%s,"status":%d,"is4k":%t,"media":{"status":%d,"status4k":%d,"downloadStatus":%s,"downloadStatus4k":%s}}`,
			id, seerr.StatusRequestApproved, c.is4k, c.status, c.status4k, c.hdList, c.uhdList)
		targets = append(targets, &pluginv1.TargetRef{Quality: c.name, ConnectionId: "c1", ExternalId: id})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	resp, err := New().CheckStatus(context.Background(), &pluginv1.CheckStatusRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "1"}},
		Targets:     targets,
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
	})
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	byQuality := map[string]*pluginv1.TargetStatus{}
	for _, st := range resp.GetStatuses() {
		byQuality[st.GetQuality()] = st
	}
	for _, c := range cases {
		st, ok := byQuality[c.name]
		if !ok {
			t.Errorf("%s: no status", c.name)
			continue
		}
		tierStatus := c.status
		if c.is4k {
			tierStatus = c.status4k
		}
		if st.GetStatus() != c.wantStatus || st.GetExternalStatus() != itoa(tierStatus) {
			t.Errorf("%s: want %s/%d, got %s/%s", c.name, c.wantStatus, tierStatus, st.GetStatus(), st.GetExternalStatus())
		}
		p := st.GetProgress()
		switch {
		case c.wantTotal == 0 && p != nil:
			t.Errorf("%s: want no progress, got %+v", c.name, p)
		case c.wantTotal != 0 && (p == nil || p.GetPhase() != "downloading" || p.GetBytesTotal() != c.wantTotal || p.GetBytesLeft() != c.wantLeft):
			t.Errorf("%s: want downloading %d/%d, got %+v", c.name, c.wantLeft, c.wantTotal, p)
		}
	}
}

// An approved request whose media Seerr has at Processing or Partially
// Available, with nothing in its tier's download list, is downloading without
// progress. The host polls a downloading target every minute only once it has
// reported progress, so this keeps an idle target on the regular cadence. The
// other tier is Available with a download in its list, to show neither its
// status nor its list is read instead.
func TestCheckStatusDownloadingWithEmptyQueueReportsNoProgress(t *testing.T) {
	const otherTier = `[{"size":1000,"sizeLeft":250,"status":"downloading","estimatedCompletionTime":"2026-09-28T12:00:00.000Z","downloadId":"other"}]`
	type tc struct {
		name        string
		is4k        bool
		mediaStatus int
		ownList     string // "" leaves the tier's list out of the JSON
	}
	var cases []tc
	for _, is4k := range []bool{false, true} {
		for _, mediaStatus := range []int{seerr.MediaStatusProcessing, seerr.MediaStatusPartiallyAvailable} {
			for _, ownList := range []string{"", "[]", "null"} {
				cases = append(cases, tc{
					name:        fmt.Sprintf("is4k=%t/media=%d/list=%q", is4k, mediaStatus, ownList),
					is4k:        is4k,
					mediaStatus: mediaStatus,
					ownList:     ownList,
				})
			}
		}
	}

	bodies := map[string]string{}
	var targets []*pluginv1.TargetRef
	for i, c := range cases {
		id := itoa(100 + i)
		ownKey, otherKey := "downloadStatus", "downloadStatus4k"
		status, status4k := c.mediaStatus, seerr.MediaStatusAvailable
		if c.is4k {
			ownKey, otherKey = otherKey, ownKey
			status, status4k = status4k, status
		}
		media := fmt.Sprintf(`"status":%d,"status4k":%d,%q:%s`, status, status4k, otherKey, otherTier)
		if c.ownList != "" {
			media += fmt.Sprintf(`,%q:%s`, ownKey, c.ownList)
		}
		bodies["/api/v1/request/"+id] = fmt.Sprintf(`{"id":%s,"status":%d,"is4k":%t,"media":{%s}}`, id, seerr.StatusRequestApproved, c.is4k, media)
		targets = append(targets, &pluginv1.TargetRef{Quality: c.name, ConnectionId: "c1", ExternalId: id})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	resp, err := New().CheckStatus(context.Background(), &pluginv1.CheckStatusRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "1"}},
		Targets:     targets,
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
	})
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if len(resp.GetStatuses()) != len(cases) {
		t.Fatalf("want %d statuses, got %d", len(cases), len(resp.GetStatuses()))
	}
	for _, st := range resp.GetStatuses() {
		if st.GetStatus() != "downloading" {
			t.Errorf("%s: status want downloading, got %q", st.GetQuality(), st.GetStatus())
		}
		if st.GetProgress() != nil {
			t.Errorf("%s: want no progress, got %+v", st.GetQuality(), st.GetProgress())
		}
	}
}

func TestCheckStatus404MapsToFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/request/77" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"not found"}`))
			return
		}
		http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()

	resp, err := New().CheckStatus(context.Background(), &pluginv1.CheckStatusRequest{
		Request: &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "1"}},
		Targets: []*pluginv1.TargetRef{
			{Quality: "1080p", ConnectionId: "c1", ExternalId: "77"},
		},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if len(resp.GetStatuses()) != 1 {
		t.Fatalf("404 should yield a failed status, not a skip: got %d statuses", len(resp.GetStatuses()))
	}
	st := resp.GetStatuses()[0]
	if st.GetStatus() != "failed" || st.GetMessage() == "" {
		t.Fatalf("404 should map to failed with a message, got %+v", st)
	}
}

func TestTestConnectionUsesAuthMe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/me" {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"id":1,"email":"owner@example.com"}`))
	}))
	defer ok.Close()
	res, _ := New().TestConnection(context.Background(), &pluginv1.TestConnectionRequest{
		Connection: conn(t, "c1", ok.URL, false),
	})
	if !res.GetOk() {
		t.Fatalf("want ok, got %+v", res)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer bad.Close()
	res, _ = New().TestConnection(context.Background(), &pluginv1.TestConnectionRequest{
		Connection: conn(t, "c1", bad.URL, false),
	})
	if res.GetOk() || res.GetMessage() == "" {
		t.Fatalf("want not-ok with message, got %+v", res)
	}

	// A 200 from a login wall / proxy with a non-user body must NOT pass.
	wall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<html>login</html>`))
	}))
	defer wall.Close()
	res, _ = New().TestConnection(context.Background(), &pluginv1.TestConnectionRequest{
		Connection: conn(t, "c1", wall.URL, false),
	})
	if res.GetOk() || res.GetMessage() == "" {
		t.Fatalf("want not-ok for 200 login-wall body, got %+v", res)
	}
}

func TestFulfillMappedUsesExistingSeerrUser(t *testing.T) {
	var createCalled bool
	var reqBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[{"id":7,"email":"bob@example.com","permissions":32}]}`))
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodPost:
			createCalled = true
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":99}`))
		case r.URL.Path == "/api/v1/request":
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":55,"status":2,"media":{"status":3,"tmdbId":42}}`))
		}
	}))
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped"})
	resp, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "bob@example.com"},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p", Is4K: false}},
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: srv.URL, ApiKey: "k", Config: cfg}},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if createCalled {
		t.Fatalf("must reuse existing user, not create")
	}
	if int(reqBody["userId"].(float64)) != 7 {
		t.Fatalf("request userId = %v, want 7", reqBody["userId"])
	}
	if len(resp.GetTargets()) != 1 || resp.GetTargets()[0].GetStatus() != "queued" {
		t.Fatalf("targets = %+v", resp.GetTargets())
	}
}

func newUserCreateStub(t *testing.T, createBody *map[string]any, createResp string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[]}`)) // no existing user -> create
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(createBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(createResp))
		case r.URL.Path == "/api/v1/request":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":55,"status":2,"media":{"status":3,"tmdbId":42}}`))
		}
	}))
}

func mustFulfill(t *testing.T, baseURL string, cfg *structpb.Struct, qs []*pluginv1.RequestedQuality) {
	t.Helper()
	if _, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "new@example.com"},
		Qualities:   qs,
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: baseURL, ApiKey: "k", Config: cfg}},
	}); err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
}

func TestFulfillMappedCreatesMissingUserWithPermissions(t *testing.T) {
	var createBody map[string]any
	srv := newUserCreateStub(t, &createBody, `{"id":99,"email":"new@example.com"}`)
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "auto_approve": true})
	mustFulfill(t, srv.URL, cfg, []*pluginv1.RequestedQuality{{Id: "1080p"}})

	if createBody["email"] != "new@example.com" {
		t.Fatalf("create email = %v", createBody["email"])
	}
	if got := int(createBody["permissions"].(float64)); got != seerr.PermRequest|seerr.PermAutoApprove {
		t.Fatalf("permissions = %d, want %d (REQUEST|AUTO_APPROVE)", got, seerr.PermRequest|seerr.PermAutoApprove)
	}
}

func TestFulfillMappedGrants4KWhenRequestHas4K(t *testing.T) {
	var createBody map[string]any
	srv := newUserCreateStub(t, &createBody, `{"id":99}`)
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "auto_approve": true, "supports_4k": true})
	mustFulfill(t, srv.URL, cfg, []*pluginv1.RequestedQuality{{Id: "1080p"}, {Id: "2160p", Is4K: true}})

	want := seerr.PermRequest | seerr.PermRequest4K | seerr.PermAutoApprove | seerr.PermAutoApprove4K
	if got := int(createBody["permissions"].(float64)); got != want {
		t.Fatalf("permissions = %d, want %d (incl 4K)", got, want)
	}
}

func TestFulfillMappedAutoApproveOffGrantsRequestOnly(t *testing.T) {
	var createBody map[string]any
	srv := newUserCreateStub(t, &createBody, `{"id":99}`)
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped"})
	mustFulfill(t, srv.URL, cfg, []*pluginv1.RequestedQuality{{Id: "1080p"}})

	if got := int(createBody["permissions"].(float64)); got != seerr.PermRequest {
		t.Fatalf("permissions = %d, want %d (REQUEST only)", got, seerr.PermRequest)
	}
}

func TestFulfillFallsBackToAdminOnResolveFailure(t *testing.T) {
	var reqBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/api/v1/request":
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":55,"status":2,"media":{"status":3,"tmdbId":42}}`))
		}
	}))
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped"})
	_, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "bob@example.com"},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p"}},
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: srv.URL, ApiKey: "k", Config: cfg}},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if _, ok := reqBody["userId"]; ok {
		t.Fatalf("admin fallback must omit userId, got %v", reqBody["userId"])
	}
}

func TestFulfillRequireMappedUserFailsWhenUnmapped(t *testing.T) {
	requestCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[]}`))
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/api/v1/request":
			requestCalled = true
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	cfg, _ := structpb.NewStruct(map[string]any{"requester_mode": "mapped", "require_mapped_user": true})
	resp, err := (&Server{}).Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}, RequesterEmail: "bob@example.com"},
		Qualities:   []*pluginv1.RequestedQuality{{Id: "1080p"}},
		Connections: []*pluginv1.RouterConnection{{Id: "c1", BaseUrl: srv.URL, ApiKey: "k", Config: cfg}},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if requestCalled {
		t.Fatalf("require_mapped_user on: must NOT submit the request when the user can't be mapped")
	}
	if resp.GetMessage() == "" || len(resp.GetTargets()) != 0 {
		t.Fatalf("want a request-level failure message and no targets, got msg=%q targets=%d", resp.GetMessage(), len(resp.GetTargets()))
	}
}

func TestListConfigOptionsAndValidateAreEmpty(t *testing.T) {
	opts, err := New().ListConfigOptions(context.Background(), &pluginv1.ListConfigOptionsRequest{
		Connection: conn(t, "c1", "http://s", false),
	})
	if err != nil || len(opts.GetOptionsByField()) != 0 {
		t.Fatalf("want empty options, got %+v err=%v", opts.GetOptionsByField(), err)
	}
	val, err := New().Validate(context.Background(), &pluginv1.ValidateRequest{
		Connection: conn(t, "c1", "http://s", false),
	})
	if err != nil || len(val.GetFieldErrors()) != 0 || val.GetFormError() != "" {
		t.Fatalf("want empty validate, got %+v err=%v", val, err)
	}
}
