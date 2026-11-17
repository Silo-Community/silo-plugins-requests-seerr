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

func TestListServersReadsServiceList(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`[{"id":0,"name":"sonarr","is4k":false,"isDefault":true,"activeDirectory":"/tv"},{"id":3,"name":"sonarr4k","is4k":true,"isDefault":false}]`))
	}))
	defer srv.Close()

	got, err := ListServers(context.Background(), httpclient.New(srv.URL, "k", nil), ServiceSonarr)
	if err != nil {
		t.Fatalf("ListServers: %v", err)
	}
	if path != "/api/v1/service/sonarr" {
		t.Fatalf("path: %q", path)
	}
	if len(got) != 2 || got[0] != (Server{ID: 0, Name: "sonarr", IsDefault: true, ActiveDirectory: "/tv"}) || got[1] != (Server{ID: 3, Name: "sonarr4k", Is4K: true}) {
		t.Fatalf("parsed wrong: %+v", got)
	}
}

func TestListServersRejectsUnknownKind(t *testing.T) {
	if _, err := ListServers(context.Background(), httpclient.New("http://unused", "k", nil), "lidarr"); err == nil {
		t.Fatal("want error for unknown kind")
	}
}

func TestGetServerDetailsReadsProfilesAndFolders(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"server":{"id":0,"name":"radarr","activeProfileId":4,"activeDirectory":"/movies"},
			"profiles":[{"id":4,"name":"Ultra-HD"}],
			"rootFolders":[{"id":1,"path":"/movies","freeSpace":1024,"totalSpace":4096}],"tags":[]}`))
	}))
	defer srv.Close()

	got, err := GetServerDetails(context.Background(), httpclient.New(srv.URL, "k", nil), ServiceRadarr, 0)
	if err != nil {
		t.Fatalf("GetServerDetails: %v", err)
	}
	if path != "/api/v1/service/radarr/0" {
		t.Fatalf("path: %q", path)
	}
	if got.Server.ActiveProfileID != 4 || len(got.Profiles) != 1 || got.Profiles[0] != (Profile{ID: 4, Name: "Ultra-HD"}) ||
		len(got.RootFolders) != 1 || got.RootFolders[0] != (RootFolder{Path: "/movies", FreeSpace: 1024}) {
		t.Fatalf("parsed wrong: %+v", got)
	}
	if _, err := GetServerDetails(context.Background(), httpclient.New(srv.URL, "k", nil), "lidarr", 0); err == nil {
		t.Fatal("want error for unknown kind")
	}
}

func TestCreateRequestBodyOmitsUnsetDestinationAndSendsZero(t *testing.T) {
	body := CreateRequestBody{MediaType: "movie", MediaID: 1}
	b := mustJSON(t, body)
	if contains(b, "serverId") || contains(b, "profileId") || contains(b, "rootFolder") {
		t.Fatalf("unset destination must be omitted: %s", b)
	}
	if !contains(b, `"is4k":false`) {
		t.Fatalf("is4k must always be sent: %s", b)
	}
	four := 4
	body.ProfileID, body.RootFolder = &four, "/movies"
	if b := mustJSON(t, body); !contains(b, `"profileId":4`) || !contains(b, `"rootFolder":"/movies"`) {
		t.Fatalf("profile and folder must be sent: %s", b)
	}
	zero := 0
	body.ServerID = &zero
	if b := mustJSON(t, body); !contains(b, `"serverId":0`) {
		t.Fatalf("server 0 must be sent: %s", b)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
