# Amber

Amber turns a jailbroken Kindle into an always-on PostHog dashboard. It runs on the
Kindle itself: a single static Go binary queries PostHog over Wi-Fi, renders a
600 × 800 grayscale frame — KPI tiles, sparklines, charts, ranked bars — and paints
it with FBInk. No server, no laptop, no virtual display.

The screen is described in `amber.json` as rows of generic widgets, each fed by a
HogQL query, so anything you can query in PostHog — product events, web analytics,
data warehouse tables such as App Store Connect or Search Console — can go on the
wall. The file can be edited from a web UI that the Kindle serves on your Wi-Fi.

Tested on a Kindle Basic 2 (KT3, 600 × 800), firmware 5.16.2.1.1, WinterBreak + KUAL +
USBNetwork (NiLuJe).

## Install

You need Go 1.21+, a Kindle with KUAL and USBNetwork, and SSH access to it as root with
a key in USBNetwork's `authorized_keys` (the Makefile uses `~/.ssh/id_ed25519`; pass
`KEY=...` and `HOST=...` to change that).

```bash
cp examples/web-analytics.json amber.json   # or examples/app-store.json (App Store Connect + Search Console sources)
make deploy                                 # binary, KUAL menu, and amber.json if the device has none
make start                                  # or KUAL → Amber → Start dashboard
make autostart-on                           # start on every boot
```

Without an API key Amber shows demo data and prints the web UI address and PIN at the
bottom of the screen. Open it, set `posthog.project` and `posthog.api_key` (a personal
API key with the **Query: Read** scope), and save.

On the Kindle: **tap** to sync now, **hold for 3 seconds** to exit to the reader.

Autostart runs once per boot: it waits for the home screen to finish loading and for
Wi-Fi (up to two minutes), then takes over the screen. Amber re-enables Wi-Fi by itself if it drops. The
log is `extensions/amber/amber.log` on the Kindle's USB storage. To skip it without a computer, put an empty file named `AMBER_DISABLE` in the root
of the Kindle's USB storage. `make autostart-off` removes the job.

## Web UI

`http://<kindle-ip>:8080`, PIN from the screen. Edit `amber.json`, **Preview** renders
the unsaved config with live data, **Save & apply** writes it and redraws the Kindle,
**Live screen** mirrors what the panel shows. The API key is never sent back to the
browser.

The UI is plain HTTP on your local network, protected only by the PIN; do not expose the
port beyond it.

## amber.json

```jsonc
{
  "title": "MY SITE",
  "timezone": "Europe/Berlin",
  "refresh_minutes": 10,
  "posthog": { "host": "https://eu.posthog.com", "project": "12345", "api_key": "phx_..." },
  "web": { "enabled": true, "port": 8080, "pin": "4821" },
  "rows": [ ... ]
}
```

The screen below the header has 700 px for rows; each row has a `height` and rows are
8 px apart. A row with a `title` is one framed panel (with an optional `meta` caption or
`meta_query` returning one string) whose `cells` sit side by side; a row without a title
draws every cell as its own card. `span` sets relative widths.

Queries are HogQL, written as a string or an array of lines. `{timezone}` is replaced by
the configured timezone.

| type | query returns | options |
| --- | --- | --- |
| `stat` | `query`: one row `value, previous?, series[]?`; `series`: rows `(day, value)` | `style`: `tile` (default for cards), `inline` (default in panels), `readout`; `lower_is_better` |
| `chart` | rows `(label, a, b?)` | `style`: `area_line` (a as area, b as line) or `columns`; `label_every`, `highlight_last`, `points` |
| `bars` | rows `(label, value)` or `(label, value, previous)` | the three-column form compares periods |
| `segments` | rows `(label, value)` or one row with named columns | |
| `stack` | — | `items`: widgets stacked vertically, `span` is relative height |

Common options: `title`, `span`, `format` (`count`, `decimal1`, `decimal2`, `money`,
`percent`), `cache_minutes` (reuse a result; daily warehouse data needs 60+), `points`.

A `stat` with only `series` compares the sum of the second half of the series with the
first half, which is what you want for counts over "last 7 days vs previous 7". Date
series get missing days filled with zeros, ending at the last date the query returned —
so sources that lag a day or two compare complete weeks.

A failed query keeps showing its last good data and marks the footer; a query that has
never succeeded shows an error box in its widget.

## Development

```bash
make test
make demo CONFIG=examples/app-store.json      # renders build/preview.png with made-up data
make preview                               # same with live data from amber.json
go run . -config amber.json -demo          # web UI on :8080 without a Kindle
make log                                   # tail the log on the device
make pull-config                           # fetch amber.json after editing it in the web UI
```
