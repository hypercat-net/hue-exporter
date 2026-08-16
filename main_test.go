package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestLoadPersistedStateMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yml")

	state, err := loadPersistedState(path)
	if err != nil {
		t.Fatalf("loadPersistedState returned error: %v", err)
	}
	if state.AppKey != "" {
		t.Fatalf("expected empty app key, got: %q", state.AppKey)
	}
}

func TestSaveAndLoadPersistedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "hue_exporter_state.yml")
	want := &persistedState{AppKey: "saved-key"}

	if err := savePersistedState(path, want); err != nil {
		t.Fatalf("savePersistedState returned error: %v", err)
	}

	got, err := loadPersistedState(path)
	if err != nil {
		t.Fatalf("loadPersistedState returned error: %v", err)
	}
	if got.AppKey != want.AppKey {
		t.Fatalf("unexpected app key: %q", got.AppKey)
	}
}

func TestLoadPersistedStateInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yml")
	if err := os.WriteFile(path, []byte("app_key: ["), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, err := loadPersistedState(path)
	if err == nil || !strings.Contains(err.Error(), "parsing persisted state") {
		t.Fatalf("expected persisted state parse error, got: %v", err)
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
	statePath := filepath.Join(t.TempDir(), "state.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, statePath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newMux(server).ServeHTTP(rec, req)

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
}

func TestGenerateAppKeyPersistsState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, statePath, hue.ClientOptions{}, hueDiscoveryURL)
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
	newMux(server).ServeHTTP(rec, req)

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

	state, err := loadPersistedState(statePath)
	if err != nil {
		t.Fatalf("loadPersistedState returned error: %v", err)
	}
	if state.AppKey != "generated-key" {
		t.Fatalf("unexpected persisted app key: %q", state.AppKey)
	}
}

func TestGenerateAppKeyMethodNotAllowed(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, statePath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/key", nil)
	rec := httptest.NewRecorder()
	newMux(server).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}

func TestMetricsUnavailableWithoutAppKey(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.yml")
	server, err := newSetupServer(&Config{BridgeIP: "bridge.local"}, statePath, hue.ClientOptions{}, hueDiscoveryURL)
	if err != nil {
		t.Fatalf("newSetupServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	newMux(server).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "app_key is not configured") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
