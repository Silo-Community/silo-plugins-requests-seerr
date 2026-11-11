# Silo Requests: Seerr

A Silo `request_router.v1` plugin that fulfills content requests by submitting
them to a [Seerr](https://github.com/seerr-team/seerr) (Overseerr/Jellyseerr-
compatible) instance. Seerr manages its own Sonarr/Radarr; this plugin is a thin
adapter.

## Connection config

Each connection carries a Seerr **base URL** + **API key** (host chrome) and
these plugin settings:

- **Attribute requests to** (`requester_mode`): the API-key owner, or the Seerr
  user matching the Silo requester's email (created if missing).
- **This Seerr has a separate 4K server** (`supports_4k`): whether a 4K copy
  is its own request to Seerr's 4K server. See
  [How requests map](#how-requests-map).
- **Movies** and **Series**: the Radarr or Sonarr server, quality profile and
  root folder requests get. See
  [Choosing the server, quality profile and root folder](#choosing-the-server-quality-profile-and-root-folder).

## API key requirement

The API key (Settings → General in Seerr) **must belong to a Seerr admin /
auto-approve user**. Silo is the sole approval authority; requests created via
the API auto-approve and hand off to Seerr's Sonarr/Radarr immediately. A
non-admin key would leave requests pending in Seerr (visible as `queued` in Silo
that never advances). `TestConnection` (`GET /api/v1/auth/me`) surfaces an
invalid key.

## How requests map

How a title's qualities become Seerr requests depends on **This Seerr has a
separate 4K server** (`supports_4k`):

- **Off** (the default): each title becomes **one** standard Seerr request
  (`is4k: false`), whatever qualities Silo asks for. A Radarr or Sonarr server
  holds one copy of a title and its quality profile decides the resolution, so
  one server can serve both HD and 4K: pick a quality profile that allows 4K.
  When Silo asks for an HD and a 4K copy, both of Silo's targets follow that
  one request and complete together.
- **On**: each quality is its own Seerr request. The 2160p one is sent with
  `is4k: true` and goes to the Radarr or Sonarr that Seerr marks as 4K, so
  Seerr tracks both versions of a title. This is what earlier versions did,
  and a connection that had the switch on keeps working as before.

Turn the switch on only if Seerr has a 4K server: Seerr refuses an
`is4k: true` request without one.

- `series` → Seerr `mediaType: "tv"` with `seasons: "all"`; movies use
  `mediaType: "movie"`. Media is identified by **TMDB id**.
- A duplicate (HTTP 409) is treated as already-queued; the plugin recovers the
  existing Seerr request id of that tier so Silo can track it. The existing
  request keeps the server, profile and folder it was made with.

## Choosing the server, quality profile and root folder

Seerr sends a request to its default Radarr or Sonarr server, with the quality
profile and root folder set for that server in Seerr, unless the request names
others. The connection's **Movies** and **Series** sections name them:

| Field | Applies to | Sent to Seerr as |
| --- | --- | --- |
| `radarr_server_id` | movies | `serverId` |
| `radarr_profile_id` | movies | `profileId` |
| `radarr_root_folder` | movies | `rootFolder` |
| `sonarr_server_id` | series | `serverId` |
| `sonarr_profile_id` | series | `profileId` |
| `sonarr_root_folder` | series | `rootFolder` |
| `radarr_4k_server_id`, `radarr_4k_profile_id`, `radarr_4k_root_folder` | 4K movie requests, with a separate 4K server | `serverId`, `profileId`, `rootFolder` |
| `sonarr_4k_server_id`, `sonarr_4k_profile_id`, `sonarr_4k_root_folder` | 4K series requests, with a separate 4K server | `serverId`, `profileId`, `rootFolder` |

- Each select lists what Seerr has. **Seerr default** (the value `default`)
  keeps Seerr's own choice and sends nothing; its label shows what that
  currently is. On a route, it forces Seerr's own choice for that rule.
- Quality profiles and root folders belong to a server. The selects list those
  of the server picked in the same section, or of Seerr's default server. After
  picking another server, test the connection to load its profiles and folders.
- Seerr's override rules still run, but only fill what a request leaves unset:
  a profile or folder chosen here wins over a rule.
- A profile or folder chosen for Sonarr also applies to anime, in place of the
  anime profile and folder set in Seerr. Use a Silo routing rule on anime to
  send anime elsewhere.
- Silo request routes can override these fields per rule. A rule that matches
  a genre, a requester or older titles can pick another profile or folder
  without a second Seerr. Silo's rule editor shows these fields on every
  rule: a Movies rule uses only the Radarr fields and a Series rule only the
  Sonarr ones, so leave the others on Server default. Silo asks the plugin
  once per quality tier. Without a separate 4K server, a rule whose HD and 4K
  destinations differ gets the one that is asked first; give both the same
  settings.
- A server Seerr does not have, a server of the other tier (a 4K server for
  standard requests or the reverse), or a profile or folder the server does
  not have, fails the request's targets with a message saying
  which field is wrong. Seerr would otherwise use its own default without
  saying so. Saving the connection checks the picks when Seerr responds; an
  unreachable Seerr does not block saving. Optional lookups share a ten-second
  budget and return earlier when the caller's deadline is shorter.
- The plugin's `TestConnection` RPC also checks the picks. Silo's current web
  **Test** button loads the choices instead, so its success message does not
  confirm that the selected values are valid or Seerr is reachable.
- Checking a profile or folder asks Seerr for the server's details, and Seerr
  asks Radarr or Sonarr. A request therefore fails, and Silo retries it, while
  that server is down. A connection left on Seerr default makes no such call.
- An existing request is reused only when its stored server id matches an
  explicit selection. A different or unknown destination fails with a message;
  changing a route does not move a request already submitted to Seerr.
- Seerr can reuse an id after a server is deleted. Check the connection and
  route selections whenever you replace a Radarr or Sonarr server in Seerr.

## Who chooses the profile and folder

By default every request gets the profile and folder set on the connection, or
a routing rule's. Next to each of those four settings is a **chosen by** select
(`radarr_profile_chosen_by`, `radarr_folder_chosen_by`,
`sonarr_profile_chosen_by`, `sonarr_folder_chosen_by`):

- **Admin (the setting above)**: nobody picks; the setting applies. This is the
  default.
- **Requester**: Silo's request dialog offers every quality profile (or root
  folder) of the Radarr or Sonarr server the request would go to. An admin can
  change the pick when approving.
- **Admin on approval**: the admin picks when approving the request.

A pick replaces the connection's setting and a routing rule's for that
request. The setting itself stays the preselected value, and it applies when
nobody picks: requests from a result card or the watchlist, requests approved
automatically, and several requests approved at once. A routing rule can
override who chooses, like any other select.

- This needs a Silo server that supports request options (its
  `GET /api/v2/requests/status` reports `request_options_supported`). An older
  server ignores these selects.
- Silo checks a pick against the lists the plugin reports, so a requester can
  only send a profile or folder Seerr lists for that server. The plugin checks
  it again when it submits.
- With **Requester**, everyone who may request sees the choices, including
  folder paths. Keep the folder on **Admin** or **Admin on approval** if paths
  should stay private.
- Silo offers the choice when it can name the one server a title goes to: a
  single Seerr connection, or the server a routing rule sends the title's HD
  copy to. Requests made from a card, and from a client that does not know
  request options, use the connection's settings.
- To let only some people choose, or to choose for them, use routing rules on
  the requesting account instead.

## Download progress

While a request is queued or downloading, the plugin reports how far its
downloads are: a phase, the bytes left, and the estimated completion time.
Seerr supplies them on the request's media (`downloadStatus`, or
`downloadStatus4k` for a 4K request). Its Download Sync job refreshes them from
the Radarr and Sonarr queues, every minute by default.

