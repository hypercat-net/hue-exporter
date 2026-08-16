package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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

type persistedState struct {
	AppKey string `yaml:"app_key"`
}

type bridgeStatus struct {
	Address string
	Source  string
	Error   string
}

type homePageData struct {
	BridgeAddress string
	BridgeSource  string
	BridgeError   string
	AppKeySet     bool
	Message       string
	ErrorMessage  string
}

type setupServer struct {
	mu               sync.RWMutex
	configuredBridge string
	discoveryURL     string
	discoveryClient  *http.Client
	storagePath      string
	opts             hue.ClientOptions
	createAppKey     func(string) (string, error)
	bridge           bridgeStatus
	appKey           string
	metricsHandler   http.Handler
}

const (
	hueDiscoveryURL           = "https://discovery.meethue.com/"
	maxDiscoveryResponseBytes = 64 * 1024
	defaultStorageFile        = "/data/hue_exporter_state.yml"
	defaultDeviceType         = "hue_exporter#server"
)

var homePageTemplate = template.Must(template.New("home").Parse(`<!DOCTYPE html>
<html>
<head><title>Hue Exporter</title></head>
<body>
<h1>Hue Exporter</h1>
{{if .Message}}<p style="color: green">{{.Message}}</p>{{end}}
{{if .ErrorMessage}}<p style="color: red">{{.ErrorMessage}}</p>{{end}}
<ul>
  <li>Bridge host/IP: {{if .BridgeAddress}}{{.BridgeAddress}}{{else}}not available{{end}}</li>
  <li>Bridge source: {{.BridgeSource}}</li>
  <li>Bridge discovery status: {{if .BridgeError}}{{.BridgeError}}{{else}}ok{{end}}</li>
  <li>API key set: {{if .AppKeySet}}yes{{else}}no{{end}}</li>
</ul>
<form method="post" action="/api/key">
  <button type="submit">Generate and save API key</button>
</form>
<p>Press the link button on the Hue Bridge before generating a key.</p>
<p><a href="/metrics">Metrics</a></p>
<p><a href="/healthz">Health</a></p>
</body>
</html>
`))

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return &cfg, nil
}

func loadPersistedState(path string) (*persistedState, error) {
	if path == "" {
		return &persistedState{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &persistedState{}, nil
		}
		return nil, fmt.Errorf("reading persisted state %s: %w", path, err)
	}
	var state persistedState
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing persisted state %s: %w", path, err)
	}
	return &state, nil
}

func savePersistedState(path string, state *persistedState) error {
	if path == "" {
		return fmt.Errorf("persisted state path is required")
	}
	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("encoding persisted state %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating persisted state directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing persisted state %s: %w", path, err)
	}
	return nil
}

type discoveredBridge struct {
	ID                string `json:"id"`
	InternalIPAddress string `json:"internalipaddress"`
}

