package hue

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestClient(server *httptest.Server) *Client {
	return &Client{
		baseURL:    server.URL,
		appKey:     "test-key",
		httpClient: server.Client(),
	}
}

func TestGetSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/light" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("hue-application-key"); got != "test-key" {
			t.Fatalf("unexpected app key header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"light-1","metadata":{"name":"Kitchen","archetype":"bulb"},"on":{"on":true}}]}`))
	}))
	defer server.Close()

	data, err := get[Light](newTestClient(server), "/light")
	if err != nil {
		t.Fatalf("get returned error: %v", err)
	}
	if len(data) != 1 || data[0].ID != "light-1" {
		t.Fatalf("unexpected result: %+v", data)
	}
}

func TestGetStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	_, err := get[Light](newTestClient(server), "/light")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 400") {
		t.Fatalf("expected status error, got: %v", err)
	}
}

func TestGetInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not-json"))
	}))
	defer server.Close()

	_, err := get[Light](newTestClient(server), "/light")
	if err == nil || !strings.Contains(err.Error(), "decoding response") {
		t.Fatalf("expected decoding error, got: %v", err)
	}
}

func TestGetAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[],"errors":[{"description":"bridge busy"}]}`))
	}))
	defer server.Close()

	_, err := get[Light](newTestClient(server), "/light")
	if err == nil || !strings.Contains(err.Error(), "API error") {
		t.Fatalf("expected API error, got: %v", err)
	}
}

func TestGet429RetriesAndSucceeds(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"light-1","metadata":{"name":"Kitchen","archetype":"bulb"},"on":{"on":true}}]}`))
	}))
	defer server.Close()

	c := newTestClient(server)
	// Override the fallback wait to zero so the test doesn't take 2 seconds.
	origWait := retryFallbackWait
	retryFallbackWait = 0
	defer func() { retryFallbackWait = origWait }()

	data, err := get[Light](c, "/light")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if len(data) != 1 || data[0].ID != "light-1" {
		t.Fatalf("unexpected result: %+v", data)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestGet429HonoursRetryAfterHeader(t *testing.T) {
	attempts := 0
	var sawDelay time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	origWait := retryFallbackWait
	retryFallbackWait = 5 * time.Second // would slow test if Retry-After is ignored
	defer func() { retryFallbackWait = origWait }()

	start := time.Now()
	_, err := get[Light](newTestClient(server), "/light")
	sawDelay = time.Since(start)

	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	// Retry-After: 0 → no sleep; if fallback were used it would be ≥5s.
	if sawDelay >= time.Second {
		t.Fatalf("Retry-After header was not respected; delay was %v", sawDelay)
	}
	_ = sawDelay
}

func TestGet429ExhaustsRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	origWait := retryFallbackWait
	retryFallbackWait = 0
	defer func() { retryFallbackWait = origWait }()

	_, err := get[Light](newTestClient(server), "/light")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	var reqErr *RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 RequestError, got: %v", err)
	}
	if attempts != maxRetries+1 {
		t.Fatalf("expected %d attempts, got %d", maxRetries+1, attempts)
	}
}

func TestResourceWrapperPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(*Client) error
	}{
		{name: "lights", path: "/light", call: func(c *Client) error { _, err := c.GetLights(); return err }},
		{name: "grouped lights", path: "/grouped_light", call: func(c *Client) error { _, err := c.GetGroupedLights(); return err }},
		{name: "rooms", path: "/room", call: func(c *Client) error { _, err := c.GetRooms(); return err }},
		{name: "zones", path: "/zone", call: func(c *Client) error { _, err := c.GetZones(); return err }},
		{name: "motion", path: "/motion", call: func(c *Client) error { _, err := c.GetMotion(); return err }},
		{name: "temperature", path: "/temperature", call: func(c *Client) error { _, err := c.GetTemperature(); return err }},
		{name: "light level", path: "/light_level", call: func(c *Client) error { _, err := c.GetLightLevel(); return err }},
		{name: "device power", path: "/device_power", call: func(c *Client) error { _, err := c.GetDevicePower(); return err }},
		{name: "zigbee", path: "/zigbee_connectivity", call: func(c *Client) error { _, err := c.GetZigbeeConnectivity(); return err }},
		{name: "devices", path: "/device", call: func(c *Client) error { _, err := c.GetDevices(); return err }},
		{name: "scenes", path: "/scene", call: func(c *Client) error { _, err := c.GetScenes(); return err }},
		{name: "buttons", path: "/button", call: func(c *Client) error { _, err := c.GetButtons(); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer server.Close()

			if err := tt.call(newTestClient(server)); err != nil {
				t.Fatalf("wrapper returned error: %v", err)
			}
		})
	}
}

func TestCreateAppKeySuccess(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"success":{"username":"generated-key"}}]`))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	appKey, err := CreateAppKey(u.Host, "hue_exporter#server", ClientOptions{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("CreateAppKey returned error: %v", err)
	}
	if appKey != "generated-key" {
		t.Fatalf("unexpected app key: %q", appKey)
	}
}

func TestCreateAppKeyAPIError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"error":{"description":"link button not pressed"}}]`))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	_, err = CreateAppKey(u.Host, "hue_exporter#server", ClientOptions{InsecureSkipVerify: true})
	if err == nil || !strings.Contains(err.Error(), "link button not pressed") {
		t.Fatalf("expected Hue API error, got: %v", err)
	}
}

func TestCreateAppKeyUnexpectedStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	_, err = CreateAppKey(u.Host, "hue_exporter#server", ClientOptions{InsecureSkipVerify: true})
	if err == nil || !strings.Contains(err.Error(), "unexpected status 400") {
		t.Fatalf("expected status error, got: %v", err)
	}
}

