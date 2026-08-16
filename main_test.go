package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "app_key is required") {
		t.Fatalf("expected app_key error, got: %v", err)
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	path := writeTestConfig(t, "bridge_ip: [")

	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "parsing config") {
		t.Fatalf("expected parsing error, got: %v", err)
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
