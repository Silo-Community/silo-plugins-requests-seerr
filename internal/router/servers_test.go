package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// multiServerSeerr is a Seerr with several Radarr and Sonarr servers. It
// records request bodies and counts server-list calls per kind.
type multiServerSeerr struct {
	mu        sync.Mutex
	bodies    []map[string]any
	listCalls map[string]int
	failLists bool
}

const radarrServers = `[
 {"id":0,"name":"radarr","is4k":false,"isDefault":true},
 {"id":1,"name":"radarr4k","is4k":true,"isDefault":true},
 {"id":2,"name":"radarr_archive","is4k":false,"isDefault":false},
 {"id":3,"name":"radarr4k_archive","is4k":true,"isDefault":false}
]`

const sonarrServers = `[
 {"id":0,"name":"sonarr","is4k":false,"isDefault":true},
 {"id":1,"name":"sonarr4k","is4k":true,"isDefault":true},
 {"id":2,"name":"sonarr_archive","is4k":false,"isDefault":false},
 {"id":3,"name":"sonarr4k_archive","is4k":true,"isDefault":false}
]`

func (s *multiServerSeerr) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/service/radarr" || r.URL.Path == "/api/v1/service/sonarr"):
			kind := strings.TrimPrefix(r.URL.Path, "/api/v1/service/")
			if s.listCalls == nil {
				s.listCalls = map[string]int{}
			}
			s.listCalls[kind]++
			if s.failLists {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			if kind == "radarr" {
				_, _ = w.Write([]byte(radarrServers))
			} else {
				_, _ = w.Write([]byte(sonarrServers))
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/request":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			s.bodies = append(s.bodies, b)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":` + itoa(200+len(s.bodies)) + `,"status":2,"media":{"status":2}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func serverConn(t *testing.T, baseURL string, cfg map[string]any) *pluginv1.RouterConnection {
	t.Helper()
	s, err := structpb.NewStruct(cfg)
	if err != nil {
		t.Fatalf("structpb: %v", err)
	}
	return &pluginv1.RouterConnection{Id: "c1", BaseUrl: baseURL, ApiKey: "k", Config: s}
}

func fulfill(t *testing.T, mediaType string, qualities []*pluginv1.RequestedQuality, c *pluginv1.RouterConnection) *pluginv1.FulfillResponse {
	t.Helper()
	resp, err := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: mediaType, ExternalIds: map[string]string{"tmdb": "42"}},
		Qualities:   qualities,
		Connections: []*pluginv1.RouterConnection{c},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	return resp
}

var (
	hd    = &pluginv1.RequestedQuality{Id: "1080p", Is4K: false}
	uhd   = &pluginv1.RequestedQuality{Id: "2160p", Is4K: true}
	hdUHD = []*pluginv1.RequestedQuality{hd, uhd}
)

func TestParseServerID(t *testing.T) {
	ok := []struct {
		in   any
		want *int
	}{
		{nil, nil}, {"", nil}, {"  ", nil}, {"default", nil}, {"Default", nil},
		{"0", intp(0)}, {"3", intp(3)}, {" 2 ", intp(2)}, {float64(0), intp(0)}, {float64(5), intp(5)},
	}
	for _, c := range ok {
		got, err := parseServerID(c.in)
		if err != nil {
			t.Fatalf("parseServerID(%#v): unexpected error %v", c.in, err)
		}
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Fatalf("parseServerID(%#v) = %v, want %v", c.in, deref(got), deref(c.want))
		}
	}
	for _, in := range []any{"abc", "-1", "1.5", float64(-1), float64(1.5), true} {
		if _, err := parseServerID(in); err == nil {
			t.Fatalf("parseServerID(%#v): want error", in)
		}
	}
}

