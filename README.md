# rackwatch

:construction: UNDER CONSTRUCTION :construction:

A terminal dashboard showing CPU, RAM, disk, temperature and network for every host in the rack, each with an optional sparkline of recent history.

```text
 RACKWATCH   HOSTS 1/3 UP                                  22:54:18  q quit  r refresh
╭ rack-01 ──────────── UP ─╮╭ rack-02 ──────── STALE 42s ─╮╭ rack-03 ───────── DOWN ─╮
│ CPU      17%   ·▄▄▃▃▂▁▁▄ ││ CPU      80%   ·████······· ││ CPU       --          · │
│ RAM      61%   ████████  ││ RAM      90%   ██████······ ││ RAM       --          · │
…
╰──────────────────────────╯╰─ timeout ───────────────────╯╰─ unknown host rack-03.i…╯
```

## Constraints

- A 1424 × 280 screen in a 10-inch rack: about 178 × 17 cells with an 8 × 16 font.
- The screen is wide and short, so each host gets its own column. Sparklines grow to fill the height that's left.

## Running

```sh
cp config.toml.example config.toml   # then edit the hosts
go run .                               # reads ./config.toml
go run . -config path/to/config.toml
```

| Key | Action |
| --- | --- |
| `r` | Refresh now |
| `q`, `Esc`, `Ctrl-C` | Quit |

## Configuration

See [config.toml.example](config.toml.example) for a full example.

| Setting | Meaning |
| --- | --- |
| `dashboard.refresh_seconds` | How often to fetch metrics (default `2`) |
| `dashboard.stale_after` | How old data can get before a host shows as STALE, e.g. `"15s"` (default `"15s"`) |
| `dashboard.theme` | `"default"` or `"brotkasten"` (default `"default"`) |
| `dashboard.show_sparklines` | Show the history graphs (default `true`) |
| `thresholds.*_warning` | Values at or above these are highlighted. Temperature is in °C. |
| `hosts[].name` | Name shown on the panel |
| `hosts[].metrics_source` | `"local"`, or the URL of a [node_exporter](https://github.com/prometheus/node_exporter), e.g. `"http://host:9100/metrics"` |

String values need quotes in TOML: `theme = "brotkasten"`, not `theme = brotkasten`. The program stops at startup with the line number if the file doesn't parse, or if it has unknown keys or invalid values.

### Themes

- **`default`**: the C64 boot screen, with light blue on blue.
- **`brotkasten`**: the original "breadbin" C64 case. Dark brown background like the keycaps, beige-brown borders, cream text, tan sparklines, and a red DOWN badge like the power LED.

## Setting up remote hosts

Every host with a URL as its `metrics_source` must run [node_exporter](https://github.com/prometheus/node_exporter). It's the official Prometheus exporter for host metrics on Linux, but it isn't part of Prometheus and isn't installed by default on any distribution. Its default address, `:9100/metrics`, is the one the example config uses.

Other Prometheus-compatible exporters, such as Netdata or Telegraf, won't work. They use the same text format but different metric names, and the TUI looks for node_exporter's names, such as `node_cpu_seconds_total`. Hosts running them show as DOWN with "no node_exporter metrics in response".

| Distribution | Install | Service |
| --- | --- | --- |
| Arch Linux | `sudo pacman -S prometheus-node-exporter` | Enable it yourself: `sudo systemctl enable --now prometheus-node-exporter` |
| Debian, Ubuntu | `sudo apt install prometheus-node-exporter` | Enabled and started on install. Set flags in `/etc/default/prometheus-node-exporter`. |

**Very old releases:** node_exporter renamed its metrics in version 0.16 (2018), for example `node_cpu` to `node_cpu_seconds_total`. Ubuntu 18.04 ships an older version, so its hosts show as DOWN. Debian and Ubuntu releases from about 2019 onward work.

To check that a host serves the metrics the TUI needs:

```sh
curl -s http://rack-02.internal:9100/metrics | grep -m1 node_cpu_seconds_total
```

If that prints a line, the TUI can read the host.

## When a remote source is unavailable

- **The UI never waits on the network.** Each host has at most one fetch running at a time, in the background, with a timeout of `max(refresh, 2s)`. A host that hangs can't freeze the screen or delay the other hosts, and fetches don't pile up.
- **The last good values stay on screen.** When a fetch fails, the values turn grey and the sparkline shows a `·` gap, so you can see the outage in the history.
- **Each host shows a status:**
  - **UP** while data is fresh.
  - **STALE 42s** once the last good data is older than `stale_after`.
  - **DOWN** if the host has never answered.

  A short version of the error (e.g. `timeout`, `connection refused`, `unknown host …`) appears on the bottom edge of the panel.
- **Wrong responses are errors too.** A non-200 response, or a page that doesn't look like node_exporter output, counts as a failure rather than showing zeros. If a counter goes backwards (a reboot or exporter restart), that one reading's CPU and network rates are skipped instead of showing a nonsense value.
- **Thresholds are highlighted.** Values at or above `[thresholds]` are highlighted, and so are the individual sparkline bars that crossed them. The highlight is yellow in `default` and amber in `brotkasten`.

## Files

| File | Contents |
| --- | --- |
| [main.go](main.go) | The main loop |
| [config.go](config.go) | Loads and checks the config |
| [collect.go](collect.go) | Collects metrics. `local` reads `/proc`, `statfs("/")` and the hwmon/thermal sensors. URLs are parsed as node_exporter output without an extra dependency. |
| [host.go](host.go) | Per-host state, the ring buffer of recent samples, and the status logic |
| [view.go](view.go) | Drawing |
| [theme.go](theme.go) | Theme definitions. To add one, write another function and add it to the `themes` map. |
| [main_test.go](main_test.go) | Tests for the parser, rate calculation, ring buffer, unreachable-host handling, the local collector, themes, and rendering |

## Testing

```sh
go test ./...
go test -v -run TestRender .   # prints the dashboard at rack-screen size
```

## Limitations

- Remote URLs only work with node_exporter's format.
- The `local` source only works on Linux.
- Disk usage covers only the `/` mount.
- Network traffic adds up all interfaces except loopback and virtual ones like `veth*`, `docker*` and `br-*`.
