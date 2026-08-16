package hue

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
