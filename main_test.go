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

	// The Radarr/Sonarr server picks are dynamic selects (so Silo's route
	// editor offers them as per-rule overrides) whose "default" value keeps
	// Seerr's default server. No option may have an empty value: Silo's
	// route editor cannot render one.
	fields := map[string]bool{}
	for _, f := range caps[0].GetConfigSchema()[0].GetAdminForm().GetFields() {
		fields[f.GetKey()] = true
		switch f.GetKey() {
		case "radarr_server_id", "radarr_4k_server_id", "sonarr_server_id", "sonarr_4k_server_id":
			if f.GetControl().String() != "ADMIN_FORM_CONTROL_SELECT" || !f.GetDynamicOptions() {
				t.Fatalf("%s: want a dynamic select, got %v dynamic=%v", f.GetKey(), f.GetControl(), f.GetDynamicOptions())
			}
			if f.GetDefaultValue().GetStringValue() != "default" || len(f.GetOptions()) != 1 || f.GetOptions()[0].GetValue() != "default" {
				t.Fatalf("%s: want a \"default\" default with one static Seerr default option", f.GetKey())
			}
		}
	}
	for _, key := range []string{"radarr_server_id", "radarr_4k_server_id", "sonarr_server_id", "sonarr_4k_server_id"} {
		if !fields[key] {
			t.Fatalf("admin form is missing %s", key)
		}
	}
}