func TestFulfillSendsConfiguredRadarrServersPerTier(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp := fulfill(t, "movie", hdUHD, serverConn(t, srv.URL, map[string]any{
		"supports_4k": true, "radarr_server_id": "2", "radarr_4k_server_id": "3",
		// Sonarr keys must not leak into a movie request.
		"sonarr_server_id": "0", "sonarr_4k_server_id": "1",
	}))
	if len(resp.GetTargets()) != 2 {
		t.Fatalf("want 2 targets, got %d msg=%q", len(resp.GetTargets()), resp.GetMessage())
	}
	for _, tgt := range resp.GetTargets() {
		if tgt.GetStatus() != "queued" {
			t.Fatalf("target not queued: %+v", tgt)
		}
	}
	if stub.bodies[0]["is4k"] != false || stub.bodies[0]["serverId"] != float64(2) {
		t.Fatalf("HD body: %+v", stub.bodies[0])
	}
	if stub.bodies[1]["is4k"] != true || stub.bodies[1]["serverId"] != float64(3) {
		t.Fatalf("4K body: %+v", stub.bodies[1])
	}
	// HD and 4K share one Seerr: its Radarr servers are listed once.
	if stub.listCalls["radarr"] != 1 || stub.listCalls["sonarr"] != 0 {
		t.Fatalf("list calls: %+v", stub.listCalls)
	}
}

func TestFulfillSendsConfiguredSonarrServerForSeries(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp := fulfill(t, "series", []*pluginv1.RequestedQuality{hd}, serverConn(t, srv.URL, map[string]any{
		"radarr_server_id": "0", "sonarr_server_id": "2",
	}))
	if len(resp.GetTargets()) != 1 || resp.GetTargets()[0].GetStatus() != "queued" {
		t.Fatalf("targets: %+v msg=%q", resp.GetTargets(), resp.GetMessage())
	}
	b := stub.bodies[0]
	if b["mediaType"] != "tv" || b["serverId"] != float64(2) || b["seasons"] != "all" {
		t.Fatalf("series body: %+v", b)
	}
}

func TestFulfillSendsServerZero(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	fulfill(t, "movie", []*pluginv1.RequestedQuality{hd}, serverConn(t, srv.URL, map[string]any{"radarr_server_id": "0"}))
	if v, ok := stub.bodies[0]["serverId"]; !ok || v != float64(0) {
		t.Fatalf("server 0 must be sent explicitly: %+v", stub.bodies[0])
	}
}

func TestFulfillWithoutServerKeepsSeerrDefault(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	// "default" (the Seerr default option) and an empty value behave as unset.
	fulfill(t, "movie", hdUHD, serverConn(t, srv.URL, map[string]any{"supports_4k": true, "radarr_server_id": "default", "radarr_4k_server_id": ""}))
	for _, b := range stub.bodies {
		if _, ok := b["serverId"]; ok {
			t.Fatalf("no server configured, serverId must be omitted: %+v", b)
		}
	}
	if len(stub.listCalls) != 0 {
		t.Fatalf("no server configured, Seerr's servers must not be listed: %+v", stub.listCalls)
	}
}

func TestFulfillRoutedOverrideReachesSeerr(t *testing.T) {
	// A Silo route sends one tier per call with its overrides merged into the
	// connection config; the plugin only sees the merged key.
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	fulfill(t, "movie", []*pluginv1.RequestedQuality{uhd}, serverConn(t, srv.URL, map[string]any{
		"supports_4k": true, "radarr_4k_server_id": "3",
	}))
	if len(stub.bodies) != 1 || stub.bodies[0]["serverId"] != float64(3) || stub.bodies[0]["is4k"] != true {
		t.Fatalf("bodies: %+v", stub.bodies)
	}
}

