package main

import (
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

	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "bridge_ip is required") {
		t.Fatalf("expected bridge_ip error, got: %v", err)
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