func discoverBridgeIP(client *http.Client, discoveryURL string) (string, error) {
	resp, err := client.Get(discoveryURL)
	if err != nil {
		return "", fmt.Errorf("discovering Hue bridges: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryResponseBytes))
		return "", fmt.Errorf("Hue discovery returned status %d: %.200q", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryResponseBytes))
	if err != nil {
		return "", fmt.Errorf("reading Hue discovery response: %w", err)
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

func resolveBridgeStatus(client *http.Client, configuredBridge, discoveryURL string) bridgeStatus {
	if configuredBridge != "" {
		return bridgeStatus{
			Address: configuredBridge,
			Source:  "configured",
		}
	}

	bridgeIP, err := discoverBridgeIP(client, discoveryURL)
	if err != nil {
		return bridgeStatus{
			Source: "discovered",
			Error:  err.Error(),
		}
	}

	return bridgeStatus{
		Address: bridgeIP,
		Source:  "discovered",
	}
}

func runHealthcheck(target string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return nil
}

func buildClientOptions(cfg *Config) (hue.ClientOptions, error) {
	opts := hue.ClientOptions{
		InsecureSkipVerify: cfg.TLSInsecureSkipVerify,
	}
	if cfg.TLSCACertFile != "" {
		caCert, err := os.ReadFile(cfg.TLSCACertFile)
		if err != nil {
			return hue.ClientOptions{}, fmt.Errorf("reading CA cert file: %w", err)
		}
		opts.CACert = caCert
	}
	return opts, nil
}

func newMetricsHandler(bridgeAddress, appKey string, opts hue.ClientOptions) (http.Handler, error) {
	bridge, err := hue.NewClient(bridgeAddress, appKey, opts)
	if err != nil {
		return nil, fmt.Errorf("creating Hue client: %w", err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collector.New(bridge))
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{}), nil
}

func newSetupServer(cfg *Config, storagePath string, opts hue.ClientOptions, discoveryURL string) (*setupServer, error) {
	state, err := loadPersistedState(storagePath)
	if err != nil {
		return nil, err
	}

	srv := &setupServer{
		configuredBridge: cfg.BridgeIP,
		discoveryURL:     discoveryURL,
		discoveryClient:  &http.Client{Timeout: 10 * time.Second},
		storagePath:      storagePath,
		opts:             opts,
		createAppKey: func(bridgeAddress string) (string, error) {
			return hue.CreateAppKey(bridgeAddress, defaultDeviceType, opts)
		},
	}
	srv.bridge = resolveBridgeStatus(srv.discoveryClient, cfg.BridgeIP, discoveryURL)

	appKey := cfg.AppKey
	if appKey == "" {
		appKey = state.AppKey
	}
	if err := srv.setAppKeyLocked(appKey); err != nil {
		return nil, err
	}

	return srv, nil
}

// setAppKeyLocked updates the effective app key and metrics handler.
// Concurrent callers must hold s.mu.
func (s *setupServer) setAppKeyLocked(appKey string) error {
	var handler http.Handler
	var err error
	if appKey != "" && s.bridge.Address != "" {
		handler, err = newMetricsHandler(s.bridge.Address, appKey, s.opts)
		if err != nil {
			return err
		}
	}

	s.appKey = appKey
	s.metricsHandler = handler
	return nil
}

func (s *setupServer) ensureBridgeStatus() (bridgeStatus, error) {
	s.mu.RLock()
	if s.bridge.Address != "" {
		bridge := s.bridge
		s.mu.RUnlock()
		return bridge, nil
	}
	configuredBridge := s.configuredBridge
	discoveryClient := s.discoveryClient
	discoveryURL := s.discoveryURL
	s.mu.RUnlock()

	resolved := resolveBridgeStatus(discoveryClient, configuredBridge, discoveryURL)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bridge.Address != "" {
		return s.bridge, nil
	}
	s.bridge = resolved
	if err := s.setAppKeyLocked(s.appKey); err != nil {
		return s.bridge, err
	}
	return s.bridge, nil
}

func (s *setupServer) persistGeneratedAppKey(appKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := savePersistedState(s.storagePath, &persistedState{AppKey: appKey}); err != nil {
		return err
	}
	return s.setAppKeyLocked(appKey)
}

func (s *setupServer) pageData(message, errorMessage string) homePageData {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return homePageData{
		BridgeAddress: s.bridge.Address,
		BridgeSource:  s.bridge.Source,
		BridgeError:   s.bridge.Error,
		AppKeySet:     s.appKey != "",
		Message:       message,
		ErrorMessage:  errorMessage,
	}
}

func (s *setupServer) renderHome(w http.ResponseWriter, message, errorMessage string) {
	var buf bytes.Buffer
	if err := homePageTemplate.Execute(&buf, s.pageData(message, errorMessage)); err != nil {
		http.Error(w, fmt.Sprintf("rendering home page: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (s *setupServer) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.renderHome(w, "", "")
}

func (s *setupServer) handleGenerateAppKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bridge, err := s.ensureBridgeStatus()
	if err != nil {
		s.renderHome(w, "", fmt.Sprintf("failed to prepare bridge status: %v", err))
		return
	}
	if bridge.Address == "" {
		s.renderHome(w, "", "bridge host/IP is unavailable")
		return
	}

	appKey, err := s.createAppKey(bridge.Address)
	if err != nil {
		s.renderHome(w, "", fmt.Sprintf("failed to generate API key: %v", err))
		return
	}
	if err := s.persistGeneratedAppKey(appKey); err != nil {
		s.renderHome(w, "", fmt.Sprintf("failed to persist API key: %v", err))
		return
	}

	s.renderHome(w, "API key generated and saved.", "")
}

func (s *setupServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	handler := s.metricsHandler
	bridgeAddress := s.bridge.Address
	appKeySet := s.appKey != ""
	s.mu.RUnlock()

	if handler == nil {
		switch {
		case !appKeySet:
			http.Error(w, "metrics unavailable: app_key is not configured", http.StatusServiceUnavailable)
		case bridgeAddress == "":
			http.Error(w, "metrics unavailable: bridge host/IP is not configured", http.StatusServiceUnavailable)
		default:
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		}
		return
	}

	handler.ServeHTTP(w, r)
}

func newMux(server *setupServer) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", http.HandlerFunc(server.handleMetrics))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/api/key", server.handleGenerateAppKey)
	mux.HandleFunc("/", server.handleHome)
	return mux
}

func main() {
	listenAddr := flag.String("web.listen-address", ":9366", "Address to listen on for web interface and telemetry.")
	configFile := flag.String("config.file", "hue_exporter.yml", "Path to the exporter configuration file.")
	storageFile := flag.String("storage.file", defaultStorageFile, "Path to the persisted state file used for generated API keys.")
	healthcheckTarget := flag.String("healthcheck.target", "", "Probe URL and exit with status 0 when healthy, 1 when unhealthy.")
	flag.Parse()

	if *healthcheckTarget != "" {
		if err := runHealthcheck(*healthcheckTarget); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg, err := loadConfig(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	opts, err := buildClientOptions(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	server, err := newSetupServer(cfg, *storageFile, opts, hueDiscoveryURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Listening on %s\n", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, newMux(server)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
