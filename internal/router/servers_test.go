package router

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

// multiServerSeerr is a Seerr with several Radarr and Sonarr servers. It
// records request bodies and counts server-list and server-detail calls.
type multiServerSeerr struct {
	mu          sync.Mutex
	bodies      []map[string]any
	listCalls   map[string]int
	detailCalls map[string]int
	failLists   bool
	failDetails bool
}

const radarrServers = `[
 {"id":0,"name":"radarr","is4k":false,"isDefault":true,"activeProfileId":1,"activeDirectory":"/movies"},
 {"id":1,"name":"radarr4k","is4k":true,"isDefault":true,"activeProfileId":1,"activeDirectory":"/movies4k"},
 {"id":2,"name":"radarr_archive","is4k":false,"isDefault":false,"activeProfileId":7,"activeDirectory":"/archive"}
]`

const sonarrServers = `[
 {"id":0,"name":"sonarr","is4k":false,"isDefault":true,"activeProfileId":1,"activeDirectory":"/tv"},
 {"id":1,"name":"sonarr4k","is4k":true,"isDefault":true,"activeProfileId":1,"activeDirectory":"/tv4k"},
 {"id":2,"name":"sonarr_archive","is4k":false,"isDefault":false,"activeProfileId":7,"activeDirectory":"/archive"}
]`

// serverDetails are the profiles and folders of each standard server, the
// same for both kinds: server 0 has profiles 1 and 4, server 2 has 7 and 8.
var serverDetails = map[string]string{
	"0": `{"profiles":[{"id":1,"name":"HD-1080p"},{"id":4,"name":"Ultra-HD"}],
	       "rootFolders":[{"id":1,"path":"/movies","freeSpace":53687091200},{"id":2,"path":"/movies-uhd","freeSpace":2199023255552},{"id":3,"path":"/tv"}]}`,
	"2": `{"profiles":[{"id":7,"name":"Archive"},{"id":8,"name":"Archive UHD"}],
	       "rootFolders":[{"id":1,"path":"/archive","freeSpace":0}]}`,
}

