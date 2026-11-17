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
//
// ActiveProfileID and ActiveDirectory are the quality profile and root folder
// Seerr applies to a request that names neither.
type Server struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	Is4K            bool   `json:"is4k"`
	IsDefault       bool   `json:"isDefault"`
	ActiveProfileID int    `json:"activeProfileId"`
	ActiveDirectory string `json:"activeDirectory"`
}

// Profile is a quality profile of a Radarr or Sonarr server.
type Profile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// RootFolder is a root folder of a Radarr or Sonarr server. A request names
// it by Path.
type RootFolder struct {
	Path      string  `json:"path"`
	FreeSpace float64 `json:"freeSpace"` // bytes
}

// ServerDetails is one server with the quality profiles and root folders
// Seerr reads from it, as GET /api/v1/service/{radarr,sonarr}/{id} returns
// them. Seerr asks the Radarr or Sonarr server on every call.
type ServerDetails struct {
	Server      Server       `json:"server"`
	Profiles    []Profile    `json:"profiles"`
	RootFolders []RootFolder `json:"rootFolders"`
}

// ListServers returns the Radarr (kind "radarr") or Sonarr (kind "sonarr")
// servers configured in Seerr.
func ListServers(ctx context.Context, c *httpclient.Client, kind string) ([]Server, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	var out []Server
	if err := c.GetJSON(ctx, "/api/v1/service/"+kind, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetServerDetails returns one Radarr or Sonarr server's quality profiles and
// root folders.
func GetServerDetails(ctx context.Context, c *httpclient.Client, kind string, id int) (*ServerDetails, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	var out ServerDetails
	if err := c.GetJSON(ctx, fmt.Sprintf("/api/v1/service/%s/%d", kind, id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func checkKind(kind string) error {
	if kind != ServiceRadarr && kind != ServiceSonarr {
		return fmt.Errorf("seerr: unknown service kind %q", kind)
	}
	return nil
}
