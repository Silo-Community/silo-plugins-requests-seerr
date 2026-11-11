package router

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Community/silo-plugins-requests-seerr/internal/seerr"
)

// Destination config keys. For each media type they name the Seerr
// Radarr/Sonarr server a request goes to, and the quality profile and root
// folder it gets there. Unset, empty or "default" leaves Seerr's own choice.
// A Silo route can override any of them per rule.
const (
	keyRadarrServer     = "radarr_server_id"
	keyRadarrProfile    = "radarr_profile_id"
	keyRadarrRootFolder = "radarr_root_folder"
	keySonarrServer     = "sonarr_server_id"
	keySonarrProfile    = "sonarr_profile_id"
	keySonarrRootFolder = "sonarr_root_folder"
)

// seerrDefault is the select value for "Seerr default": send nothing and let
// Seerr decide. It is not empty because Silo's route editor cannot show a
// select option whose value is empty.
const seerrDefault = "default"

// serviceFields names the destination keys of one Seerr service kind.
type serviceFields struct {
	Kind       string // seerr.ServiceRadarr | seerr.ServiceSonarr
	Server     string
	Profile    string
	RootFolder string
}

// keys lists the service's config keys in form order.
func (f serviceFields) keys() []string {
	return []string{f.Server, f.Profile, f.RootFolder}
}

// services lists every service kind's destination keys, in form order.
var services = []serviceFields{
	{seerr.ServiceRadarr, keyRadarrServer, keyRadarrProfile, keyRadarrRootFolder},
	{seerr.ServiceSonarr, keySonarrServer, keySonarrProfile, keySonarrRootFolder},
}

// serviceFor returns the destination keys for a Seerr media type
// ("movie" | "tv").
func serviceFor(mediaType string) serviceFields {
	if mediaType == "tv" {
		return services[1]
	}
	return services[0]
}

// Connection is the per-call Seerr connection: base URL + API key (host chrome)
// plus plugin_config fields.
type Connection struct {
	ID      string
	BaseURL string
	APIKey  string

	Mapped            bool // requester_mode == "mapped"
	RequireMappedUser bool // mapped + can't map -> fail the request (else admin fallback)
	AutoApprove       bool // grant AUTO_APPROVE to mapped users

	// Destination holds the raw destination values, keyed by config key.
	// Absent, empty or "default" means Seerr's own choice.
	Destination map[string]any
}

// connectionFromRouter parses a host-supplied RouterConnection. base_url and
// api_key are chrome; the remaining fields come from the config struct. Keys
// of earlier versions (supports_4k, the 4K server picks) are ignored.
func connectionFromRouter(c *pluginv1.RouterConnection) Connection {
	conn := Connection{
		ID:          c.GetId(),
		BaseURL:     c.GetBaseUrl(),
		APIKey:      c.GetApiKey(),
		Destination: map[string]any{},
	}
	if cfg := c.GetConfig(); cfg != nil {
		m := cfg.AsMap()
		if v, ok := m["requester_mode"].(string); ok {
			conn.Mapped = v == "mapped"
		}
		conn.RequireMappedUser, _ = m["require_mapped_user"].(bool)
		conn.AutoApprove, _ = m["auto_approve"].(bool)
		for _, f := range services {
			for _, key := range f.keys() {
				if v, ok := m[key]; ok {
					conn.Destination[key] = v
				}
			}
		}
	}
	return conn
}

// ServerID returns the Seerr server id configured under key: nil for Seerr's
// default, or an error when the value is not a whole number of 0 or more.
func (c Connection) ServerID(key string) (*int, error) {
	return parseID(c.Destination[key], "server")
}

// ProfileID returns the quality profile id configured under key: nil for the
// server's own profile, or an error when the value is not a whole number of 0
// or more.
func (c Connection) ProfileID(key string) (*int, error) {
	return parseID(c.Destination[key], "quality profile")
}

// RootFolder returns the root folder path configured under key: empty for the
// server's own folder, or an error when the value is not a path.
func (c Connection) RootFolder(key string) (string, error) {
	switch x := c.Destination[key].(type) {
	case nil:
		return "", nil
	case string:
		s := strings.TrimSpace(x)
		if strings.EqualFold(s, seerrDefault) {
			return "", nil
		}
		return s, nil
	default:
		return "", fmt.Errorf("%v is not a root folder path", x)
	}
}

// parseID reads an id select. The admin form stores select values as strings;
// a number is accepted too, for configs written through the API. what names
// the thing the id belongs to, for the error.
func parseID(v any, what string) (*int, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		s := strings.TrimSpace(x)
		if s == "" || strings.EqualFold(s, seerrDefault) {
			return nil, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%q is not a Seerr %s id", x, what)
		}
		return &n, nil
	case float64:
		if x < 0 || x != math.Trunc(x) || x > math.MaxInt32 {
			return nil, fmt.Errorf("%v is not a Seerr %s id", x, what)
		}
		n := int(x)
		return &n, nil
	default:
		return nil, fmt.Errorf("%v is not a Seerr %s id", x, what)
	}
}