func TestFulfillFailsTargetForUnknownServer(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp := fulfill(t, "movie", hdUHD, serverConn(t, srv.URL, map[string]any{
		"supports_4k": true, "radarr_server_id": "9",
	}))
	if len(resp.GetTargets()) != 2 {
		t.Fatalf("want 2 targets, got %+v", resp.GetTargets())
	}
	bad, good := resp.GetTargets()[0], resp.GetTargets()[1]
	if bad.GetStatus() != "failed" || bad.GetQuality() != "1080p" || !strings.Contains(bad.GetMessage(), "radarr_server_id") || !strings.Contains(bad.GetMessage(), "9") {
		t.Fatalf("HD target: %+v", bad)
	}
	// One bad tier never blocks the other.
	if good.GetStatus() != "queued" || good.GetQuality() != "2160p" {
		t.Fatalf("4K target: %+v", good)
	}
	if len(stub.bodies) != 1 || stub.bodies[0]["is4k"] != true {
		t.Fatalf("only the 4K request may reach Seerr: %+v", stub.bodies)
	}
}

func TestFulfillFailsTargetForWrongTierServer(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp := fulfill(t, "movie", hdUHD, serverConn(t, srv.URL, map[string]any{
		"supports_4k": true, "radarr_server_id": "1", "radarr_4k_server_id": "2",
	}))
	for _, tgt := range resp.GetTargets() {
		if tgt.GetStatus() != "failed" {
			t.Fatalf("wrong-tier server must fail the target: %+v", tgt)
		}
	}
	if !strings.Contains(resp.GetTargets()[0].GetMessage(), "is a 4K server") ||
		!strings.Contains(resp.GetTargets()[1].GetMessage(), "is not a 4K server") {
		t.Fatalf("messages: %q / %q", resp.GetTargets()[0].GetMessage(), resp.GetTargets()[1].GetMessage())
	}
	if len(stub.bodies) != 0 {
		t.Fatalf("nothing may reach Seerr: %+v", stub.bodies)
	}
}

func TestFulfillFailsTargetForMalformedServer(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp := fulfill(t, "movie", []*pluginv1.RequestedQuality{hd}, serverConn(t, srv.URL, map[string]any{"radarr_server_id": "radarr_archive"}))
	tgt := resp.GetTargets()[0]
	if tgt.GetStatus() != "failed" || !strings.Contains(tgt.GetMessage(), "not a Seerr server id") {
		t.Fatalf("target: %+v", tgt)
	}
	if len(stub.bodies) != 0 || len(stub.listCalls) != 0 {
		t.Fatalf("a malformed id must fail before calling Seerr: bodies=%v lists=%v", stub.bodies, stub.listCalls)
	}
}

func TestFulfillFailsTargetWhenServersCannotBeListed(t *testing.T) {
	stub := &multiServerSeerr{failLists: true}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp := fulfill(t, "movie", hdUHD, serverConn(t, srv.URL, map[string]any{
		"supports_4k": true, "radarr_server_id": "2", "radarr_4k_server_id": "3",
	}))
	for _, tgt := range resp.GetTargets() {
		if tgt.GetStatus() != "failed" || !strings.Contains(tgt.GetMessage(), "could not list Seerr's Radarr servers") {
			t.Fatalf("target: %+v", tgt)
		}
	}
	if len(stub.bodies) != 0 {
		t.Fatalf("unchecked server must not be sent: %+v", stub.bodies)
	}
	if stub.listCalls["radarr"] != 1 {
		t.Fatalf("a failed list is not retried within one call: %+v", stub.listCalls)
	}
}

