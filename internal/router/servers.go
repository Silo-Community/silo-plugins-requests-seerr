package router

import (
	"context"
	"fmt"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"

	"github.com/Silo-Community/silo-plugins-requests-seerr/internal/seerr"
)

// kindLabel is a service kind as people write it.
func kindLabel(kind string) string {
	if kind == seerr.ServiceSonarr {
		return "Sonarr"
	}
	return "Radarr"
}

// serverLister lists one Seerr connection's servers per kind, at most once per
// kind, so a Fulfill that sends HD and 4K to the same Seerr asks it once.
type serverLister struct {
	client *httpclient.Client
	cache  map[string][]seerr.Server
	errs   map[string]error
}

func newServerLister(client *httpclient.Client) *serverLister {
	return &serverLister{client: client, cache: map[string][]seerr.Server{}, errs: map[string]error{}}
}

func (l *serverLister) list(ctx context.Context, kind string) ([]seerr.Server, error) {
	if s, ok := l.cache[kind]; ok {
		return s, nil
	}
	if err, ok := l.errs[kind]; ok {
		return nil, err
	}
	s, err := seerr.ListServers(ctx, l.client, kind)
	if err != nil {
		l.errs[kind] = err
		return nil, err
	}
	l.cache[kind] = s
	return s, nil
}

// checkServer reports whether id names a server of the field's kind and tier.
// Seerr would otherwise send a request for an unknown id to its default server
// without saying so, and a 4K server would receive an HD request (or the
// reverse), so both are refused here.
func checkServer(servers []seerr.Server, f serverField, id int) error {
	for _, s := range servers {
		if s.ID != id {
			continue
		}
		switch {
		case s.Is4K && !f.Is4K:
			return fmt.Errorf("%s picks the server for HD requests, but Seerr's %s server %q (id %d) is a 4K server", f.Key, kindLabel(f.Kind), s.Name, id)
		case !s.Is4K && f.Is4K:
			return fmt.Errorf("%s picks the server for 4K requests, but Seerr's %s server %q (id %d) is not a 4K server", f.Key, kindLabel(f.Kind), s.Name, id)
		}
		return nil
	}
	return fmt.Errorf("%s is set to %s server id %d, which Seerr does not have", f.Key, kindLabel(f.Kind), id)
}

// resolveServer returns the server id a (media type, tier) target is sent to:
// nil for Seerr's default, or the configured id once Seerr confirms it exists
// and serves that tier.
func resolveServer(ctx context.Context, l *serverLister, conn Connection, mediaType string, is4k bool) (*int, error) {
	f := serverFieldFor(mediaType, is4k)
	id, err := conn.ServerID(f.Key)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Key, err)
	}
	if id == nil {
		return nil, nil
	}
	servers, err := l.list(ctx, f.Kind)
	if err != nil {
		return nil, fmt.Errorf("could not list Seerr's %s servers to check %s: %w", kindLabel(f.Kind), f.Key, err)
	}
	if err := checkServer(servers, f, *id); err != nil {
		return nil, err
	}
	return id, nil
}

// serverOptions builds the dynamic options for every server-selection field
// from Seerr's servers. Each list starts with "default" (Seerr's default,
// named when Seerr marks one) and holds only servers of the field's
// tier. A kind whose servers cannot be listed is left out, so the form falls
// back to the field's static "Seerr default" option.
func serverOptions(ctx context.Context, l *serverLister) map[string]*pluginv1.ConfigOptionList {
	out := map[string]*pluginv1.ConfigOptionList{}
	for _, f := range serverFields {
		servers, err := l.list(ctx, f.Kind)
		if err != nil {
			continue
		}
		def := "Seerr default"
		var opts []*pluginv1.ConfigOption
		for _, s := range servers {
			if s.Is4K != f.Is4K {
				continue
			}
			if s.IsDefault {
				def = fmt.Sprintf("Seerr default (%s)", s.Name)
			}
			opts = append(opts, &pluginv1.ConfigOption{Value: itoa(s.ID), Label: s.Name})
		}
		out[f.Key] = &pluginv1.ConfigOptionList{
			Options: append([]*pluginv1.ConfigOption{{Value: serverDefault, Label: def}}, opts...),
		}
	}
	return out
}

// configuredServerErrors checks every server-selection value on conn, keyed by
// config key. Values that are not server ids are always reported. When
// checkSeerr is set, ids are also checked against Seerr's servers; a kind that
// cannot be listed is skipped and reported in unreachable.
func configuredServerErrors(ctx context.Context, l *serverLister, conn Connection, checkSeerr bool) (fieldErrs map[string]string, unreachable []string) {
	fieldErrs = map[string]string{}
	for _, f := range serverFields {
		id, err := conn.ServerID(f.Key)
		if err != nil {
			fieldErrs[f.Key] = err.Error()
			continue
		}
		if id == nil || !checkSeerr {
			continue
		}
		servers, err := l.list(ctx, f.Kind)
		if err != nil {
			unreachable = append(unreachable, f.Key)
			continue
		}
		if err := checkServer(servers, f, *id); err != nil {
			fieldErrs[f.Key] = err.Error()
		}
	}
	return fieldErrs, unreachable
}
