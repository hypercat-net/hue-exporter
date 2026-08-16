package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hypercat-net/hue-exporter/collector"
	"github.com/hypercat-net/hue-exporter/hue"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// Config holds the exporter configuration loaded from a YAML file.
type Config struct {
	BridgeIP string `yaml:"bridge_ip"`
	AppKey   string `yaml:"app_key"`
	// TLSInsecureSkipVerify disables TLS certificate verification when connecting
	// to the bridge. Hue bridges use self-signed certificates, so this is
	// typically required unless you provide the bridge CA certificate via
	// TLSCACertFile.
	TLSInsecureSkipVerify bool `yaml:"tls_insecure_skip_verify"`
	// TLSCACertFile is the path to a PEM-encoded CA certificate file used to
	// verify the bridge's TLS certificate. When set, TLSInsecureSkipVerify is
	// ignored.
	TLSCACertFile string `yaml:"tls_ca_cert_file"`
}

const hueDiscoveryURL = "https://discovery.meethue.com/"

type discoveredBridge struct {
	ID                string `json:"id"`
	InternalIPAddress string `json:"internalipaddress"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if cfg.AppKey == "" {
		return nil, fmt.Errorf("app_key is required in config")
	}
	return &cfg, nil
}

func discoverBridgeIP(client *http.Client, discoveryURL string) (string, error) {
	resp, err := client.Get(discoveryURL)
	if err != nil {
		return "", fmt.Errorf("discovering Hue bridges: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading Hue discovery response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Hue discovery returned status %d: %s", resp.StatusCode, body)
	}

	var bridges []discoveredBridge
	if err := json.Unmarshal(body, &bridges); err != nil {
		return "", fmt.Errorf("decoding Hue discovery response: %w", err)
	}

	switch len(bridges) {
	case 0:
		return "", fmt.Errorf("no Hue bridges discovered; set bridge_ip in config")
	case 1:
		if bridges[0].InternalIPAddress == "" {
			return "", fmt.Errorf("Hue discovery response missing internal IP address")
		}
		return bridges[0].InternalIPAddress, nil
	default:
		return "", fmt.Errorf("discovered %d Hue bridges; set bridge_ip in config", len(bridges))
	}
}

func main() {
	listenAddr := flag.String("web.listen-address", ":9366", "Address to listen on for web interface and telemetry.")
	configFile := flag.String("config.file", "hue_exporter.yml", "Path to the exporter configuration file.")
	flag.Parse()

	cfg, err := loadConfig(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	bridgeIP := cfg.BridgeIP
	if bridgeIP == "" {
		bridgeIP, err = discoverBridgeIP(&http.Client{Timeout: 10 * time.Second}, hueDiscoveryURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Discovered Hue bridge at %s\n", bridgeIP)
	}

	opts := hue.ClientOptions{
		InsecureSkipVerify: cfg.TLSInsecureSkipVerify,
	}
	if cfg.TLSCACertFile != "" {
		caCert, err := os.ReadFile(cfg.TLSCACertFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading CA cert file: %v\n", err)
			os.Exit(1)
		}
		opts.CACert = caCert
	}

	bridge, err := hue.NewClient(bridgeIP, cfg.AppKey, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating Hue client: %v\n", err)
		os.Exit(1)
	}
	col := collector.New(bridge)

	reg := prometheus.NewRegistry()
	reg.MustRegister(col)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `<html>
<head><title>Hue Exporter</title></head>
<body>
<h1>Hue Exporter</h1>
<p><a href="/metrics">Metrics</a></p>
</body>
</html>`)
	})

	fmt.Printf("Listening on %s\n", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