func TestListConfigOptionsListsServersPerTier(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp, err := New().ListConfigOptions(context.Background(), &pluginv1.ListConfigOptionsRequest{
		Connection: serverConn(t, srv.URL, map[string]any{}),
	})
	if err != nil {
		t.Fatalf("ListConfigOptions: %v", err)
	}
	want := map[string][]string{
		"radarr_server_id":    {"default=Seerr default (radarr)", "0=radarr", "2=radarr_archive"},
		"radarr_4k_server_id": {"default=Seerr default (radarr4k)", "1=radarr4k", "3=radarr4k_archive"},
		"sonarr_server_id":    {"default=Seerr default (sonarr)", "0=sonarr", "2=sonarr_archive"},
		"sonarr_4k_server_id": {"default=Seerr default (sonarr4k)", "1=sonarr4k", "3=sonarr4k_archive"},
	}
	for key, opts := range want {
		got := resp.GetOptionsByField()[key]
		var flat []string
		for _, o := range got.GetOptions() {
			flat = append(flat, o.GetValue()+"="+o.GetLabel())
		}
		if strings.Join(flat, "|") != strings.Join(opts, "|") {
			t.Fatalf("%s: got %v want %v", key, flat, opts)
		}
	}
	if stub.listCalls["radarr"] != 1 || stub.listCalls["sonarr"] != 1 {
		t.Fatalf("each kind is listed once: %+v", stub.listCalls)
	}
	// Silo's route editor cannot render an option with an empty value.
	for key, list := range resp.GetOptionsByField() {
		for _, o := range list.GetOptions() {
			if o.GetValue() == "" {
				t.Fatalf("%s: option %q has an empty value", key, o.GetLabel())
			}
		}
	}
}

func TestListConfigOptionsUnreachableReturnsNoOptions(t *testing.T) {
	stub := &multiServerSeerr{failLists: true}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp, err := New().ListConfigOptions(context.Background(), &pluginv1.ListConfigOptionsRequest{
		Connection: serverConn(t, srv.URL, map[string]any{}),
	})
	if err != nil || len(resp.GetOptionsByField()) != 0 {
		t.Fatalf("want empty options without error, got %v %v", resp.GetOptionsByField(), err)
	}
}

func TestValidateChecksServerSelections(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp, err := New().Validate(context.Background(), &pluginv1.ValidateRequest{
		Connection: serverConn(t, srv.URL, map[string]any{
			"radarr_server_id":    "2",   // fine
			"radarr_4k_server_id": "0",   // HD server in the 4K slot
			"sonarr_server_id":    "9",   // does not exist
			"sonarr_4k_server_id": "abc", // not an id
		}),
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	fe := resp.GetFieldErrors()
	if _, ok := fe["radarr_server_id"]; ok {
		t.Fatalf("valid server flagged: %v", fe)
	}
	for _, key := range []string{"radarr_4k_server_id", "sonarr_server_id", "sonarr_4k_server_id"} {
		if fe[key] == "" {
			t.Fatalf("%s: want field error, got %v", key, fe)
		}
	}
}

func TestValidateDoesNotBlockSaveWhenSeerrUnreachable(t *testing.T) {
	stub := &multiServerSeerr{failLists: true}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp, err := New().Validate(context.Background(), &pluginv1.ValidateRequest{
		Connection: serverConn(t, srv.URL, map[string]any{"radarr_server_id": "2", "sonarr_server_id": "x"}),
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	fe := resp.GetFieldErrors()
	if _, ok := fe["radarr_server_id"]; ok {
		t.Fatalf("unreachable Seerr must not block a well-formed id: %v", fe)
	}
	if fe["sonarr_server_id"] == "" {
		t.Fatalf("malformed id is still reported: %v", fe)
	}
}

func TestTestConnectionReportsBadServerSelection(t *testing.T) {
	stub := &multiServerSeerr{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	resp, err := New().TestConnection(context.Background(), &pluginv1.TestConnectionRequest{
		Connection: serverConn(t, srv.URL, map[string]any{"radarr_server_id": "2", "sonarr_server_id": "7"}),
	})
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if resp.GetOk() || !strings.Contains(resp.GetMessage(), "sonarr_server_id") {
		t.Fatalf("want failure naming sonarr_server_id, got %+v", resp)
	}

	resp, _ = New().TestConnection(context.Background(), &pluginv1.TestConnectionRequest{
		Connection: serverConn(t, srv.URL, map[string]any{"radarr_server_id": "2", "sonarr_4k_server_id": "3"}),
	})
	if !resp.GetOk() {
		t.Fatalf("valid servers: want ok, got %+v", resp)
	}
}

func intp(n int) *int { return &n }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
