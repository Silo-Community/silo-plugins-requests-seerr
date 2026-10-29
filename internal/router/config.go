package router

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Community/silo-plugins-requests-seerr/internal/seerr"
)

// Server-selection config keys. Each names the Seerr Radarr/Sonarr server one
// (media type, quality tier) goes to; unset or empty leaves Seerr's default
// server for that tier. A Silo route can override any of them per tier.
const (
	keyRadarrServer   = "radarr_server_id"
	keyRadarr4KServer = "radarr_4k_server_id"
	keySonarrServer   = "sonarr_server_id"
	keySonarr4KServer = "sonarr_4k_server_id"
)

// serverDefault is the select value for "Seerr default": send no serverId and
// let Seerr use its default server for the tier. It is not empty because
// Silo's route editor cannot show a select option whose value is empty.
const serverDefault = "default"

// serverField describes one server-selection key: which Seerr service kind it
// picks from and which tier it serves.
type serverField struct {
	Key  string
	Kind string // seerr.ServiceRadarr | seerr.ServiceSonarr
	Is4K bool
}

// serverFields lists every server-selection key, in form order.
var serverFields = []serverField{
	{keyRadarrServer, seerr.ServiceRadarr, false},
	{keyRadarr4KServer, seerr.ServiceRadarr, true},
	{keySonarrServer, seerr.ServiceSonarr, false},
	{keySonarr4KServer, seerr.ServiceSonarr, true},
}

// serverFieldFor returns the server-selection key for a Seerr media type
// ("movie" | "tv") and tier.
func serverFieldFor(mediaType string, is4k bool) serverField {
	kind := seerr.ServiceRadarr
	if mediaType == "tv" {
		kind = seerr.ServiceSonarr
	}
	for _, f := range serverFields {
		if f.Kind == kind && f.Is4K == is4k {
			return f
		}
	}
	panic("unreachable: every kind/tier pair has a server field")
}

// Connection is the per-call Seerr connection: base URL + API key (host chrome)
// plus plugin_config fields.
type Connection struct {
	ID         string
	BaseURL    string
	APIKey     string
	Supports4K bool

	Mapped            bool // requester_mode == "mapped"
	RequireMappedUser bool // mapped + can't map -> fail the request (else admin fallback)
	AutoApprove       bool // grant AUTO_APPROVE(+4K) to mapped users

	// Servers holds the raw server-selection values, keyed by config key.
	// Absent or empty means Seerr's default server for that tier.
	Servers map[string]any
}

// connectionFromRouter parses a host-supplied RouterConnection. base_url and
// api_key are chrome; the remaining fields come from the config struct.
func connectionFromRouter(c *pluginv1.RouterConnection) Connection {
	conn := Connection{
		ID:      c.GetId(),
		BaseURL: c.GetBaseUrl(),
		APIKey:  c.GetApiKey(),
		Servers: map[string]any{},
	}
	if cfg := c.GetConfig(); cfg != nil {
		m := cfg.AsMap()
		if v, ok := m["supports_4k"].(bool); ok {
			conn.Supports4K = v
		}
		if v, ok := m["requester_mode"].(string); ok {
			conn.Mapped = v == "mapped"
		}
		conn.RequireMappedUser, _ = m["require_mapped_user"].(bool)
		conn.AutoApprove, _ = m["auto_approve"].(bool)
		for _, f := range serverFields {
			if v, ok := m[f.Key]; ok {
				conn.Servers[f.Key] = v
			}
		}
	}
	return conn
}

// ServerID returns the Seerr server id configured under key: nil when unset,
// empty or "default" (use Seerr's default), or an error when the value is not
// a whole number of 0 or more. The admin form stores select values as
// strings; a number is accepted too, for configs written through the API.
func (c Connection) ServerID(key string) (*int, error) {
	return parseServerID(c.Servers[key])
}

func parseServerID(v any) (*int, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		s := strings.TrimSpace(x)
		if s == "" || strings.EqualFold(s, serverDefault) {
			return nil, nil
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%q is not a Seerr server id", x)
		}
		return &n, nil
	case float64:
		if x < 0 || x != math.Trunc(x) || x > math.MaxInt32 {
			return nil, fmt.Errorf("%v is not a Seerr server id", x)
		}
		n := int(x)
		return &n, nil
	default:
		return nil, fmt.Errorf("%v is not a Seerr server id", x)
	}
}
