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

<img width="1024" height="768" alt="amber" src="https://github.com/user-attachments/assets/1552c692-ceb1-407e-a90f-b1d1342596d1" />

## Install

You need Go 1.21+, a Kindle with KUAL and USBNetwork, and SSH access to it as root with
a key in USBNetwork's `authorized_keys` (the Makefile uses `~/.ssh/id_ed25519`; pass
`KEY=...` and `HOST=...` to change that).

```bash
cp examples/web-analytics.json amber.json   # or examples/app-store.json (App Store Connect + Search Console sources),
                                            # or examples/rotating.json (both, as two rotating screens)
make deploy                                 # binary, KUAL menu, and amber.json if the device has none
make start                                  # or KUAL → Amber → Start dashboard
make autostart-on                           # start on every boot
```

Without an API key Amber shows demo data and prints the web UI address and PIN at the
bottom of the screen. Open it, set `posthog.project` and `posthog.api_key` (a personal
API key with the **Query: Read** scope), and save.

On the Kindle, **hold a finger on the screen for 3 seconds** to exit to the reader.

Autostart runs once per boot: it waits for the home screen to finish loading and for
Wi-Fi (up to two minutes), then takes over the screen. Amber re-enables Wi-Fi by itself if it drops. The
log is `extensions/amber/amber.log` on the Kindle's USB storage. To skip it without a computer, put an empty file named `AMBER_DISABLE` in the root
of the Kindle's USB storage. `make autostart-off` removes the job.

## Web UI

`http://<kindle-ip>:8080`, PIN from the screen. Edit `amber.json`, **Preview** renders
the unsaved config with live data (◀ ▶ pick the screen when there are several), **Save & apply** writes it and redraws the Kindle,
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
  "rotate_minutes": 5,
  "power": { "mode": "auto", "battery_refresh_minutes": 30 },
  "posthog": { "host": "https://eu.posthog.com", "project": "12345", "api_key": "phx_..." },
  "web": { "enabled": true, "port": 8080, "pin": "4821" },
  "rows": [ ... ]            // one screen; or, for several:
  "screens": [ { "title": "MY SITE", "rows": [ ... ] }, { "title": "MY APP", "rows": [ ... ] } ]
}
```

### Screens

`screens` holds several full layouts that take turns on the panel, `rotate_minutes`
each (default 5). Use either `rows` for a single screen or `screens`, not both. A
screen's `title` replaces the config title in the header, and dots next to it mark
which screen is up. A tap on the touchscreen flips to the next one.

Rotating and fetching are separate: every update queries the data of all screens at
once, and switching screens only redraws from those results, without Wi-Fi. Data is
still fetched every `refresh_minutes` (or `battery_refresh_minutes` on battery).

### Orientation

`"orientation": "landscape"` draws an 800 x 600 frame and turns it onto the panel for a
Kindle stood on its side, turned to the left; `"landscape_right"` turns it the other way.
Rows then have 500 px instead of 700, and a row fits four stat tiles comfortably — see
[examples/web-analytics-landscape.json](examples/web-analytics-landscape.json). The web
UI always shows the frame upright.

### Power

A Kindle that never sleeps lasts about 17 hours on a charge; the radio and the SoC
idling awake cost far more than the queries. With `power.mode` set to `auto` (the
default) Amber updates every `refresh_minutes` while charging, and on battery it
suspends between updates, waking on an RTC alarm every `battery_refresh_minutes`. The
E Ink panel keeps the last frame without power, and the big time in the header is
when that frame was fetched. `awake` never sleeps; `sleep` always does.

With several screens Amber also wakes every `rotate_minutes` to draw the next screen
from the cache and goes straight back to sleep, so the time a screen stays up is
time asleep.

While asleep the web UI is unreachable. Amber stays awake for three minutes after it
starts, and for five minutes after the power button wakes it early.

### Layout

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
go run . -config examples/rotating.json -demo -png build/preview.png -screen 2   # one of several screens
make preview                               # same with live data from amber.json
go run . -config amber.json -demo          # web UI on :8080 without a Kindle
make log                                   # tail the log on the device
make pull-config                           # fetch amber.json after editing it in the web UI
```
