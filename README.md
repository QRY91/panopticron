# panopticron

One wall for my web real estate. Every domain I run is a cluster of coloured
tiles — does the name resolve, does the page answer, is the certificate healthy,
is the registration about to lapse, did the last deploy land, is CI green — and
one integer sorts the wall, most urgent first. A human can override that
integer, visibly. The introduction lives at **[panopticron.com](https://panopticron.com)**;
the wall itself goes up at `wall.panopticron.com` once it has a home. It watches
its own siblings.

*Panopticon, inverted: garage door open, everyone watches together.
Transparency, not surveillance.*

## Run

```
go build -o panopticron .      # CGO_ENABLED=0 works: pure-Go SQLite
./panopticron                  # serves :8080 from projects.toml + panopticron.db
./panopticron -once            # run every poller once and exit
./panopticron -dev             # reload templates/css from disk per request
go test ./...
```

Environment, all optional:

| var | effect |
|---|---|
| `GITHUB_TOKEN` | 5000 GitHub requests/h instead of 60; private repos |
| `CLOUDFLARE_API_TOKEN` + `CLOUDFLARE_ACCOUNT_ID` | enables the `pages` lens (token needs Pages:Read) |
| `PANOPTICRON_ADMIN_PASSWORD` | enables manual overrides (HTTP Basic auth on the one POST) |

`projects.toml` is what it watches — `name`, `url`, `domain`, `repo`, `pages`
per project; see the file. Membership lives there, versioned; state and history
live in the SQLite file. `deploy/panopticron.service` is a systemd unit. `site/`
is the static introduction page served at panopticron.com by Cloudflare Pages
(no build step).

## How it works

```
projects.toml ──► store.syncProjects ──► projects (id, name, score, override, sort_key)
                                             ▲
pollers (poll.go), each on its own ticker:   │ rescore() after every visit
  probe   5m   dns + http + tls  (checks.go) │
  domain  24h  RDAP expiry        (rdap.go)  ├──► lenses  latest per (project, kind)
  ci      15m  GitHub Actions   (github.go)  │
  pages   10m  Cloudflare Pages (cfpages.go) └──► events  append-only, on change only
                                                   runs    one row per poller run  → /status
web.go:  GET /           the wall: LensClusters sorted by sort_key
         GET /p/{name}   lenses, lifeline (SVG of priority over time), events, override form
         POST /p/{name}/override   the one write, Basic-auth'd
```

- **Lens** (`lens.go`) — one observation: kind, status (good/warn/error/neutral),
  a ≤5-char tile value, a one-line detail, a link.
- **Cluster** (`lens.go`) — the 2025 LensCluster layout: sort by (status, kind),
  top three become 76px tiles, next four nest in a 32px 2×2, pad to a fixed
  164px frame. Problems are always the big tiles; you scan the wall by colour.
- **Score** (`score.go`) — start at 10000, subtract a malus per problem
  (http/dns down −8000, tls −7000, domain −5000, pages failed −2000, ci −500,
  warnings −300…), floor 1; `sort_key = COALESCE(override, score)`. Pure
  function, table-tested.
- **Events** — one append-only table for lens status changes *and* priority
  changes; the priority rows are the lifeline.
- **No JavaScript.** The wall `<meta refresh>`es every minute; the lifeline is
  server-rendered SVG.

## Scope freeze

Written before the first line, because the 2025 version died of features. Not
in this repo, on purpose: a login system (one Basic-auth password guards the one
write), themes (one, Zenburn), client-side JavaScript, an ORM or migration
framework (one `schema` string), a mock-data layer (the config points at real
sites), dashboards-about-the-dashboard (`/status` is a table of poller runs),
metrics time series (events are recorded on change only), notifications,
multi-user anything. If one of these is ever actually needed, only that one
gets built.

## Lineage

This is the lean rebuild of a 2025 internship project (Next 14 + Refine + MUI +
Supabase, 13,248 lines, 90 files, which watched an agency's Vercel fleet) — see
[panopticron-demo](https://github.com/QRY91/panopticron-demo) and the
[write-up](https://qry.zone/panopticron/). The ideas survived — LensCluster,
the descending-malus priority score with a visible manual override, the
lifeline, the poller-run log — and the rubric machinery did not. Same signal on
screen; the whole thing reads in a sitting.