func (s *multiServerSeerr) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.listCalls == nil {
			s.listCalls, s.detailCalls = map[string]int{}, map[string]int{}
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/service/")
		kind, id, isDetail := strings.Cut(rest, "/")
		switch {
		case r.Method == http.MethodGet && rest != r.URL.Path && !isDetail:
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
		case r.Method == http.MethodGet && rest != r.URL.Path:
			s.detailCalls[rest]++
			details, ok := serverDetails[id]
			if s.failDetails || !ok {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(details))
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

// newMultiServerSeerr starts a multiServerSeerr.
func newMultiServerSeerr(t *testing.T, stub *multiServerSeerr) string {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

var (
	hd    = &pluginv1.RequestedQuality{Id: "1080p", Is4K: false}
	uhd   = &pluginv1.RequestedQuality{Id: "2160p", Is4K: true}
	hdUHD = []*pluginv1.RequestedQuality{hd, uhd}
)

func TestParseID(t *testing.T) {
	ok := []struct {
		in   any
		want *int
	}{
		{nil, nil}, {"", nil}, {"  ", nil}, {"default", nil}, {"Default", nil},
		{"0", intp(0)}, {"3", intp(3)}, {" 2 ", intp(2)}, {float64(0), intp(0)}, {float64(5), intp(5)},
	}
	for _, c := range ok {
		got, err := parseID(c.in, "server")
		if err != nil {
			t.Fatalf("parseID(%#v): unexpected error %v", c.in, err)
		}
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Fatalf("parseID(%#v) = %v, want %v", c.in, deref(got), deref(c.want))
		}
	}
	for _, in := range []any{"abc", "-1", "1.5", float64(-1), float64(1.5), true} {
		if _, err := parseID(in, "server"); err == nil {
			t.Fatalf("parseID(%#v): want error", in)
		}
	}
}

func TestRootFolderReadsPathOrDefault(t *testing.T) {
	for in, want := range map[any]string{nil: "", "": "", "default": "", "Default": "", " /movies ": "/movies"} {
		got, err := Connection{Destination: map[string]any{"k": in}}.RootFolder("k")
		if err != nil || got != want {
			t.Fatalf("RootFolder(%#v) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := (Connection{Destination: map[string]any{"k": float64(3)}}).RootFolder("k"); err == nil {
		t.Fatal("a number is not a root folder path")
	}
}

// The reported failure: one Radarr for HD and 4K. Both tiers are answered by
// one standard request that carries the configured profile and folder.
func TestFulfillSendsServerProfileAndFolder(t *testing.T) {
	stub := &multiServerSeerr{}
	url := newMultiServerSeerr(t, stub)

	resp := fulfill(t, "movie", hdUHD, serverConn(t, url, map[string]any{
		"radarr_server_id": "0", "radarr_profile_id": "4", "radarr_root_folder": "/movies-uhd",
		// Sonarr keys must not leak into a movie request.
		"sonarr_server_id": "2", "sonarr_profile_id": "8", "sonarr_root_folder": "/archive",
	}))
	if len(resp.GetTargets()) != 2 {
		t.Fatalf("want 2 targets, got %d msg=%q", len(resp.GetTargets()), resp.GetMessage())
	}
	for _, tgt := range resp.GetTargets() {
		if tgt.GetStatus() != "queued" || tgt.GetExternalId() != "201" {
			t.Fatalf("target should follow request 201: %+v", tgt)
		}
	}
	if len(stub.bodies) != 1 {
		t.Fatalf("want one Seerr request, got %+v", stub.bodies)
	}
	b := stub.bodies[0]
	if b["is4k"] != false || b["serverId"] != float64(0) || b["profileId"] != float64(4) || b["rootFolder"] != "/movies-uhd" {
		t.Fatalf("body: %+v", b)
	}
	if stub.listCalls["radarr"] != 1 || stub.detailCalls["radarr/0"] != 1 || len(stub.listCalls)+len(stub.detailCalls) != 2 {
		t.Fatalf("only the Radarr server in use is read, once: lists=%v details=%v", stub.listCalls, stub.detailCalls)
	}
}

func TestFulfillSendsSonarrDestinationForSeries(t *testing.T) {
	stub := &multiServerSeerr{}
	url := newMultiServerSeerr(t, stub)

	resp := fulfill(t, "series", []*pluginv1.RequestedQuality{hd}, serverConn(t, url, map[string]any{
		"radarr_server_id": "0", "radarr_profile_id": "4",
		"sonarr_server_id": "2", "sonarr_profile_id": "8", "sonarr_root_folder": "/archive",
	}))
	if len(resp.GetTargets()) != 1 || resp.GetTargets()[0].GetStatus() != "queued" {
		t.Fatalf("targets: %+v msg=%q", resp.GetTargets(), resp.GetMessage())
	}
	b := stub.bodies[0]
	if b["mediaType"] != "tv" || b["seasons"] != "all" || b["serverId"] != float64(2) || b["profileId"] != float64(8) || b["rootFolder"] != "/archive" {
		t.Fatalf("series body: %+v", b)
	}
}

// A profile or folder without a server is checked against, and applies to,
// Seerr's default standard server.
func TestFulfillProfileWithoutServerUsesSeerrDefaultServer(t *testing.T) {
	stub := &multiServerSeerr{}
	url := newMultiServerSeerr(t, stub)

	resp := fulfill(t, "movie", []*pluginv1.RequestedQuality{hd}, serverConn(t, url, map[string]any{"radarr_profile_id": "4"}))
	if resp.GetTargets()[0].GetStatus() != "queued" {
		t.Fatalf("target: %+v", resp.GetTargets()[0])
	}
	b := stub.bodies[0]
	if _, ok := b["serverId"]; ok || b["profileId"] != float64(4) {
		t.Fatalf("want profile 4 on Seerr's default server: %+v", b)
	}
	if _, ok := b["rootFolder"]; ok {
		t.Fatalf("unset folder must be omitted: %+v", b)
	}
	if stub.detailCalls["radarr/0"] != 1 {
		t.Fatalf("the default server's profiles should be read: %v", stub.detailCalls)
	}
}

func TestFulfillSendsServerZero(t *testing.T) {
	stub := &multiServerSeerr{}
	url := newMultiServerSeerr(t, stub)

	fulfill(t, "movie", []*pluginv1.RequestedQuality{hd}, serverConn(t, url, map[string]any{"radarr_server_id": "0"}))
	if v, ok := stub.bodies[0]["serverId"]; !ok || v != float64(0) {
		t.Fatalf("server 0 must be sent explicitly: %+v", stub.bodies[0])
	}
	if len(stub.detailCalls) != 0 {
		t.Fatalf("no profile or folder set, the server's details must not be read: %v", stub.detailCalls)
	}
}

func TestFulfillWithoutDestinationKeepsSeerrDefault(t *testing.T) {
	stub := &multiServerSeerr{}
	url := newMultiServerSeerr(t, stub)

	// "default" (the Seerr default option) and an empty value behave as unset.
	fulfill(t, "movie", hdUHD, serverConn(t, url, map[string]any{
		"radarr_server_id": "default", "radarr_profile_id": "", "radarr_root_folder": "default",
	}))
	for _, key := range []string{"serverId", "profileId", "rootFolder"} {
		if _, ok := stub.bodies[0][key]; ok {
			t.Fatalf("nothing configured, %s must be omitted: %+v", key, stub.bodies[0])
		}
	}
	if len(stub.listCalls) != 0 || len(stub.detailCalls) != 0 {
		t.Fatalf("nothing configured, Seerr's servers must not be read: %v %v", stub.listCalls, stub.detailCalls)
	}
}

// Each case must fail every target with a message naming the field, and send
// nothing: Seerr would use its own default without saying so.
func TestFulfillFailsTargetsForBadDestination(t *testing.T) {
	for name, c := range map[string]struct {
		cfg         map[string]any
		failLists   bool
		failDetails bool
		want        []string
	}{
		"unknown server":      {cfg: map[string]any{"radarr_server_id": "9"}, want: []string{"radarr_server_id", "9", "does not have"}},
		"4K server":           {cfg: map[string]any{"radarr_server_id": "1"}, want: []string{"radarr_server_id", "radarr4k", "4K server"}},
		"malformed server":    {cfg: map[string]any{"radarr_server_id": "radarr_archive"}, want: []string{"radarr_server_id", "not a Seerr server id"}},
		"malformed profile":   {cfg: map[string]any{"radarr_profile_id": "Ultra-HD"}, want: []string{"radarr_profile_id", "not a Seerr quality profile id"}},
		"unknown profile":     {cfg: map[string]any{"radarr_profile_id": "8"}, want: []string{"radarr_profile_id", "8", `"radarr"`}},
		"other server's":      {cfg: map[string]any{"radarr_server_id": "2", "radarr_profile_id": "4", "radarr_root_folder": "/movies"}, want: []string{"radarr_profile_id", "radarr_root_folder", `"radarr_archive"`}},
		"unknown folder":      {cfg: map[string]any{"radarr_root_folder": "/nope"}, want: []string{"radarr_root_folder", "/nope", "not a root folder"}},
		"servers unreachable": {cfg: map[string]any{"radarr_server_id": "2", "radarr_profile_id": "7"}, failLists: true, want: []string{"could not list Seerr's Radarr servers", "radarr_server_id", "radarr_profile_id"}},
		"details unreachable": {cfg: map[string]any{"radarr_root_folder": "/movies"}, failDetails: true, want: []string{"could not read the profiles and folders", "radarr_root_folder"}},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &multiServerSeerr{failLists: c.failLists, failDetails: c.failDetails}
			resp := fulfill(t, "movie", hdUHD, serverConn(t, newMultiServerSeerr(t, stub), c.cfg))
			if len(resp.GetTargets()) != 2 {
				t.Fatalf("want a failed target per quality, got %+v", resp.GetTargets())
			}
			for _, tgt := range resp.GetTargets() {
				if tgt.GetStatus() != "failed" || tgt.GetConnectionId() != "c1" || tgt.GetQuality() == "" {
					t.Fatalf("target: %+v", tgt)
				}
				for _, part := range c.want {
					if !strings.Contains(tgt.GetMessage(), part) {
						t.Fatalf("message %q should contain %q", tgt.GetMessage(), part)
					}
				}
			}
			if len(stub.bodies) != 0 {
				t.Fatalf("nothing may reach Seerr: %+v", stub.bodies)
			}
			if stub.listCalls["radarr"] > 1 {
				t.Fatalf("a list is not repeated within one call: %+v", stub.listCalls)
			}
		})
	}
}

func optionLabels(list *pluginv1.ConfigOptionList) string {
	var flat []string
	for _, o := range list.GetOptions() {
		flat = append(flat, o.GetValue()+"="+o.GetLabel())
	}
	return strings.Join(flat, "|")
}

func TestListConfigOptionsListsServersProfilesAndFolders(t *testing.T) {
	stub := &multiServerSeerr{}
	url := newMultiServerSeerr(t, stub)

	// Radarr is left on Seerr's default server; Sonarr picks the archive.
	resp, err := New().ListConfigOptions(context.Background(), &pluginv1.ListConfigOptionsRequest{
		Connection: serverConn(t, url, map[string]any{"sonarr_server_id": "2"}),
	})
	if err != nil {
		t.Fatalf("ListConfigOptions: %v", err)
	}
	want := map[string]string{
		// 4K servers are never offered.
		"radarr_server_id":   "default=Seerr default (radarr)|0=radarr|2=radarr_archive",
		"radarr_profile_id":  "default=Seerr default (HD-1080p)|1=HD-1080p|4=Ultra-HD",
		"radarr_root_folder": "default=Seerr default (/movies)|/movies=/movies (50 GB free)|/movies-uhd=/movies-uhd (2.0 TB free)|/tv=/tv",
		"sonarr_server_id":   "default=Seerr default (sonarr)|0=sonarr|2=sonarr_archive",
		"sonarr_profile_id":  "default=Seerr default (Archive)|7=Archive|8=Archive UHD",
		"sonarr_root_folder": "default=Seerr default (/archive)|/archive=/archive",
	}
	if len(resp.GetOptionsByField()) != len(want) {
		t.Fatalf("fields: got %v", resp.GetOptionsByField())
	}
	for key, opts := range want {
		if got := optionLabels(resp.GetOptionsByField()[key]); got != opts {
			t.Fatalf("%s:\n got %s\nwant %s", key, got, opts)
		}
	}
	if stub.listCalls["radarr"] != 1 || stub.listCalls["sonarr"] != 1 || stub.detailCalls["radarr/0"] != 1 || stub.detailCalls["sonarr/2"] != 1 {
		t.Fatalf("each list and server is read once: %v %v", stub.listCalls, stub.detailCalls)
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

// A server whose profiles cannot be read still gets its server list; the
// profile and folder selects fall back to their static option.
func TestListConfigOptionsWithoutDetailsListsServersOnly(t *testing.T) {
	stub := &multiServerSeerr{failDetails: true}
	resp, err := New().ListConfigOptions(context.Background(), &pluginv1.ListConfigOptionsRequest{
		Connection: serverConn(t, newMultiServerSeerr(t, stub), map[string]any{}),
	})
	if err != nil || len(resp.GetOptionsByField()) != 2 || resp.GetOptionsByField()["radarr_server_id"] == nil || resp.GetOptionsByField()["sonarr_server_id"] == nil {
		t.Fatalf("want only the two server lists, got %v %v", resp.GetOptionsByField(), err)
	}
}

func TestListConfigOptionsUnreachableReturnsNoOptions(t *testing.T) {
	stub := &multiServerSeerr{failLists: true}
	resp, err := New().ListConfigOptions(context.Background(), &pluginv1.ListConfigOptionsRequest{
		Connection: serverConn(t, newMultiServerSeerr(t, stub), map[string]any{}),
	})
	if err != nil || len(resp.GetOptionsByField()) != 0 {
		t.Fatalf("want empty options without error, got %v %v", resp.GetOptionsByField(), err)
	}
}

func TestValidateChecksDestinationSelections(t *testing.T) {
	stub := &multiServerSeerr{}
	resp, err := New().Validate(context.Background(), &pluginv1.ValidateRequest{
		Connection: serverConn(t, newMultiServerSeerr(t, stub), map[string]any{
			"radarr_server_id":   "2",       // fine
			"radarr_profile_id":  "8",       // fine on server 2
			"radarr_root_folder": "/movies", // server 0's folder
			"sonarr_server_id":   "1",       // 4K server
			"sonarr_profile_id":  "abc",     // not an id
		}),
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	fe := resp.GetFieldErrors()
	for _, key := range []string{"radarr_server_id", "radarr_profile_id", "sonarr_root_folder"} {
		if _, ok := fe[key]; ok {
			t.Fatalf("%s: valid value flagged: %v", key, fe)
		}
	}
	for _, key := range []string{"radarr_root_folder", "sonarr_server_id", "sonarr_profile_id"} {
		if fe[key] == "" {
			t.Fatalf("%s: want field error, got %v", key, fe)
		}
	}
}

func TestValidateDoesNotBlockSaveWhenSeerrUnreachable(t *testing.T) {
	for _, stub := range []*multiServerSeerr{{failLists: true}, {failDetails: true}} {
		resp, err := New().Validate(context.Background(), &pluginv1.ValidateRequest{
			Connection: serverConn(t, newMultiServerSeerr(t, stub), map[string]any{
				"radarr_server_id": "2", "radarr_profile_id": "8", "radarr_root_folder": "/archive", "sonarr_server_id": "x",
			}),
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		fe := resp.GetFieldErrors()
		if len(fe) != 1 || fe["sonarr_server_id"] == "" {
			t.Fatalf("only the malformed id may be reported while Seerr cannot be asked: %v", fe)
		}
	}
}

func TestTestConnectionReportsBadDestination(t *testing.T) {
	check := func(stub *multiServerSeerr, cfg map[string]any) *pluginv1.TestConnectionResponse {
		t.Helper()
		resp, err := New().TestConnection(context.Background(), &pluginv1.TestConnectionRequest{
			Connection: serverConn(t, newMultiServerSeerr(t, stub), cfg),
		})
		if err != nil {
			t.Fatalf("TestConnection: %v", err)
		}
		return resp
	}

	resp := check(&multiServerSeerr{}, map[string]any{"radarr_server_id": "2", "sonarr_server_id": "7"})
	if resp.GetOk() || !strings.Contains(resp.GetMessage(), "sonarr_server_id") {
		t.Fatalf("want failure naming sonarr_server_id, got %+v", resp)
	}
	resp = check(&multiServerSeerr{}, map[string]any{"sonarr_profile_id": "8"})
	if resp.GetOk() || !strings.Contains(resp.GetMessage(), "sonarr_profile_id") {
		t.Fatalf("want failure naming sonarr_profile_id, got %+v", resp)
	}
	resp = check(&multiServerSeerr{failDetails: true}, map[string]any{"radarr_profile_id": "4"})
	if resp.GetOk() || !strings.Contains(resp.GetMessage(), "connected, but could not read") {
		t.Fatalf("want a connected-but-unchecked failure, got %+v", resp)
	}
	// Keys of earlier versions are ignored, even when they hold nonsense.
	resp = check(&multiServerSeerr{}, map[string]any{
		"radarr_server_id": "2", "radarr_profile_id": "7", "radarr_root_folder": "/archive", "sonarr_4k_server_id": "x",
	})
	if !resp.GetOk() {
		t.Fatalf("valid destination: want ok, got %+v", resp)
	}
}

func intp(n int) *int { return &n }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestFulfillRecoveryKeepsSelectedServer(t *testing.T) {
	for _, response := range []struct {
		name, mediaType string
		status          int
	}{
		{"movie conflict", "movie", http.StatusConflict},
		{"series already requested", "series", http.StatusAccepted},
		{"empty create response", "movie", http.StatusCreated},
	} {
		for _, destination := range []struct {
			name, configured string
			existing, posted any
			wantOK, mixed    bool
		}{
			{name: "same server", configured: "2", existing: 2, posted: float64(2), wantOK: true},
			{name: "different server", configured: "2", existing: 0, posted: float64(2)},
			{name: "unknown server", configured: "2", posted: float64(2)},
			{name: "server zero", configured: "0", existing: 0, posted: float64(0), wantOK: true},
			{name: "default", configured: "default", existing: 0, wantOK: true},
			{name: "legacy default", configured: "default", wantOK: true},
			{name: "mixed servers", configured: "2", existing: 2, posted: float64(2), wantOK: true, mixed: true},
			{name: "default mixed servers", configured: "default", existing: 2, wantOK: true, mixed: true},
		} {
			t.Run(response.name+"/"+destination.name, func(t *testing.T) {
				mediaType, key := "movie", keyRadarrServer
				if response.mediaType == "series" {
					mediaType, key = "tv", keySonarrServer
				}
				media := map[string]any{"tmdbId": 42, "mediaType": mediaType, "status": 2}
				requests := []map[string]any{{"id": 91, "status": 2, "is4k": false, "serverId": destination.existing, "media": media}}
				if destination.mixed {
					requests = append(requests, map[string]any{"id": 92, "status": 2, "is4k": false, "serverId": 0, "media": media})
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case strings.HasPrefix(r.URL.Path, "/api/v1/service/"):
						_, _ = w.Write([]byte(radarrServers))
					case r.Method == http.MethodPost && r.URL.Path == "/api/v1/request":
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if body["serverId"] != destination.posted {
							t.Errorf("posted server = %v, want %v", body["serverId"], destination.posted)
						}
						w.WriteHeader(response.status)
						if response.status != http.StatusCreated {
							_, _ = w.Write([]byte(`{"message":"already requested"}`))
						}
					case r.URL.Path == "/api/v1/"+mediaType+"/42":
						_ = json.NewEncoder(w).Encode(map[string]any{"mediaInfo": map[string]any{
							"tmdbId": 42, "mediaType": mediaType, "status": 2, "requests": requests,
						}})
					case r.URL.Path == "/api/v1/request":
						_ = json.NewEncoder(w).Encode(map[string]any{"results": requests})
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				resp := fulfill(t, response.mediaType, []*pluginv1.RequestedQuality{hd}, serverConn(t, srv.URL, map[string]any{key: destination.configured}))
				if len(resp.GetTargets()) != 1 {
					t.Fatalf("want one target, got %v", resp)
				}
				target := resp.GetTargets()[0]
				if destination.wantOK {
					wantID := "91"
					if destination.mixed && destination.configured == "default" {
						wantID = "92"
					}
					if target.GetStatus() != "queued" || target.GetExternalId() != wantID {
						t.Fatalf("want recovered request %s, got %v", wantID, target)
					}
				} else if target.GetStatus() != "failed" || target.GetExternalId() != "" || !strings.Contains(target.GetMessage(), "server") {
					t.Fatalf("unconfirmed destination must fail clearly, got %v", target)
				}
			})
		}
	}
}

func TestOptionalServerLookupsReturnBeforeRPCDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pluginv1.RegisterRequestRouterServer(server, New())
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pluginv1.NewRequestRouterClient(conn)
	connection := serverConn(t, srv.URL, map[string]any{keyRadarrServer: "2", keySonarrServer: "2"})
	for _, method := range []string{"validate", "options"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if method == "validate" {
				resp, err := client.Validate(ctx, &pluginv1.ValidateRequest{Connection: connection})
				if err != nil || len(resp.GetFieldErrors()) != 0 {
					t.Fatalf("unavailable Seerr must allow saving: response=%v err=%v", resp, err)
				}
			} else {
				resp, err := client.ListConfigOptions(ctx, &pluginv1.ListConfigOptionsRequest{Connection: connection})
				if err != nil || len(resp.GetOptionsByField()) != 0 {
					t.Fatalf("unavailable Seerr must return fallback options: response=%v err=%v", resp, err)
				}
			}
		})
	}
}

// With a separate 4K server, the 4K request takes the 4K destination keys and
// the standard request the standard ones.
func TestFulfillSends4KDestinationToSeparateServer(t *testing.T) {
	stub := &multiServerSeerr{}
	resp := fulfill(t, "movie", hdUHD, serverConn(t, newMultiServerSeerr(t, stub), map[string]any{
		"supports_4k":      true,
		"radarr_server_id": "2", "radarr_4k_server_id": "1",
	}))
	if len(resp.GetTargets()) != 2 || len(stub.bodies) != 2 {
		t.Fatalf("want two requests and targets: targets=%v bodies=%v", resp.GetTargets(), stub.bodies)
	}
	hdBody, uhdBody := stub.bodies[0], stub.bodies[1]
	if hdBody["is4k"] != false || hdBody["serverId"] != float64(2) {
		t.Fatalf("standard request: %+v", hdBody)
	}
	if uhdBody["is4k"] != true || uhdBody["serverId"] != float64(1) {
		t.Fatalf("4K request: %+v", uhdBody)
	}
}

func TestValidateChecksServerTier(t *testing.T) {
	validate := func(cfg map[string]any) map[string]string {
		t.Helper()
		resp, err := New().Validate(context.Background(), &pluginv1.ValidateRequest{
			Connection: serverConn(t, newMultiServerSeerr(t, &multiServerSeerr{}), cfg),
		})
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		return resp.GetFieldErrors()
	}
	// Server 0 is a standard server and server 1 a 4K one.
	fe := validate(map[string]any{"supports_4k": true, "radarr_4k_server_id": "0", "sonarr_4k_server_id": "1", "sonarr_server_id": "1"})
	if fe["radarr_4k_server_id"] == "" || fe["sonarr_server_id"] == "" || fe["sonarr_4k_server_id"] != "" {
		t.Fatalf("want the tier mismatches flagged: %v", fe)
	}
	// Without a separate 4K server the 4K fields are hidden and not checked.
	if fe := validate(map[string]any{"radarr_4k_server_id": "0"}); len(fe) != 0 {
		t.Fatalf("hidden 4K fields must not be checked: %v", fe)
	}
}
