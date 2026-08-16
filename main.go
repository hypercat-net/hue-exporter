package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/hypercat-net/hue-exporter/collector"
	"github.com/hypercat-net/hue-exporter/hue"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// Config holds the exporter configuration loaded from a YAML file.
type Config struct {
	BridgeIP             string `yaml:"bridge_ip"`
	AppKey               string `yaml:"app_key"`
	// TLSInsecureSkipVerify disables TLS certificate verification when connecting
	// to the bridge. Hue bridges use self-signed certificates, so this is
	// typically required unless you provide the bridge CA certificate via
	// TLSCACertFile.
	TLSInsecureSkipVerify bool   `yaml:"tls_insecure_skip_verify"`
	// TLSCACertFile is the path to a PEM-encoded CA certificate file used to
	// verify the bridge's TLS certificate. When set, TLSInsecureSkipVerify is
	// ignored.
	TLSCACertFile string `yaml:"tls_ca_cert_file"`
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
	if cfg.BridgeIP == "" {
		return nil, fmt.Errorf("bridge_ip is required in config")
	}
	if cfg.AppKey == "" {
		return nil, fmt.Errorf("app_key is required in config")
	}
	return &cfg, nil
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

	bridge, err := hue.NewClient(cfg.BridgeIP, cfg.AppKey, opts)
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
