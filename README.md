# Silo Requests: Seerr

A Silo `request_router.v1` plugin that fulfills content requests by submitting
them to a [Seerr](https://github.com/seerr-team/seerr) (Overseerr/Jellyseerr-
compatible) instance. Seerr manages its own Sonarr/Radarr; this plugin is a thin
adapter.

## Connection config

Each connection carries a Seerr **base URL** + **API key** (host chrome) and one
plugin setting:

- **This Seerr handles 4K requests** (`supports_4k`, default off) — enable only
  if the Seerr instance has a 4K Sonarr/Radarr configured. When off, 2160p
  requests are not sent to this connection.

## API key requirement

The API key (Settings → General in Seerr) **must belong to a Seerr admin /
auto-approve user**. Silo is the sole approval authority; requests created via
the API auto-approve and hand off to Seerr's Sonarr/Radarr immediately. A
non-admin key would leave requests pending in Seerr (visible as `queued` in Silo
that never advances). `TestConnection` (`GET /api/v1/auth/me`) surfaces an
invalid key.

## How requests map

- Each requested quality becomes one Seerr request: HD → `is4k:false`, 2160p →
  `is4k:true` (skipped when `supports_4k` is off).
- `series` → Seerr `mediaType: "tv"` with `seasons: "all"`; movies use
  `mediaType: "movie"`. Media is identified by **TMDB id**.
- A duplicate (HTTP 409) is treated as already-queued; the plugin recovers the
  existing Seerr request id so Silo can track it.
- Each target follows its own tier of the Seerr media: HD reads `status`, 4K
  reads `status4k`. A 4K target keeps downloading after the HD copy is
  available.

## Choosing Seerr's Radarr and Sonarr servers

A Seerr with more than one Radarr or Sonarr server sends every request to the
server it marks as default for that tier, unless the request names another.
The connection's **Radarr and Sonarr servers** section names one per media type
and tier:

| Field | Requests it routes |
| --- | --- |
| `radarr_server_id` | HD movies |
| `radarr_4k_server_id` | 4K movies |
| `sonarr_server_id` | HD series |
| `sonarr_4k_server_id` | 4K series |

- Each select lists the servers of its kind and tier that Seerr has. **Seerr
  default** (the value `default`) keeps Seerr's own choice, as before. On a
  route, it forces Seerr's own choice for that rule.
- The plugin sends only `serverId`. Seerr applies the chosen server's own
  quality profile, root folder and tags, and its override rules still run.
- Silo request routes can override these fields per rule. A rule that matches
  older movies, say, can send its HD tier to one Radarr server and its 4K tier
  to another, without a second Seerr. Silo's rule editor shows all four fields
  on every rule: a Movies rule uses only the Radarr fields and a Series rule
  only the Sonarr ones, so leave the others on Server default. The labels say
  which media type each field is for.
- A server id Seerr does not have, or a 4K server picked for HD (or the
  reverse), fails that target with a message saying which field is wrong.
  Seerr would otherwise send the request to its default server without saying
  so. Saving the connection and **Test connection** check the picks too; a
  Seerr that cannot be reached does not block saving.

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
