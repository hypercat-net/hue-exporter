package main

import (
	"bytes"
	"context"
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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
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
	Address         string
	Source          string
	Error           string
	DiscoveryStatus string
}

type homePageData struct {
	// Step 1: Discovery
	BridgeAddress         string
	BridgeSource          string
	BridgeDiscoveryStatus string
	DiscoveryDone         bool

	// Step 2: Certificate
	CertSaved bool
	CertFile  string
	CertError string

	// Step 3: API Key
	AppKeySet bool

	// General feedback
	Message      string
	ErrorMessage string
}

type setupServer struct {
	mu               sync.RWMutex
	configuredBridge string
	discoverer       bridgeDiscoverer
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
	mdnsService               = "_hue._tcp"
	mdnsDiscoveryTimeout      = 5 * time.Second
)

var homePageTemplate = template.Must(template.New("home").Parse(`<!DOCTYPE html>
<html>
<head>
<title>Hue Exporter Setup</title>
<style>
body{font-family:sans-serif;max-width:640px;margin:2em auto;padding:0 1em}
h1{margin-bottom:1.5em}
.step{border:1px solid #ccc;border-radius:6px;padding:1em 1.2em;margin-bottom:1em}
.step.done{border-color:#4caf50;background:#f6fff6}
.step.failed{border-color:#f44336;background:#fff6f6}
.step h3{margin:0 0 0.6em;font-size:1em;text-transform:uppercase;letter-spacing:.05em;color:#555}
.ok{color:#4caf50;font-weight:bold}
.err{color:#f44336;font-weight:bold}
.pending{color:#888}
code{background:#f4f4f4;padding:0 .3em;border-radius:3px}
input[type=text]{width:100%;box-sizing:border-box;padding:.4em;margin:.4em 0 .6em;border:1px solid #ccc;border-radius:4px}
button{padding:.4em 1em;cursor:pointer;border-radius:4px;border:1px solid #aaa}
button:disabled{opacity:.4;cursor:default}
.links{margin-top:1.5em}
</style>
</head>
<body>
<h1>Hue Exporter Setup</h1>

{{if .Message}}<p class="ok">{{.Message}}</p>{{end}}
{{if .ErrorMessage}}<p class="err">{{.ErrorMessage}}</p>{{end}}

<!-- Step 1: Discovery -->
<div class="step{{if .DiscoveryDone}} done{{else}} failed{{end}}">
  <h3>Step 1 &mdash; Bridge Discovery</h3>
  {{if .BridgeAddress}}
    <p><span class="ok">&#10003;</span> Bridge found at <strong>{{.BridgeAddress}}</strong> (source: {{.BridgeSource}})</p>
    {{if .BridgeDiscoveryStatus}}<p><small>{{.BridgeDiscoveryStatus}}</small></p>{{end}}
  {{else}}
    <p><span class="err">&#10007;</span> Bridge not found{{if .BridgeDiscoveryStatus}}: {{.BridgeDiscoveryStatus}}{{end}}</p>
    <p class="pending">Discovery tries mDNS (<code>_hue._tcp</code>) first, then <code>discovery.meethue.com</code>. If both fail, enter the bridge IP manually below.</p>
  {{end}}
  <form method="post" action="/api/bridge">
    <label>Bridge host/IP (leave blank to use discovered address):</label>
    <input type="text" name="bridge_address" value="{{.BridgeAddress}}" placeholder="e.g. 192.168.1.2 or bridge.local">
    <button type="submit">Save bridge host/IP</button>
  </form>
</div>

<!-- Step 2: Certificate -->
<div class="step{{if .CertSaved}} done{{else if .CertError}} failed{{end}}">
  <h3>Step 2 &mdash; Bridge Certificate</h3>
  {{if .CertSaved}}
    <p><span class="ok">&#10003;</span> Certificate saved: <code>{{.CertFile}}</code></p>
  {{else if .CertError}}
    <p><span class="err">&#10007;</span> {{.CertError}}</p>
    <p class="pending">The certificate will be fetched automatically when the bridge becomes reachable. Reload this page to retry.</p>
  {{else if .DiscoveryDone}}
    <p class="pending">Fetching certificate&hellip;</p>
  {{else}}
    <p class="pending">Waiting for bridge discovery.</p>
  {{end}}
</div>

<!-- Step 3: API Key -->
<div class="step{{if .AppKeySet}} done{{end}}">
  <h3>Step 3 &mdash; API Key</h3>
  {{if .AppKeySet}}
    <p><span class="ok">&#10003;</span> API key is set.</p>
  {{else}}
    <p>Press the <strong>link button</strong> on your Hue Bridge, then click Generate.</p>
  {{end}}
  <form method="post" action="/api/key">
    <button type="submit"{{if not .DiscoveryDone}} disabled{{end}}>Generate and save API key</button>
  </form>
</div>

<div class="links">
  <a href="/metrics">Metrics</a> &nbsp;|&nbsp; <a href="/healthz">Health</a>
</div>
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

// bridgeDiscoverer is the interface used to locate Hue bridges on the network.
// It is satisfied by discoverBridgesMDNS and discoverBridgesHTTP, and can be
// replaced in tests with a fake implementation.
type bridgeDiscoverer func() ([]discoveredBridge, error)

// discoverBridgesMDNS browses for _hue._tcp mDNS/DNS-SD services and returns
// any Hue bridges found on the local network. It is the preferred discovery
// method because it works entirely on the local network without relying on an
// external cloud endpoint.
func discoverBridgesMDNS() ([]discoveredBridge, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("creating mDNS resolver: %w", err)
	}

	entries := make(chan *zeroconf.ServiceEntry)
	ctx, cancel := context.WithTimeout(context.Background(), mdnsDiscoveryTimeout)
	defer cancel()

	if err := resolver.Browse(ctx, mdnsService, "local.", entries); err != nil {
		return nil, fmt.Errorf("browsing mDNS for %s: %w", mdnsService, err)
	}

	var bridges []discoveredBridge
	for entry := range entries {
		var ip string
		for _, addr := range entry.AddrIPv4 {
			ip = addr.String()
			break
		}
		if ip == "" {
			for _, addr := range entry.AddrIPv6 {
				ip = addr.String()
				break
			}
		}
		if ip == "" {
			continue
		}
		bridges = append(bridges, discoveredBridge{
			ID:                entry.ServiceRecord.Instance,
			InternalIPAddress: ip,
		})
	}
	return bridges, nil
}

// discoverBridgesHTTP queries the Hue cloud discovery endpoint and returns the
// list of bridges it reports. This is the fallback when mDNS finds nothing.
func discoverBridgesHTTP(client *http.Client, discoveryURL string) ([]discoveredBridge, error) {
	resp, err := client.Get(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("discovering Hue bridges: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryResponseBytes))
		return nil, fmt.Errorf("Hue discovery returned status %d: %.200q", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading Hue discovery response: %w", err)
	}

	var bridges []discoveredBridge
	if err := json.Unmarshal(body, &bridges); err != nil {
		return nil, fmt.Errorf("decoding Hue discovery response: %w", err)
	}
	return bridges, nil
}

// discoverBridges tries mDNS first and falls back to the HTTP cloud endpoint
// when mDNS finds no bridges. The mdns and http parameters allow callers (and
// tests) to inject alternative implementations.
func discoverBridges(mdns bridgeDiscoverer, http bridgeDiscoverer) ([]discoveredBridge, error) {
	bridges, err := mdns()
	if err == nil && len(bridges) > 0 {
		return bridges, nil
	}

	return http()
}

func joinDiscoveryBridgeIPs(bridges []discoveredBridge) string {
	ips := make([]string, 0, len(bridges))
	for _, bridge := range bridges {
		if bridge.InternalIPAddress == "" {
			ips = append(ips, "<missing ip>")
			continue
		}
		ips = append(ips, bridge.InternalIPAddress)
	}
	return strings.Join(ips, ", ")
}

// makeDiscoverer constructs a bridgeDiscoverer that tries mDNS first and falls
// back to the Hue HTTP cloud endpoint. mdnsFn is the mDNS implementation to
// use; pass discoverBridgesMDNS for production and a no-op stub for tests.
func makeDiscoverer(mdnsFn bridgeDiscoverer, client *http.Client, discoveryURL string) bridgeDiscoverer {
	return func() ([]discoveredBridge, error) {
		return discoverBridges(mdnsFn, func() ([]discoveredBridge, error) {
			return discoverBridgesHTTP(client, discoveryURL)
		})
	}
}

// makeHTTPOnlyDiscoverer returns a discoverer that queries only the HTTP cloud
// endpoint, skipping mDNS entirely. Use this in tests to avoid multicast
// timeouts that would otherwise slow down the test suite.
func makeHTTPOnlyDiscoverer(client *http.Client, discoveryURL string) bridgeDiscoverer {
	return func() ([]discoveredBridge, error) {
		return discoverBridgesHTTP(client, discoveryURL)
	}
}

// discoverBridgeIP resolves a bridge IP using only the HTTP cloud endpoint.
// It is intended for callers that already have an HTTP client and discovery URL
// and do not need the mDNS-first behaviour of the full discovery stack.
func discoverBridgeIP(client *http.Client, discoveryURL string) (string, error) {
	bridges, err := discoverBridgesHTTP(client, discoveryURL)
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("discovered %d Hue bridges (%s); set bridge_ip in config", len(bridges), joinDiscoveryBridgeIPs(bridges))
	}
}

func discoverBridgeStatus(discoverer bridgeDiscoverer) bridgeStatus {
	bridges, err := discoverer()
	if err != nil {
		return bridgeStatus{
			Source:          "discovered",
			Error:           err.Error(),
			DiscoveryStatus: err.Error(),
		}
	}

	switch len(bridges) {
	case 0:
		msg := "no Hue bridges discovered; set bridge_ip in config"
		return bridgeStatus{
			Source:          "discovered",
			Error:           msg,
			DiscoveryStatus: msg,
		}
	case 1:
		if bridges[0].InternalIPAddress == "" {
			msg := "Hue discovery response missing internal IP address"
			return bridgeStatus{
				Source:          "discovered",
				Error:           msg,
				DiscoveryStatus: msg,
			}
		}
		return bridgeStatus{
			Address:         bridges[0].InternalIPAddress,
			Source:          "discovered",
			DiscoveryStatus: fmt.Sprintf("discovered 1 Hue bridge: %s", bridges[0].InternalIPAddress),
		}
	default:
		msg := fmt.Sprintf("discovered %d Hue bridges (%s); set bridge_ip in config", len(bridges), joinDiscoveryBridgeIPs(bridges))
		return bridgeStatus{
			Source:          "discovered",
			Error:           msg,
			DiscoveryStatus: msg,
		}
	}
}

func resolveBridgeStatus(discoverer bridgeDiscoverer, configuredBridge string) bridgeStatus {
	if configuredBridge != "" {
		return bridgeStatus{
			Address: configuredBridge,
			Source:  "configured",
		}
	}

	return discoverBridgeStatus(discoverer)
}

func resolveBridgeStatusWithPersisted(discoverer bridgeDiscoverer, configuredBridge, persistedBridge string) bridgeStatus {
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
	return resolveBridgeStatus(discoverer, "")
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
	discoverer := makeDiscoverer(discoverBridgesMDNS, &http.Client{Timeout: 10 * time.Second}, discoveryURL)
	return newSetupServerWithDiscoverer(cfg, configPath, opts, discoverer)
}

// newSetupServerWithDiscoverer creates a setup server with a caller-supplied
// bridgeDiscoverer. Use this in tests to inject an HTTP-only discoverer
// (via makeHTTPOnlyDiscoverer) so that mDNS timeouts do not slow tests down.
func newSetupServerWithDiscoverer(cfg *Config, configPath string, opts hue.ClientOptions, discoverer bridgeDiscoverer) (*setupServer, error) {
	srv := &setupServer{
		configuredBridge: cfg.BridgeIP,
		discoverer:       discoverer,
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
	srv.bridge = resolveBridgeStatusWithPersisted(discoverer, cfg.BridgeIP, cfg.State.BridgeIP)

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

func normalizeBridgeAddress(raw string) (string, error) {
	bridgeAddress := strings.TrimSpace(raw)
	if bridgeAddress == "" {
		return "", fmt.Errorf("bridge host/IP is required")
	}
	if strings.Contains(bridgeAddress, "://") {
		parsed, err := url.Parse(bridgeAddress)
		if err != nil {
			return "", fmt.Errorf("invalid bridge host/IP %q: %w", raw, err)
		}
		if parsed.Host == "" {
			return "", fmt.Errorf("invalid bridge host/IP %q", raw)
		}
		bridgeAddress = parsed.Host
	}
	if strings.ContainsRune(bridgeAddress, '/') {
		return "", fmt.Errorf("invalid bridge host/IP %q", raw)
	}
	return bridgeAddress, nil
}

func (s *setupServer) persistConfiguredBridgeAddress(bridgeAddress string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.configuredBridge = bridgeAddress
	s.bridge = bridgeStatus{
		Address: bridgeAddress,
		Source:  "configured",
	}
	s.config.BridgeIP = bridgeAddress
	s.config.State.BridgeIP = bridgeAddress
	return saveConfig(s.configPath, &s.config)
}

func (s *setupServer) bridgeStatusForRequest(r *http.Request) (bridgeStatus, error) {
	if err := r.ParseForm(); err != nil {
		return bridgeStatus{}, fmt.Errorf("parsing form: %w", err)
	}
	if rawBridgeAddress := r.FormValue("bridge_address"); rawBridgeAddress != "" {
		bridgeAddress, err := normalizeBridgeAddress(rawBridgeAddress)
		if err != nil {
			return bridgeStatus{}, err
		}
		if err := s.persistConfiguredBridgeAddress(bridgeAddress); err != nil {
			return bridgeStatus{}, err
		}
		return bridgeStatus{
			Address: bridgeAddress,
			Source:  "configured",
		}, nil
	}
	return s.ensureBridgeStatus()
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

	resolved := resolveBridgeStatus(s.discoverer, "")
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

	resolved := resolveBridgeStatus(s.discoverer, "")
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

	bridge, err := s.bridgeStatusForRequest(r)
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

func (s *setupServer) handleSaveBridgeAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bridge, err := s.bridgeStatusForRequest(r)
	if err != nil {
		s.renderHome(w, "", fmt.Sprintf("failed to save bridge host/IP: %v", err))
		return
	}

	s.renderHome(w, fmt.Sprintf("Bridge host/IP saved: %s.", bridge.Address), "")
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

func (s *setupServer) pageData(message, errorMessage, certError string) homePageData {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return homePageData{
		BridgeAddress:         s.bridge.Address,
		BridgeSource:          s.bridge.Source,
		BridgeDiscoveryStatus: s.bridge.DiscoveryStatus,
		DiscoveryDone:         s.bridge.Address != "",
		CertSaved:             s.config.TLSCACertFile != "",
		CertFile:              s.config.TLSCACertFile,
		CertError:             certError,
		AppKeySet:             s.appKey != "",
		Message:               message,
		ErrorMessage:          errorMessage,
	}
}

func (s *setupServer) renderHome(w http.ResponseWriter, message, errorMessage string) {
	s.renderHomeWithCertError(w, message, errorMessage, "")
}

func (s *setupServer) renderHomeWithCertError(w http.ResponseWriter, message, errorMessage, certError string) {
	var buf bytes.Buffer
	if err := homePageTemplate.Execute(&buf, s.pageData(message, errorMessage, certError)); err != nil {
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

	// Auto-fetch and save the bridge certificate whenever a bridge is known but
	// no cert has been saved yet. This runs on every page load so that a
	// transient network error is retried the next time the user visits.
	s.mu.RLock()
	bridgeAddr := s.bridge.Address
	certSaved := s.config.TLSCACertFile != ""
	s.mu.RUnlock()

	var certError string
	if bridgeAddr != "" && !certSaved {
		certPEM, err := s.fetchBridgeCert(bridgeAddr)
		if err != nil {
			certError = fmt.Sprintf("could not fetch bridge certificate: %v", err)
		} else if err := s.persistBridgeCertificate(certPEM); err != nil {
			certError = fmt.Sprintf("could not save bridge certificate: %v", err)
		}
	}

	s.renderHomeWithCertError(w, "", "", certError)
}

func (s *setupServer) handleGenerateAppKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bridge, err := s.bridgeStatusForRequest(r)
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
		mux.HandleFunc("/api/bridge", server.handleSaveBridgeAddress)
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
