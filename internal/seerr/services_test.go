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
	if len(got) != 2 || got[0] != (Server{ID: 0, Name: "sonarr", IsDefault: true}) || got[1] != (Server{ID: 3, Name: "sonarr4k", Is4K: true}) {
		t.Fatalf("parsed wrong: %+v", got)
	}
}

func TestListServersRejectsUnknownKind(t *testing.T) {
	if _, err := ListServers(context.Background(), httpclient.New("http://unused", "k", nil), "lidarr"); err == nil {
		t.Fatal("want error for unknown kind")
	}
}

func TestCreateRequestBodyOmitsUnsetServerAndSendsZero(t *testing.T) {
	body := CreateRequestBody{MediaType: "movie", MediaID: 1}
	if b := mustJSON(t, body); contains(b, "serverId") {
		t.Fatalf("unset server must be omitted: %s", b)
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
