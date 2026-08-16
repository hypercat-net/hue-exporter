package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hypercat-net/hue-exporter/hue"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hue_exporter.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfigSuccess(t *testing.T) {
	path := writeTestConfig(t, `
bridge_ip: 192.168.1.2
app_key: test-key
tls_insecure_skip_verify: true
tls_ca_cert_file: /tmp/bridge-ca.pem
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.BridgeIP != "192.168.1.2" {
		t.Fatalf("unexpected bridge_ip: %q", cfg.BridgeIP)
	}
	if cfg.AppKey != "test-key" {
		t.Fatalf("unexpected app_key: %q", cfg.AppKey)
	}
	if !cfg.TLSInsecureSkipVerify {
		t.Fatal("expected tls_insecure_skip_verify=true")
	}
	if cfg.TLSCACertFile != "/tmp/bridge-ca.pem" {
		t.Fatalf("unexpected tls_ca_cert_file: %q", cfg.TLSCACertFile)
	}
}

func TestLoadConfigMissingBridgeIP(t *testing.T) {
	path := writeTestConfig(t, `
app_key: test-key
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.BridgeIP != "" {
		t.Fatalf("expected empty bridge_ip, got: %q", cfg.BridgeIP)
	}
}

func TestLoadConfigMissingAppKey(t *testing.T) {
	path := writeTestConfig(t, `
bridge_ip: 192.168.1.2
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.AppKey != "" {
		t.Fatalf("expected empty app_key, got: %q", cfg.AppKey)
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	path := writeTestConfig(t, "bridge_ip: [")

	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "parsing config") {
		t.Fatalf("expected parsing error, got: %v", err)
	}
}

func TestLoadConfigWithPersistedState(t *testing.T) {
	path := writeTestConfig(t, `
state:
  bridge_ip: 192.168.1.2
  app_key: saved-key
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.State.BridgeIP != "192.168.1.2" {
		t.Fatalf("unexpected persisted bridge IP: %q", cfg.State.BridgeIP)
	}
	if cfg.State.AppKey != "saved-key" {
		t.Fatalf("unexpected persisted app key: %q", cfg.State.AppKey)
	}
}

func TestSaveConfigWritesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "hue_exporter.yml")
	want := &Config{
		BridgeIP: "bridge.local",
		State: persistedState{
			BridgeIP: "192.168.1.2",
			AppKey:   "saved-key",
		},
	}

	if err := saveConfig(path, want); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	got, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if got.BridgeIP != want.BridgeIP {
		t.Fatalf("unexpected bridge IP: %q", got.BridgeIP)
	}
	if got.State.AppKey != want.State.AppKey {
		t.Fatalf("unexpected persisted app key: %q", got.State.AppKey)
	}
	if got.State.BridgeIP != want.State.BridgeIP {
		t.Fatalf("unexpected persisted bridge IP: %q", got.State.BridgeIP)
	}
}

func TestDiscoverBridgeIPSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"bridge-1","internalipaddress":"192.168.1.2"}]`))
	}))
	defer server.Close()

	bridgeIP, err := discoverBridgeIP(server.Client(), server.URL)
	if err != nil {
		t.Fatalf("discoverBridgeIP returned error: %v", err)
	}
	if bridgeIP != "192.168.1.2" {
		t.Fatalf("unexpected bridge IP: %q", bridgeIP)
	}
}

func TestDiscoverBridgeIPNoBridges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	_, err := discoverBridgeIP(server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "no Hue bridges discovered") {
		t.Fatalf("expected no bridges error, got: %v", err)
	}
}

func TestDiscoverBridgeIPMultipleBridges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"bridge-1","internalipaddress":"192.168.1.2"},
			{"id":"bridge-2","internalipaddress":"192.168.1.3"}
		]`))
	}))
	defer server.Close()

	_, err := discoverBridgeIP(server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "discovered 2 Hue bridges") {
		t.Fatalf("expected multiple bridges error, got: %v", err)
	}
}

func TestDiscoverBridgeIPStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := discoverBridgeIP(server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "Hue discovery returned status 502") {
		t.Fatalf("expected status error, got: %v", err)
	}
}

func TestDiscoverBridgeIPMissingIPAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"bridge-1","internalipaddress":""}]`))
	}))
	defer server.Close()

	_, err := discoverBridgeIP(server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "missing internal IP address") {
		t.Fatalf("expected missing IP error, got: %v", err)
	}
}

func TestDiscoverBridgeIPInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{"))
	}))
	defer server.Close()

	_, err := discoverBridgeIP(server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "decoding Hue discovery response") {
		t.Fatalf("expected decode error, got: %v", err)
	}
}

func TestRunHealthcheckSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := runHealthcheck(server.URL); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRunHealthcheckNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := runHealthcheck(server.URL)
	if err == nil || !strings.Contains(err.Error(), "unexpected status code 503") {
		t.Fatalf("expected status code error, got: %v", err)
	}
}

func TestRunHealthcheckRequestFailure(t *testing.T) {
	err := runHealthcheck("://bad-url")
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("expected request failure error, got: %v", err)
	}
}

func TestHomePageShowsBridgeAndAppKeyStatus(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, configPath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newMux(server, true).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Bridge host/IP: bridge.local") {
		t.Fatalf("expected bridge address in page, got: %s", body)
	}
	if !strings.Contains(body, "API key set: no") {
		t.Fatalf("expected missing app key status, got: %s", body)
	}
	if !strings.Contains(body, "Bridge certificate file: not configured") {
		t.Fatalf("expected certificate status, got: %s", body)
	}
}

func TestNewSetupServerUsesPersistedBridgeIP(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	if err := saveConfig(configPath, &Config{
		State: persistedState{
			BridgeIP: "bridge.local",
			AppKey:   "saved-key",
		},
	}); err != nil {
		t.Fatalf("saveConfig returned error: %v", err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}

	server, err := newSetupServer(cfg, configPath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	if server.bridge.Address != "bridge.local" {
		t.Fatalf("unexpected bridge address: %q", server.bridge.Address)
	}
	if server.bridge.Source != "persisted" {
		t.Fatalf("unexpected bridge source: %q", server.bridge.Source)
	}
	if server.appKey != "saved-key" {
		t.Fatalf("unexpected app key: %q", server.appKey)
	}
}

func TestNewSetupServerPersistsDiscoveredBridgeIP(t *testing.T) {
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"bridge-1","internalipaddress":"192.168.1.2"}]`))
	}))
	defer discovery.Close()

	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{}, configPath, hue.ClientOptions{}, discovery.URL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	if server.bridge.Address != "192.168.1.2" {
		t.Fatalf("unexpected bridge address: %q", server.bridge.Address)
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.State.BridgeIP != "192.168.1.2" {
		t.Fatalf("unexpected persisted bridge IP: %q", cfg.State.BridgeIP)
	}
}

func TestGenerateAppKeyPersistsState(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, configPath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}
	server.createAppKey = func(bridgeAddress string) (string, error) {
		if bridgeAddress != "bridge.local" {
			t.Fatalf("unexpected bridge address: %q", bridgeAddress)
		}
		return "generated-key", nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/key", nil)
	rec := httptest.NewRecorder()
	newMux(server, true).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "API key generated and saved.") {
		t.Fatalf("expected success message, got: %s", body)
	}
	if !strings.Contains(body, "API key set: yes") {
		t.Fatalf("expected app key status, got: %s", body)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.State.AppKey != "generated-key" {
		t.Fatalf("unexpected persisted app key: %q", cfg.State.AppKey)
	}
	if cfg.State.BridgeIP != "bridge.local" {
		t.Fatalf("unexpected persisted bridge IP: %q", cfg.State.BridgeIP)
	}
}

