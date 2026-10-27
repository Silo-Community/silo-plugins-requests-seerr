package seerr

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"
)

// Service kinds, as Seerr names them in /api/v1/service/{kind}.
const (
	ServiceRadarr = "radarr"
	ServiceSonarr = "sonarr"
)

// Server is one Radarr or Sonarr server configured in Seerr, as
// GET /api/v1/service/{radarr,sonarr} lists it. Seerr numbers each kind's
// servers from 0, so 0 is a real id.
type Server struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Is4K      bool   `json:"is4k"`
	IsDefault bool   `json:"isDefault"`
}

// ListServers returns the Radarr (kind "radarr") or Sonarr (kind "sonarr")
// servers configured in Seerr.
func ListServers(ctx context.Context, c *httpclient.Client, kind string) ([]Server, error) {
	if kind != ServiceRadarr && kind != ServiceSonarr {
		return nil, fmt.Errorf("seerr: unknown service kind %q", kind)
	}
	var out []Server
	if err := c.GetJSON(ctx, "/api/v1/service/"+kind, &out); err != nil {
		return nil, err
	}
	return out, nil
}
