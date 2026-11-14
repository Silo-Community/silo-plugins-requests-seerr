package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"

	"github.com/Silo-Community/silo-plugins-requests-seerr/internal/seerr"
)

// Optional lookups share a short budget so an unreachable Seerr cannot consume
// the host's RPC deadline. Reserve half the remaining time for the response.
func serverLookupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := 10 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)/2)
	}
	return context.WithTimeout(ctx, budget)
}

// kindLabel is a service kind as people write it.
func kindLabel(kind string) string {
	if kind == seerr.ServiceSonarr {
		return "Sonarr"
	}
	return "Radarr"
}

// serverLister reads one Seerr connection's servers per kind, and a server's
// profiles and folders, at most once each.
type serverLister struct {
	client  *httpclient.Client
	servers map[string]listResult
	details map[string]detailsResult
}

type listResult struct {
	servers []seerr.Server
	err     error
}

type detailsResult struct {
	details *seerr.ServerDetails
	err     error
}

func newServerLister(client *httpclient.Client) *serverLister {
	return &serverLister{client: client, servers: map[string]listResult{}, details: map[string]detailsResult{}}
}

func (l *serverLister) list(ctx context.Context, kind string) ([]seerr.Server, error) {
	r, ok := l.servers[kind]
	if !ok {
		r.servers, r.err = seerr.ListServers(ctx, l.client, kind)
		l.servers[kind] = r
	}
	return r.servers, r.err
}

func (l *serverLister) get(ctx context.Context, kind string, id int) (*seerr.ServerDetails, error) {
	key := fmt.Sprintf("%s/%d", kind, id)
	r, ok := l.details[key]
	if !ok {
		r.details, r.err = seerr.GetServerDetails(ctx, l.client, kind, id)
		l.details[key] = r
	}
	return r.details, r.err
}

// checkServer reports whether id names a server of the field's kind and
// tier. Seerr sends a request for an unknown id, or for a server of the other
// tier, to its default server without saying so, so both are refused here.
func checkServer(servers []seerr.Server, f serviceFields, id int) error {
	for _, s := range servers {
		if s.ID != id {
			continue
		}
		switch {
		case s.Is4K && !f.Is4K:
			return fmt.Errorf("%s is set to Seerr's %s server %q (id %d), which Seerr marks as a 4K server; it takes standard requests, so pick a server that is not marked 4K", f.Server, kindLabel(f.Kind), s.Name, id)
		case !s.Is4K && f.Is4K:
			return fmt.Errorf("%s is set to Seerr's %s server %q (id %d), which Seerr does not mark as a 4K server; it takes 4K requests, so pick a server that is marked 4K", f.Server, kindLabel(f.Kind), s.Name, id)
		}
		return nil
	}
	return fmt.Errorf("%s is set to %s server id %d, which Seerr does not have", f.Server, kindLabel(f.Kind), id)
}

// defaultServer returns the server Seerr sends a request of the tier to when
// the request names none.
func defaultServer(servers []seerr.Server, is4k bool) *seerr.Server {
	for i := range servers {
		if servers[i].IsDefault && servers[i].Is4K == is4k {
			return &servers[i]
		}
	}
	return nil
}

// destination is where one request goes. A nil or empty field leaves Seerr's
// own choice.
type destination struct {
	ServerID   *int
	ProfileID  *int
	RootFolder string
}

// destinationCheck is a service's destination as configured, with what is
// wrong with it. Both maps are keyed by config key.
type destinationCheck struct {
	dest destination
	// fieldErrs holds values that are not valid, or that Seerr does not have.
	fieldErrs map[string]string
	// unreachable holds why Seerr could not be asked about a value.
	unreachable map[string]string
}

// message joins everything wrong, in form order; empty when the destination
// can be used.
func (c destinationCheck) message(f serviceFields) string {
	var out []string
	for _, key := range f.keys() {
		if m, ok := c.fieldErrs[key]; ok {
			out = append(out, m)
		} else if m, ok := c.unreachable[key]; ok {
			out = append(out, m)
		}
	}
	return strings.Join(out, "; ")
}