func TestGenerateAppKeyMethodNotAllowed(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, configPath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/key", nil)
	rec := httptest.NewRecorder()
	newMux(server, true).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

func TestSaveBridgeCertificateMethodNotAllowed(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, configPath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/cert", nil)
	rec := httptest.NewRecorder()
	newMux(server, true).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

func TestSaveBridgeCertificatePersistsConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local", TLSInsecureSkipVerify: true}, configPath, hue.ClientOptions{InsecureSkipVerify: true}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}
	wantPEM := []byte("-----BEGIN CERTIFICATE-----\nZmFrZS1jZXJ0\n-----END CERTIFICATE-----\n")
	server.fetchBridgeCert = func(bridgeAddress string) ([]byte, error) {
		if bridgeAddress != "bridge.local" {
			t.Fatalf("unexpected bridge address: %q", bridgeAddress)
		}
		return wantPEM, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/cert", nil)
	rec := httptest.NewRecorder()
	newMux(server, true).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Bridge certificate saved and config updated.") {
		t.Fatalf("expected success message, got: %s", body)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	wantCertPath := filepath.Join(filepath.Dir(configPath), "bridge-ca.pem")
	if cfg.TLSCACertFile != wantCertPath {
		t.Fatalf("unexpected tls_ca_cert_file: %q", cfg.TLSCACertFile)
	}
	if cfg.TLSInsecureSkipVerify {
		t.Fatal("expected tls_insecure_skip_verify to be false")
	}
	gotPEM, err := os.ReadFile(wantCertPath)
	if err != nil {
		t.Fatalf("read saved cert file: %v", err)
	}
	if string(gotPEM) != string(wantPEM) {
		t.Fatalf("unexpected cert file contents: %q", string(gotPEM))
	}
	if server.opts.InsecureSkipVerify {
		t.Fatal("expected runtime insecure-skip-verify to be false")
	}
	if string(server.opts.CACert) != string(wantPEM) {
		t.Fatalf("unexpected runtime cert contents: %q", string(server.opts.CACert))
	}
}

func TestSetupUIDisabledByDefaultInMux(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, configPath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}
	mux := newMux(server, false)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/"},
		{method: http.MethodPost, path: "/api/key"},
		{method: http.MethodPost, path: "/api/cert"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s %s, got %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestSetupUIEnabledFromFlagAndEnv(t *testing.T) {
	t.Run("default disabled", func(t *testing.T) {
		t.Setenv("HUE_EXPORTER_ENABLE_SETUP_UI", "")
		enabled, err := setupUIEnabled(false)
		if err != nil {
			t.Fatalf("setupUIEnabled returned error: %v", err)
		}
		if enabled {
			t.Fatal("expected setup UI disabled by default")
		}
	})

	t.Run("flag enables", func(t *testing.T) {
		t.Setenv("HUE_EXPORTER_ENABLE_SETUP_UI", "")
		enabled, err := setupUIEnabled(true)
		if err != nil {
			t.Fatalf("setupUIEnabled returned error: %v", err)
		}
		if !enabled {
			t.Fatal("expected setup UI enabled from flag")
		}
	})

	t.Run("env enables", func(t *testing.T) {
		t.Setenv("HUE_EXPORTER_ENABLE_SETUP_UI", "true")
		enabled, err := setupUIEnabled(false)
		if err != nil {
			t.Fatalf("setupUIEnabled returned error: %v", err)
		}
		if !enabled {
			t.Fatal("expected setup UI enabled from environment")
		}
	})

	t.Run("env false overrides flag", func(t *testing.T) {
		t.Setenv("HUE_EXPORTER_ENABLE_SETUP_UI", "false")
		enabled, err := setupUIEnabled(true)
		if err != nil {
			t.Fatalf("setupUIEnabled returned error: %v", err)
		}
		if enabled {
			t.Fatal("expected setup UI disabled from environment override")
		}
	})

	t.Run("invalid env fails", func(t *testing.T) {
		t.Setenv("HUE_EXPORTER_ENABLE_SETUP_UI", "definitely-not-bool")
		_, err := setupUIEnabled(false)
		if err == nil || !strings.Contains(err.Error(), "invalid HUE_EXPORTER_ENABLE_SETUP_UI value") {
			t.Fatalf("expected invalid env error, got: %v", err)
		}
	})
}

func TestMetricsUnavailableWithoutAppKey(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, configPath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	newMux(server, true).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "app_key is not configured") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestGetLightsRediscoveryUpdatesPersistedBridgeIP(t *testing.T) {
	firstBridge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"light-old","metadata":{"name":"Old","archetype":"bulb"},"on":{"on":true}}]}`))
	}))
	t.Cleanup(func() {
		firstBridge.Close()
	})

	secondBridge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"light-new","metadata":{"name":"New","archetype":"bulb"},"on":{"on":true}}]}`))
	}))
	defer secondBridge.Close()

	firstURL, err := url.Parse(firstBridge.URL)
	if err != nil {
		t.Fatalf("parse first bridge URL: %v", err)
	}

	func TestBuildClientOptions(t *testing.T) {
		t.Run("uses insecure skip verify from config", func(t *testing.T) {
			opts, err := buildClientOptions(&Config{TLSInsecureSkipVerify: true})
			if err != nil {
				t.Fatalf("buildClientOptions returned error: %v", err)
			}
			if !opts.InsecureSkipVerify {
				t.Fatal("expected InsecureSkipVerify to be true")
			}
		})

		t.Run("loads CA cert bytes from file", func(t *testing.T) {
			certPath := filepath.Join(t.TempDir(), "bridge-ca.pem")
			want := []byte("test-ca-cert")
			if err := os.WriteFile(certPath, want, 0o600); err != nil {
				t.Fatalf("write cert file: %v", err)
			}

			opts, err := buildClientOptions(&Config{TLSCACertFile: certPath})
			if err != nil {
				t.Fatalf("buildClientOptions returned error: %v", err)
			}
			if string(opts.CACert) != string(want) {
				t.Fatalf("unexpected CA cert bytes: %q", string(opts.CACert))
			}
			if opts.InsecureSkipVerify {
				t.Fatal("expected InsecureSkipVerify to be false when CA cert is configured")
			}
		})

		t.Run("returns error when cert file is unreadable", func(t *testing.T) {
			_, err := buildClientOptions(&Config{TLSCACertFile: filepath.Join(t.TempDir(), "missing.pem")})
			if err == nil || !strings.Contains(err.Error(), "reading CA cert file") {
				t.Fatalf("expected cert read error, got: %v", err)
			}
		})
	}

	func TestResolveBridgeStatusConfigured(t *testing.T) {
		got := resolveBridgeStatus(&http.Client{}, "bridge.local", "https://unused.local")
		if got.Address != "bridge.local" || got.Source != "configured" || got.Error != "" {
			t.Fatalf("unexpected bridge status: %+v", got)
		}
	}

	func TestResolveBridgeStatusDiscoveryError(t *testing.T) {
		got := resolveBridgeStatus(&http.Client{Timeout: 100 * time.Millisecond}, "", "http://127.0.0.1:1")
		if got.Address != "" {
			t.Fatalf("expected empty address, got: %q", got.Address)
		}
		if got.Source != "discovered" {
			t.Fatalf("unexpected source: %q", got.Source)
		}
		if got.Error == "" {
			t.Fatal("expected discovery error")
		}
	}

	func TestEnsureBridgeStatusDiscoversAndPersists(t *testing.T) {
		discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"bridge-1","internalipaddress":"192.168.1.20"}]`))
		}))
		defer discovery.Close()

		configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
		server := &setupServer{
			discoveryClient: discovery.Client(),
			discoveryURL:    discovery.URL,
			configPath:      configPath,
			config:          Config{},
		}

		bridge, err := server.ensureBridgeStatus()
		if err != nil {
			t.Fatalf("ensureBridgeStatus returned error: %v", err)
		}
		if bridge.Address != "192.168.1.20" {
			t.Fatalf("unexpected bridge address: %q", bridge.Address)
		}

		cfg, err := loadConfig(configPath)
		if err != nil {
			t.Fatalf("loadConfig returned error: %v", err)
		}
		if cfg.State.BridgeIP != "192.168.1.20" {
			t.Fatalf("unexpected persisted bridge IP: %q", cfg.State.BridgeIP)
		}
	}

	func TestSetupServerBridgeWrappers(t *testing.T) {
		bridge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		}))
		defer bridge.Close()

		bridgeURL, err := url.Parse(bridge.URL)
		if err != nil {
			t.Fatalf("parse bridge URL: %v", err)
		}

		configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
		server, err := newSetupServer(
			&Config{BridgeIP: bridgeURL.Host, AppKey: "saved-key", TLSInsecureSkipVerify: true},
			configPath,
			hue.ClientOptions{InsecureSkipVerify: true},
			hueDiscoveryURL,
		)
		if err != nil {
			t.Fatalf("newSetupServer returned error: %v", err)
		}

		tests := []struct {
			name string
			call func() error
		}{
			{name: "GetGroupedLights", call: func() error { _, err := server.GetGroupedLights(); return err }},
			{name: "GetRooms", call: func() error { _, err := server.GetRooms(); return err }},
			{name: "GetZones", call: func() error { _, err := server.GetZones(); return err }},
			{name: "GetMotion", call: func() error { _, err := server.GetMotion(); return err }},
			{name: "GetTemperature", call: func() error { _, err := server.GetTemperature(); return err }},
			{name: "GetLightLevel", call: func() error { _, err := server.GetLightLevel(); return err }},
			{name: "GetDevicePower", call: func() error { _, err := server.GetDevicePower(); return err }},
			{name: "GetZigbeeConnectivity", call: func() error { _, err := server.GetZigbeeConnectivity(); return err }},
			{name: "GetDevices", call: func() error { _, err := server.GetDevices(); return err }},
			{name: "GetScenes", call: func() error { _, err := server.GetScenes(); return err }},
			{name: "GetButtons", call: func() error { _, err := server.GetButtons(); return err }},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.call(); err != nil {
					t.Fatalf("%s returned error: %v", tc.name, err)
				}
			})
		}
	}
	secondURL, err := url.Parse(secondBridge.URL)
	if err != nil {
		t.Fatalf("parse second bridge URL: %v", err)
	}

	currentHost := firstURL.Host
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"bridge-1","internalipaddress":"` + currentHost + `"}]`))
	}))
	defer discovery.Close()

	configPath := filepath.Join(t.TempDir(), "hue_exporter.yml")
	server, err := newSetupServer(&Config{AppKey: "saved-key", TLSInsecureSkipVerify: true}, configPath, hue.ClientOptions{InsecureSkipVerify: true}, discovery.URL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	firstBridge.Close()
	currentHost = secondURL.Host

	lights, err := server.GetLights()
	if err != nil {
		t.Fatalf("GetLights returned error: %v", err)
	}
	if len(lights) != 1 || lights[0].ID != "light-new" {
		t.Fatalf("unexpected lights: %+v", lights)
	}
	if server.bridge.Address != secondURL.Host {
		t.Fatalf("unexpected bridge address after rediscovery: %q", server.bridge.Address)
	}
	if server.bridge.Source != "discovered" {
		t.Fatalf("unexpected bridge source after rediscovery: %q", server.bridge.Source)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.State.BridgeIP != secondURL.Host {
		t.Fatalf("unexpected persisted bridge IP after rediscovery: %q", cfg.State.BridgeIP)
	}
}
