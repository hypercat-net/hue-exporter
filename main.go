package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	TLSCACertFile string         `yaml:"tls_ca_cert_file"`
	State         persistedState `yaml:"state,omitempty"`
}

type persistedState struct {
	BridgeIP string `yaml:"bridge_ip,omitempty"`
	AppKey   string `yaml:"app_key,omitempty"`
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
	TLSCACertFile string
	Message       string
	ErrorMessage  string
}

type setupServer struct {
	mu               sync.RWMutex
	configuredBridge string
	discoveryURL     string
	discoveryClient  *http.Client
	configPath       string
	config           Config
	opts             hue.ClientOptions
	createAppKey     func(string) (string, error)
	fetchBridgeCert  func(string) ([]byte, error)
	bridge           bridgeStatus
	appKey           string
	metricsHandler   http.Handler
}

const (
	hueDiscoveryURL           = "https://discovery.meethue.com/"
	maxDiscoveryResponseBytes = 64 * 1024
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
  <li>Bridge certificate file: {{if .TLSCACertFile}}{{.TLSCACertFile}}{{else}}not configured{{end}}</li>
</ul>
<form method="post" action="/api/key">
  <button type="submit">Generate and save API key</button>
</form>
<form method="post" action="/api/cert">
  <button type="submit">Save bridge certificate and update config</button>
</form>
<p>Press the link button on the Hue Bridge before generating a key.</p>
<p><a href="/metrics">Metrics</a></p>
<p><a href="/healthz">Health</a></p>
</body>
</html>
`))

func fetchBridgeCertificatePEM(bridgeAddress string) ([]byte, error) {
	target := bridgeAddress
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "443")
	}

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", target, &tls.Config{ //nolint:gosec // Required to fetch self-signed bridge cert.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to bridge TLS endpoint: %w", err)
	}
	defer conn.Close()

	peerCerts := conn.ConnectionState().PeerCertificates
	if len(peerCerts) == 0 {
		return nil, fmt.Errorf("bridge TLS endpoint did not provide certificates")
	}

	var pemData []byte
	for _, cert := range peerCerts {
		pemData = append(pemData, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: cert.Raw,
		})...)
	}
	return pemData, nil
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
	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encoding config %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
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

func resolveBridgeStatusWithPersisted(client *http.Client, configuredBridge, persistedBridge, discoveryURL string) bridgeStatus {
	if configuredBridge != "" {
		return bridgeStatus{
			Address: configuredBridge,
			Source:  "configured",
		}
	}
	if persistedBridge != "" {
		return bridgeStatus{
			Address: persistedBridge,
			Source:  "persisted",
		}
	}
	return resolveBridgeStatus(client, "", discoveryURL)
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

func newMetricsHandler(bridge hue.Bridge) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector.New(bridge))
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

func newSetupServer(cfg *Config, configPath string, opts hue.ClientOptions, discoveryURL string) (*setupServer, error) {
	srv := &setupServer{
		configuredBridge: cfg.BridgeIP,
		discoveryURL:     discoveryURL,
		discoveryClient:  &http.Client{Timeout: 10 * time.Second},
		configPath:       configPath,
		config:           *cfg,
		opts:             opts,
		fetchBridgeCert:  fetchBridgeCertificatePEM,
		appKey:           cfg.State.AppKey,
	}
	srv.createAppKey = func(bridgeAddress string) (string, error) {
		srv.mu.RLock()
		currentOpts := srv.opts
		srv.mu.RUnlock()
		return hue.CreateAppKey(bridgeAddress, defaultDeviceType, currentOpts)
	}
	srv.bridge = resolveBridgeStatusWithPersisted(srv.discoveryClient, cfg.BridgeIP, cfg.State.BridgeIP, discoveryURL)

	appKey := cfg.AppKey
	if appKey == "" {
		appKey = cfg.State.AppKey
	}
	srv.setAppKeyLocked(appKey)
	if cfg.BridgeIP == "" && srv.bridge.Source == "discovered" {
		if err := srv.persistBridgeIPLocked(srv.bridge.Address); err != nil {
			return nil, err
		}
	}

	return srv, nil
}

// setAppKeyLocked updates the effective app key and metrics handler.
// Concurrent callers must hold s.mu; newSetupServer may call it during
// initialization before the server is shared.
func (s *setupServer) setAppKeyLocked(appKey string) {
	s.appKey = appKey
	if appKey == "" {
		s.metricsHandler = nil
		return
	}
	s.metricsHandler = newMetricsHandler(s)
}

func (s *setupServer) persistBridgeIPLocked(bridgeIP string) error {
	if s.configPath == "" {
		return nil
	}
	s.config.State.BridgeIP = bridgeIP
	return saveConfig(s.configPath, &s.config)
}

func (s *setupServer) ensureBridgeStatus() (bridgeStatus, error) {
	s.mu.RLock()
	bridge := s.bridge
	s.mu.RUnlock()

	if bridge.Address != "" {
		return bridge, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bridge.Address != "" {
		return s.bridge, nil
	}

	resolved := resolveBridgeStatus(s.discoveryClient, "", s.discoveryURL)
	s.bridge = resolved
	if resolved.Address != "" {
		if err := s.persistBridgeIPLocked(resolved.Address); err != nil {
			return s.bridge, err
		}
	}
	return s.bridge, nil
}

func (s *setupServer) persistGeneratedAppKey(appKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.config.State.BridgeIP = s.bridge.Address
	s.config.State.AppKey = appKey
	if err := saveConfig(s.configPath, &s.config); err != nil {
		return err
	}
	s.setAppKeyLocked(appKey)
	return nil
}

func (s *setupServer) persistBridgeCertificate(certPEM []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.configPath == "" {
		return fmt.Errorf("config path is not configured")
	}

	certPath := s.config.TLSCACertFile
	if certPath == "" {
		certPath = filepath.Join(filepath.Dir(s.configPath), "bridge-ca.pem")
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return fmt.Errorf("creating certificate directory: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("writing bridge certificate file: %w", err)
	}

	s.config.TLSCACertFile = certPath
	s.config.TLSInsecureSkipVerify = false
	if err := saveConfig(s.configPath, &s.config); err != nil {
		return err
	}

	s.opts.CACert = certPEM
	s.opts.InsecureSkipVerify = false
	return nil
}

func (s *setupServer) rediscoverBridge() (bridgeStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.configuredBridge != "" {
		return s.bridge, nil
	}

	resolved := resolveBridgeStatus(s.discoveryClient, "", s.discoveryURL)
	if resolved.Address == "" {
		if s.bridge.Address != "" {
			s.bridge.Error = resolved.Error
			return s.bridge, errors.New(resolved.Error)
		}
		s.bridge = resolved
		return s.bridge, errors.New(resolved.Error)
	}

	s.bridge = resolved
	if err := s.persistBridgeIPLocked(resolved.Address); err != nil {
		return s.bridge, err
	}
	return s.bridge, nil
}

func (s *setupServer) handleSaveBridgeCertificate(w http.ResponseWriter, r *http.Request) {
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

	certPEM, err := s.fetchBridgeCert(bridge.Address)
	if err != nil {
		s.renderHome(w, "", fmt.Sprintf("failed to fetch bridge certificate: %v", err))
		return
	}
	if err := s.persistBridgeCertificate(certPEM); err != nil {
		s.renderHome(w, "", fmt.Sprintf("failed to persist bridge certificate: %v", err))
		return
	}

	s.renderHome(w, "Bridge certificate saved and config updated.", "")
}

func (s *setupServer) bridgeClient() (*hue.Client, bridgeStatus, error) {
	bridge, err := s.ensureBridgeStatus()
	if err != nil {
		return nil, bridge, err
	}

	s.mu.RLock()
	appKey := s.appKey
	opts := s.opts
	s.mu.RUnlock()
	if appKey == "" {
		return nil, bridge, fmt.Errorf("app_key is not configured")
	}

	client, err := hue.NewClient(bridge.Address, appKey, opts)
	if err != nil {
		return nil, bridge, fmt.Errorf("creating Hue client: %w", err)
	}
	return client, bridge, nil
}

func (s *setupServer) shouldRediscover(bridge bridgeStatus, err error) bool {
	return bridge.Source != "configured" && hue.IsConnectionError(err)
}

func fetchWithRediscovery[T any](s *setupServer, getter func(*hue.Client) ([]T, error)) ([]T, error) {
	client, bridge, err := s.bridgeClient()
	if err != nil {
		return nil, err
	}

	items, err := getter(client)
	if err == nil || !s.shouldRediscover(bridge, err) {
		return items, err
	}

	rediscovered, rediscoverErr := s.rediscoverBridge()
	if rediscoverErr != nil || rediscovered.Address == "" {
		return nil, err
	}

	client, _, clientErr := s.bridgeClient()
	if clientErr != nil {
		return nil, clientErr
	}
	return getter(client)
}

func (s *setupServer) GetLights() ([]hue.Light, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.Light, error) {
		return client.GetLights()
	})
}

func (s *setupServer) GetGroupedLights() ([]hue.GroupedLight, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.GroupedLight, error) {
		return client.GetGroupedLights()
	})
}

func (s *setupServer) GetRooms() ([]hue.Room, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.Room, error) {
		return client.GetRooms()
	})
}

func (s *setupServer) GetZones() ([]hue.Zone, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.Zone, error) {
		return client.GetZones()
	})
}

func (s *setupServer) GetMotion() ([]hue.Motion, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.Motion, error) {
		return client.GetMotion()
	})
}

func (s *setupServer) GetTemperature() ([]hue.Temperature, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.Temperature, error) {
		return client.GetTemperature()
	})
}

func (s *setupServer) GetLightLevel() ([]hue.LightLevel, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.LightLevel, error) {
		return client.GetLightLevel()
	})
}

func (s *setupServer) GetDevicePower() ([]hue.DevicePower, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.DevicePower, error) {
		return client.GetDevicePower()
	})
}

func (s *setupServer) GetZigbeeConnectivity() ([]hue.ZigbeeConnectivity, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.ZigbeeConnectivity, error) {
		return client.GetZigbeeConnectivity()
	})
}

func (s *setupServer) GetDevices() ([]hue.Device, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.Device, error) {
		return client.GetDevices()
	})
}

func (s *setupServer) GetScenes() ([]hue.Scene, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.Scene, error) {
		return client.GetScenes()
	})
}

func (s *setupServer) GetButtons() ([]hue.Button, error) {
	return fetchWithRediscovery(s, func(client *hue.Client) ([]hue.Button, error) {
		return client.GetButtons()
	})
}

func (s *setupServer) pageData(message, errorMessage string) homePageData {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return homePageData{
		BridgeAddress: s.bridge.Address,
		BridgeSource:  s.bridge.Source,
		BridgeError:   s.bridge.Error,
		AppKeySet:     s.appKey != "",
		TLSCACertFile: s.config.TLSCACertFile,
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

func newMux(server *setupServer, setupUIEnabled bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", http.HandlerFunc(server.handleMetrics))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	if setupUIEnabled {
		mux.HandleFunc("/api/key", server.handleGenerateAppKey)
		mux.HandleFunc("/api/cert", server.handleSaveBridgeCertificate)
		mux.HandleFunc("/", server.handleHome)
	}
	return mux
}

func setupUIEnabled(flagValue bool) (bool, error) {
	envValue := os.Getenv("HUE_EXPORTER_ENABLE_SETUP_UI")
	if envValue == "" {
		return flagValue, nil
	}
	enabled, err := strconv.ParseBool(envValue)
	if err != nil {
		return false, fmt.Errorf("invalid HUE_EXPORTER_ENABLE_SETUP_UI value %q: %w", envValue, err)
	}
	return enabled, nil
}

func main() {
	listenAddr := flag.String("web.listen-address", ":9366", "Address to listen on for web interface and telemetry.")
	configFile := flag.String("config.file", "hue_exporter.yml", "Path to the exporter configuration file.")
	healthcheckTarget := flag.String("healthcheck.target", "", "Probe URL and exit with status 0 when healthy, 1 when unhealthy.")
	enableSetupUI := flag.Bool("web.enable-setup-ui", false, "Enable setup API UI (/, /api/key, /api/cert). Disabled by default.")
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

	server, err := newSetupServer(cfg, *configFile, opts, hueDiscoveryURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	uiEnabled, err := setupUIEnabled(*enableSetupUI)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Listening on %s\n", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, newMux(server, uiEnabled)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