// checkDestination reads one service's destination from conn. Values that are
// not ids or paths are always reported. When checkSeerr is set, the server
// must be one of Seerr's standard servers of the kind, and the profile and
// folder must exist on the server the request will go to: the chosen one, or
// Seerr's default.
func checkDestination(ctx context.Context, l *serverLister, conn Connection, f serviceFields, checkSeerr bool) destinationCheck {
	c := destinationCheck{fieldErrs: map[string]string{}, unreachable: map[string]string{}}
	var err error
	if c.dest.ServerID, err = conn.ServerID(f.Server); err != nil {
		c.fieldErrs[f.Server] = fmt.Sprintf("%s: %v", f.Server, err)
	}
	if c.dest.ProfileID, err = conn.ProfileID(f.Profile); err != nil {
		c.fieldErrs[f.Profile] = fmt.Sprintf("%s: %v", f.Profile, err)
	}
	if c.dest.RootFolder, err = conn.RootFolder(f.RootFolder); err != nil {
		c.fieldErrs[f.RootFolder] = fmt.Sprintf("%s: %v", f.RootFolder, err)
	}
	wantsOptions := c.dest.ProfileID != nil || c.dest.RootFolder != ""
	if !checkSeerr || (c.dest.ServerID == nil && !wantsOptions) {
		return c
	}

	label := kindLabel(f.Kind)
	servers, err := l.list(ctx, f.Kind)
	if err != nil {
		why := fmt.Sprintf("could not list Seerr's %s servers to check %%s: %v", label, err)
		if c.dest.ServerID != nil {
			c.unreachable[f.Server] = fmt.Sprintf(why, f.Server)
		}
		if c.dest.ProfileID != nil {
			c.unreachable[f.Profile] = fmt.Sprintf(why, f.Profile)
		}
		if c.dest.RootFolder != "" {
			c.unreachable[f.RootFolder] = fmt.Sprintf(why, f.RootFolder)
		}
		return c
	}

	// The server the request goes to decides which profiles and folders exist.
	var target *seerr.Server
	if c.dest.ServerID != nil {
		if err := checkServer(servers, f, *c.dest.ServerID); err != nil {
			c.fieldErrs[f.Server] = err.Error()
			return c
		}
		for i := range servers {
			if servers[i].ID == *c.dest.ServerID {
				target = &servers[i]
			}
		}
	} else {
		target = defaultServer(servers, f.Is4K)
	}
	if !wantsOptions {
		return c
	}
	if target == nil {
		why := fmt.Sprintf("%%s is set, but Seerr has no default %s server for this tier to take it from; pick one in %s", label, f.Server)
		if c.dest.ProfileID != nil {
			c.fieldErrs[f.Profile] = fmt.Sprintf(why, f.Profile)
		}
		if c.dest.RootFolder != "" {
			c.fieldErrs[f.RootFolder] = fmt.Sprintf(why, f.RootFolder)
		}
		return c
	}

	details, err := l.get(ctx, f.Kind, target.ID)
	if err != nil {
		why := fmt.Sprintf("could not read the profiles and folders of Seerr's %s server %q to check %%s: %v", label, target.Name, err)
		if c.dest.ProfileID != nil {
			c.unreachable[f.Profile] = fmt.Sprintf(why, f.Profile)
		}
		if c.dest.RootFolder != "" {
			c.unreachable[f.RootFolder] = fmt.Sprintf(why, f.RootFolder)
		}
		return c
	}
	if id := c.dest.ProfileID; id != nil && !hasProfile(details.Profiles, *id) {
		c.fieldErrs[f.Profile] = fmt.Sprintf("%s is set to quality profile id %d, which Seerr's %s server %q does not have", f.Profile, *id, label, target.Name)
	}
	if path := c.dest.RootFolder; path != "" && !hasRootFolder(details.RootFolders, path) {
		c.fieldErrs[f.RootFolder] = fmt.Sprintf("%s is set to %q, which is not a root folder of Seerr's %s server %q", f.RootFolder, path, label, target.Name)
	}
	return c
}

func hasProfile(profiles []seerr.Profile, id int) bool {
	for _, p := range profiles {
		if p.ID == id {
			return true
		}
	}
	return false
}

func hasRootFolder(folders []seerr.RootFolder, path string) bool {
	for _, f := range folders {
		if f.Path == path {
			return true
		}
	}
	return false
}

