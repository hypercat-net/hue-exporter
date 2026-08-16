package hue_test

import (
	"testing"

	"github.com/hypercat-net/hue-exporter/hue"
)

func TestNewClientDefault(t *testing.T) {
	_, err := hue.NewClient("192.168.1.1", "testkey", hue.ClientOptions{})
	if err != nil {
		t.Fatalf("NewClient with default options: %v", err)
	}
}

func TestNewClientInsecureSkipVerify(t *testing.T) {
	_, err := hue.NewClient("192.168.1.1", "testkey", hue.ClientOptions{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("NewClient with InsecureSkipVerify: %v", err)
	}
}

func TestNewClientValidCACert(t *testing.T) {
	// A real self-signed PEM certificate for testing cert pool parsing.
	testCAPEM := []byte(`-----BEGIN CERTIFICATE-----
MIIC/zCCAeegAwIBAgIUQdDVGZUEsn1rQEytLQ5eJmdbDdgwDQYJKoZIhvcNAQEL
BQAwDzENMAsGA1UEAwwEdGVzdDAeFw0yNjA4MTYwOTM4MjJaFw0yNjA4MTcwOTM4
MjJaMA8xDTALBgNVBAMMBHRlc3QwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEK
AoIBAQCdJV93c8ebVVrx5LdRWB/Z/xYTmGlVPJzvWeWXbbOjUY6/MQ0F1FXUe9Ob
mdXqVnNUc0dqslw7H9gd3ujIEc7UUNV9tgLuH1DUPipUdaRSeYHssZjj2teDUUNu
Zo6uSZfr5GMhXsRXCCBeQmt9WGR5zxlQMmWKaPv+C6Bk0UTdT1mQfMlIJyD9PLrV
95Q4wU5q9hK/4FOre7v+0lWpApsJYpt8mamUe2F/W2rBvG0JNfYU/XhdQe8EzUzP
aviDko8sitAZugLMCZZElmm1vKRT10UY/jmNBdn/CWz8+3zzJiRzDk3Rv2LovGgf
FscKsLahn/hEiI4mMYaJS8D8kRj1AgMBAAGjUzBRMB0GA1UdDgQWBBROY01R8Tvt
zsd+5vnOd9WB3C0P9DAfBgNVHSMEGDAWgBROY01R8Tvtzsd+5vnOd9WB3C0P9DAP
BgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQAO5NCfHP3r6qQVC+1Y
rIIxZyMS2a/bWujIhb+NSZgb/LmIFrWJJBDcx7/shToi9muQ0UakfvNnOCAMStnq
NK0AjUQaWcxTWj6mYwDKki2ukFJH6oyLKRLgnA9w5QE1A0IxToQqK+jVGjZl8Xqe
5/dR8/LqrGQeoBkp5v9D7AaAkt2hdCz2VTiLOEMZI8hb8L6xT9eTrkCbXrKTib87
HVNAAHeLbGMLb/WpFCnpQSI0yKgtAJ3WXMpOJBQGGRnxUqtrypB7MMpwkn4NdAII
MOnVYAztykjtxzJMZM0hEgv2qG7mTtkRZj3bJjqEvqoS/v0qXBvQAIS6zqgBJzuD
u/8X
-----END CERTIFICATE-----`)

	_, err := hue.NewClient("192.168.1.1", "testkey", hue.ClientOptions{CACert: testCAPEM})
	if err != nil {
		t.Fatalf("NewClient with valid CA cert: %v", err)
	}
}

func TestNewClientInvalidCACert(t *testing.T) {
	_, err := hue.NewClient("192.168.1.1", "testkey", hue.ClientOptions{CACert: []byte("not a valid PEM certificate")})
	if err == nil {
		t.Fatal("expected error for invalid CA cert, got nil")
	}
}
