# hue-exporter

[![CI](https://github.com/hypercat-net/hue-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/hypercat-net/hue-exporter/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/hypercat-net/hue-exporter)](LICENSE)
[![Buy Me a Coffee](https://img.shields.io/badge/Buy%20me%20a%20coffee-support-FFDD00?logo=buymeacoffee&logoColor=000000)](https://buymeacoffee.com/barcar)

Prometheus exporter for Philips Hue using the native **CLIP v2 API** (no
deprecated or archived third-party Hue libraries required).

## Features

* Uses the Hue **CLIP v2** (`/clip/v2/resource/...`) REST API directly
* No dependency on deprecated Hue v1 client libraries
* Exports metrics for lights, grouped lights, motion sensors, temperature
  sensors, light-level sensors, device battery levels, Zigbee connectivity,
  and scenes

## Metrics

| Metric | Type | Description |
|---|---|---|
| `hue_light_on` | Gauge | 1 if the light is on, 0 if off |
| `hue_light_brightness_percent` | Gauge | Brightness 0–100% |
| `hue_light_color_temperature_mirek` | Gauge | Color temperature in mirek (153–500) |
| `hue_light_color_x` | Gauge | CIE 1931 xy color coordinate X |
| `hue_light_color_y` | Gauge | CIE 1931 xy color coordinate Y |
| `hue_light_scrapes_failed_total` | Counter | Failed light scrape count |
| `hue_grouped_light_on` | Gauge | 1 if any light in group is on |
| `hue_grouped_light_brightness_percent` | Gauge | Group brightness 0–100% |
| `hue_grouped_light_scrapes_failed_total` | Counter | Failed grouped-light scrape count |
| `hue_motion_detected` | Gauge | 1 if motion currently detected |
| `hue_motion_enabled` | Gauge | 1 if motion sensor is enabled |
| `hue_motion_scrapes_failed_total` | Counter | Failed motion scrape count |
| `hue_temperature_celsius` | Gauge | Temperature in °C |
| `hue_temperature_scrapes_failed_total` | Counter | Failed temperature scrape count |
| `hue_light_level_lux` | Gauge | Ambient light level in lux |
| `hue_light_level_scrapes_failed_total` | Counter | Failed light-level scrape count |
| `hue_device_battery_level_percent` | Gauge | Battery level 0–100% |
| `hue_device_scrapes_failed_total` | Counter | Failed device-power scrape count |
| `hue_zigbee_connected` | Gauge | 1 if Zigbee device is connected |
| `hue_zigbee_scrapes_failed_total` | Counter | Failed Zigbee scrape count |
| `hue_scene_active` | Gauge | 1 if scene is currently active |
| `hue_scene_scrapes_failed_total` | Counter | Failed scene scrape count |

## Hue API connection process

### 1. Enable local network access to the bridge

Ensure the exporter host can reach your Hue Bridge over HTTPS (`443`) on your
LAN.

### 2. Obtain a Hue application key

```bash
# Press the link button on the bridge first, then:
curl -k -X POST https://<bridge_ip>/api \
  -H 'Content-Type: application/json' \
  -d '{"devicetype":"hue_exporter#server"}'
```

Copy the `success.username` value from the response; this is your `app_key`.

### 3. Configure TLS trust for the Hue bridge

Hue bridges use self-signed certificates:

* **Quick setup**: set `tls_insecure_skip_verify: true`
* **Recommended setup**: trust the bridge certificate and set
  `tls_ca_cert_file`

You can export the bridge certificate with:

```bash
openssl s_client -showcerts -connect <bridge_ip>:443 </dev/null 2>/dev/null \
  | openssl x509 -outform PEM > bridge-ca.pem
```

### 4. Create the configuration file

Copy `hue_exporter.example.yml` to `hue_exporter.yml` and fill in your bridge
IP and app key:

```yaml
# Optional: if omitted, the exporter will auto-discover a single bridge
bridge_ip: 192.168.1.2
app_key: "your-app-key-here"

# Hue bridges use self-signed TLS certificates. Choose one:
# Option A: skip TLS verification (quick, less secure)
tls_insecure_skip_verify: true
# Option B: provide the bridge CA certificate (recommended)
# tls_ca_cert_file: /path/to/bridge-ca.pem
```

If `bridge_ip` is omitted, the exporter uses `https://discovery.meethue.com/`
to find Hue bridges. Auto-discovery succeeds only when exactly one bridge is
found; if none or multiple bridges are discovered, set `bridge_ip` explicitly.

Discovery only resolves the bridge address. After that, the exporter connects
directly to the bridge over HTTPS on your LAN using the returned private IP.
That means the runtime environment must be able to:

* reach `https://discovery.meethue.com/` to look up bridges
* reach the discovered bridge IP on port `443`

#### Docker networking nuance

When running in Docker, auto-discovery still depends on the container being
able to reach both the public discovery service and the bridge's private LAN
address. If the container cannot route to your LAN, discovery may find the
bridge but the exporter will still fail to connect to it.

On Linux, `--network host` is often the simplest option for local-network
access. With bridge networking, port publishing is not enough by itself — the
container also needs outbound access to the Hue bridge's subnet. On Docker
Desktop for macOS or Windows, networking is more indirect, so setting
`bridge_ip` explicitly is often the more predictable setup.

### 5. Build and run

```bash
go build -o hue-exporter .
./hue-exporter --config.file hue_exporter.yml --web.listen-address :9366
```

Metrics are available at `http://localhost:9366/metrics`.

## Release, image tags, and branch protection

* Create release tags using semantic version format, for example `v0.0.1`.
* Pushing a `v*` tag publishes:
  * a GitHub release
  * multi-arch Docker images to `ghcr.io/<owner>/<repo>` with tags:
    * `v0.0.1`
    * `0.0.1`
    * `v0.0`
    * `0.0`
    * `latest`
* Protect the `main` branch in GitHub settings by:
  * disallowing direct pushes
  * requiring at least one approving review
  * requiring CI checks:
    * `CI / Build and test`
    * `Build and push Docker image / Build multi-platform image`
* Branch protection is configured in GitHub repository settings, not in this
  repository's files.

### Flags

| Flag | Default | Description |
|---|---|---|
| `--config.file` | `hue_exporter.yml` | Path to config file |
| `--web.listen-address` | `:9366` | Address to listen on |

## Building

```bash
go build ./...
go test ./...
```

## Architecture

```
main.go              – flag parsing, config loading, HTTP server
hue/client.go        – Hue CLIP v2 HTTP client + resource types + Bridge interface
collector/collector.go – Prometheus Collector implementation
```

The `hue.Bridge` interface enables mock-based unit testing of all collectors
without needing a real bridge.

## Contributing and support

- Contribution guidelines: [CONTRIBUTING.md](CONTRIBUTING.md)
- Security policy: [SECURITY.md](SECURITY.md)
- Support process: [SUPPORT.md](SUPPORT.md)
