# hue-exporter

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

## Usage

### 1. Obtain a Hue application key

```bash
# Press the link button on the bridge first, then:
curl -k -X POST https://<bridge_ip>/api \
  -H 'Content-Type: application/json' \
  -d '{"devicetype":"hue_exporter#server"}'
```

### 2. Create the configuration file

Copy `hue_exporter.example.yml` to `hue_exporter.yml` and fill in your bridge
IP and app key:

```yaml
bridge_ip: 192.168.1.2
app_key: "your-app-key-here"

# Hue bridges use self-signed TLS certificates. Choose one:
# Option A: skip TLS verification (quick, less secure)
tls_insecure_skip_verify: true
# Option B: provide the bridge CA certificate (recommended)
# tls_ca_cert_file: /path/to/bridge-ca.pem
```

### 3. Build and run

```bash
go build -o hue-exporter .
./hue-exporter --config.file hue_exporter.yml --web.listen-address :9366
```

Metrics are available at `http://localhost:9366/metrics`.

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