func TestCreateAppKeyMissingUsername(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"success":{}}]`))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	_, err = CreateAppKey(u.Host, "hue_exporter#server", ClientOptions{InsecureSkipVerify: true})
	if err == nil || !strings.Contains(err.Error(), "did not include a username") {
		t.Fatalf("expected missing username error, got: %v", err)
	}
}

func TestRequestErrorHelpers(t *testing.T) {
	t.Run("Error handles nil receiver", func(t *testing.T) {
		var err *RequestError
		if got := err.Error(); got != "<nil>" {
			t.Fatalf("unexpected nil error text: %q", got)
		}
	})

	t.Run("Error includes status and path", func(t *testing.T) {
		err := &RequestError{Path: "/light", StatusCode: http.StatusBadGateway, Err: errors.New("bridge down")}
		if got := err.Error(); !strings.Contains(got, "unexpected status 502") {
			t.Fatalf("unexpected error text: %q", got)
		}
	})

	t.Run("Unwrap returns wrapped error", func(t *testing.T) {
		cause := errors.New("network problem")
		err := &RequestError{Path: "/light", Err: cause}
		if !errors.Is(err, cause) {
			t.Fatal("expected errors.Is to match wrapped cause")
		}
	})
}

func TestIsConnectionError(t *testing.T) {
	if !IsConnectionError(&RequestError{Path: "/light", Err: errors.New("dial tcp"), StatusCode: 0}) {
		t.Fatal("expected connection error to be detected")
	}
	if IsConnectionError(&RequestError{Path: "/light", Err: errors.New("bad request"), StatusCode: http.StatusBadRequest}) {
		t.Fatal("did not expect status-based error to be treated as connection error")
	}
}

func TestCreateAppKeyAdditionalErrors(t *testing.T) {
	t.Run("empty API response", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[]`))
		}))
		defer server.Close()

		u, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("parse server URL: %v", err)
		}

		_, err = CreateAppKey(u.Host, "hue_exporter#server", ClientOptions{InsecureSkipVerify: true})
		if err == nil || !strings.Contains(err.Error(), "response was empty") {
			t.Fatalf("expected empty response error, got: %v", err)
		}
	})

	t.Run("invalid JSON response", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		}))
		defer server.Close()

		u, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("parse server URL: %v", err)
		}

		_, err = CreateAppKey(u.Host, "hue_exporter#server", ClientOptions{InsecureSkipVerify: true})
		if err == nil || !strings.Contains(err.Error(), "decoding app key response") {
			t.Fatalf("expected decode error, got: %v", err)
		}
	})
}

func TestCreateAppKeyWithCACertAndIPWithoutIPSAN(t *testing.T) {
	certPEM, tlsCert := createTestBridgeCertificate(t, []string{"bridge.local"})

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"success":{"username":"generated-key"}}]`))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	server.StartTLS()
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("unexpected test server host: %s", host)
	}

	appKey, err := CreateAppKey(net.JoinHostPort("127.0.0.1", port), "hue_exporter#server", ClientOptions{CACert: certPEM})
	if err != nil {
		t.Fatalf("CreateAppKey returned error: %v", err)
	}
	if appKey != "generated-key" {
		t.Fatalf("unexpected app key: %q", appKey)
	}
}

func TestCreateAppKeyWithCACertAndHostWithoutAnySAN(t *testing.T) {
	certPEM, tlsCert := createTestBridgeCertificate(t, nil)
	localhostIPs, err := net.LookupIP("localhost")
	if err != nil {
		t.Fatalf("lookup localhost: %v", err)
	}
	hasIPv4Loopback := false
	for _, ip := range localhostIPs {
		if ip.Equal(net.IPv4(127, 0, 0, 1)) {
			hasIPv4Loopback = true
			break
		}
	}
	if !hasIPv4Loopback {
		t.Skip("localhost does not resolve to 127.0.0.1 on this environment")
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"success":{"username":"generated-key"}}]`))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	server.StartTLS()
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	appKey, err := CreateAppKey(net.JoinHostPort("localhost", port), "hue_exporter#server", ClientOptions{CACert: certPEM})
	if err != nil {
		t.Fatalf("CreateAppKey returned error: %v", err)
	}
	if appKey != "generated-key" {
		t.Fatalf("unexpected app key: %q", appKey)
	}
}

func TestCreateAppKeyRejectsNameMismatchForUnpinnedLeaf(t *testing.T) {
	caPEM, tlsCert := createTestCAAndLeafCertificate(t, nil)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"success":{"username":"generated-key"}}]`))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	server.StartTLS()
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	_, err = CreateAppKey(net.JoinHostPort("127.0.0.1", port), "hue_exporter#server", ClientOptions{CACert: caPEM})
	if err == nil || !strings.Contains(err.Error(), "hostname mismatch is only allowed for pinned bridge certificates") {
		t.Fatalf("expected pinned certificate mismatch error, got: %v", err)
	}
}

func createTestBridgeCertificate(t *testing.T, dnsNames []string) ([]byte, tls.Certificate) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "bridge.local",
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("create TLS key pair: %v", err)
	}

	return certPEM, tlsCert
}

func createTestCAAndLeafCertificate(t *testing.T, leafDNSNames []string) ([]byte, tls.Certificate) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA private key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName: "test-ca.local",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf private key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject: pkix.Name{
			CommonName: "bridge.local",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              leafDNSNames,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}

	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	tlsCert, err := tls.X509KeyPair(leafCertPEM, leafKeyPEM)
	if err != nil {
		t.Fatalf("create TLS key pair: %v", err)
	}

	return caPEM, tlsCert
}