- Turn on **Enable Scan** for each Radarr and Sonarr server configured in
  Seerr. Download Sync polls only those servers, so Silo shows no progress for
  the others.
- Seerr reads only the first 10 items of each queue
  ([seerr-team/seerr#3360](https://github.com/seerr-team/seerr/issues/3360)).
  On a busy queue a download can be missing, so progress is best effort.
- A season pack counts once, although Sonarr lists it under every episode.
- While any of a request's downloads has no known size yet, the size and bytes
  left are reported as 0, so Silo shows no percentage rather than an
  overstated one.
- Seerr keeps each download's queue status but not Radarr and Sonarr's import
  state. A finished download shows as importing even when its import needs
  attention in Radarr or Sonarr.

## Build / test

```
go build ./... && go test ./...
```

## Community maintenance

This is an approved community plugin maintained in the
[`Silo-Community`](https://github.com/Silo-Community) organization. Use
[GitHub Issues](https://github.com/Silo-Community/silo-plugins-requests-seerr/issues)
for support and bug reports. Security reports should follow
[`SECURITY.md`](SECURITY.md).

The plugin consumes the published
[`silo-plugin-sdk`](https://github.com/Silo-Server/silo-plugin-sdk); CI rejects
machine-local SDK replacement directives.

## License

Licensed under AGPL-3.0. See [`LICENSE`](LICENSE).