// destinationOptions builds the dynamic options for every destination field
// from Seerr. Each list starts with "default", named after what Seerr would
// use when it can be told. A server list holds only servers of its tier. Profile
// and folder lists come from the server picked in conn, or Seerr's default
// when none (or an unusable one) is picked. A field whose choices cannot be
// read is left out, so the form falls back to its static "Seerr default"
// option.
func destinationOptions(ctx context.Context, l *serverLister, conn Connection) map[string]*pluginv1.ConfigOptionList {
	out := map[string]*pluginv1.ConfigOptionList{}
	// The 4K selects are hidden without a separate 4K server.
	for _, f := range servicesOf(conn) {
		servers, err := l.list(ctx, f.Kind)
		if err != nil {
			continue
		}
		def := defaultServer(servers, f.Is4K)
		target := def
		defName := ""
		if def != nil {
			defName = def.Name
		}
		serverOpts := []*pluginv1.ConfigOption{{Value: seerrDefault, Label: defaultLabel(defName)}}
		picked, _ := conn.ServerID(f.Server)
		for i, s := range servers {
			if s.Is4K != f.Is4K {
				continue
			}
			serverOpts = append(serverOpts, &pluginv1.ConfigOption{Value: itoa(s.ID), Label: s.Name})
			if picked != nil && s.ID == *picked {
				target = &servers[i]
			}
		}
		out[f.Server] = &pluginv1.ConfigOptionList{Options: serverOpts}

		if target == nil {
			continue
		}
		details, err := l.get(ctx, f.Kind, target.ID)
		if err != nil {
			continue
		}
		var activeProfile string
		profileOpts := []*pluginv1.ConfigOption{nil}
		for _, p := range details.Profiles {
			if p.ID == target.ActiveProfileID {
				activeProfile = p.Name
			}
			profileOpts = append(profileOpts, &pluginv1.ConfigOption{Value: itoa(p.ID), Label: p.Name})
		}
		profileOpts[0] = &pluginv1.ConfigOption{Value: seerrDefault, Label: defaultLabel(activeProfile)}
		out[f.Profile] = &pluginv1.ConfigOptionList{Options: profileOpts}

		folderOpts := []*pluginv1.ConfigOption{{Value: seerrDefault, Label: defaultLabel(target.ActiveDirectory)}}
		for _, rf := range details.RootFolders {
			if rf.Path == "" || strings.EqualFold(rf.Path, seerrDefault) {
				continue
			}
			folderOpts = append(folderOpts, &pluginv1.ConfigOption{Value: rf.Path, Label: folderLabel(rf)})
		}
		out[f.RootFolder] = &pluginv1.ConfigOptionList{Options: folderOpts}
	}
	return out
}

// defaultLabel names the "default" option, with what Seerr uses when known.
func defaultLabel(name string) string {
	if name == "" {
		return "Seerr default"
	}
	return fmt.Sprintf("Seerr default (%s)", name)
}

// folderLabel is a root folder's path with its free space, when Seerr reports it.
func folderLabel(rf seerr.RootFolder) string {
	if rf.FreeSpace <= 0 {
		return rf.Path
	}
	const gib = 1 << 30
	if rf.FreeSpace >= 1024*gib {
		return fmt.Sprintf("%s (%.1f TB free)", rf.Path, rf.FreeSpace/(1024*gib))
	}
	return fmt.Sprintf("%s (%.0f GB free)", rf.Path, rf.FreeSpace/gib)
}

// configuredDestinationErrors checks every destination value on conn, in form
// order. invalid lists values that are wrong; unreachable lists why Seerr
// could not be asked about the others (only when checkSeerr is set).
func configuredDestinationErrors(ctx context.Context, l *serverLister, conn Connection, checkSeerr bool) (fieldErrs map[string]string, invalid, unreachable []string) {
	fieldErrs = map[string]string{}
	// The 4K fields are hidden, and not used, without a separate 4K server.
	for _, f := range servicesOf(conn) {
		c := checkDestination(ctx, l, conn, f, checkSeerr)
		for _, key := range f.keys() {
			if m, ok := c.fieldErrs[key]; ok {
				fieldErrs[key] = m
				invalid = append(invalid, m)
			} else if m, ok := c.unreachable[key]; ok {
				unreachable = append(unreachable, m)
			}
		}
	}
	return fieldErrs, invalid, unreachable
}
