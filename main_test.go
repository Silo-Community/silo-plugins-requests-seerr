package main

import (
	"testing"

	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

func TestEmbeddedManifestLoads(t *testing.T) {
	m, err := publicmanifest.LoadWithChecksum(manifestJSON, version)
	if err != nil {
		t.Fatalf("LoadWithChecksum: %v", err)
	}
	if m.GetPluginId() != "silo.requests.seerr" {
		t.Fatalf("plugin_id: want silo.requests.seerr got %q", m.GetPluginId())
	}
	caps := m.GetCapabilities()
	if len(caps) != 1 {
		t.Fatalf("want 1 capability, got %d", len(caps))
	}
	if caps[0].GetType() != "request_router.v1" || caps[0].GetId() != "seerr" {
		t.Fatalf("capability: want request_router.v1/seerr got %q/%q", caps[0].GetType(), caps[0].GetId())
	}
	rr := caps[0].GetRequestRouter()
	if !rr.GetReportsDownloadProgress() {
		t.Fatalf("request_router.reports_download_progress: want true")
	}
	// Seerr requests are whole-series ("seasons": "all"), so the plugin must
	// not receive season-only requests.
	if rr.GetSupportsSeasons() {
		t.Fatalf("request_router.supports_seasons: want false")
	}
}
