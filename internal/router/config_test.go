package router

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestConnectionFromRouterParsesRequesterConfig(t *testing.T) {
	s, _ := structpb.NewStruct(map[string]any{
		"supports_4k": true, "requester_mode": "mapped", "require_mapped_user": true,
		"auto_approve": true, "radarr_profile_id": "4", "sonarr_root_folder": "/tv",
		"radarr_4k_server_id": "3",
	})
	conn := connectionFromRouter(&pluginv1.RouterConnection{Id: "c1", BaseUrl: "http://x", ApiKey: "k", Config: s})
	if !conn.Mapped || !conn.RequireMappedUser || !conn.AutoApprove || !conn.Supports4K {
		t.Fatalf("parsed wrong: %+v", conn)
	}
	if len(conn.Destination) != 3 || conn.Destination["radarr_profile_id"] != "4" || conn.Destination["sonarr_root_folder"] != "/tv" || conn.Destination["radarr_4k_server_id"] != "3" {
		t.Fatalf("destination parsed wrong: %+v", conn.Destination)
	}
}
