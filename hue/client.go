// Package hue provides a client for the Philips Hue CLIP v2 REST API.
package hue

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	maxRetries     = 3
)

var retryFallbackWait = time.Second

// RequestError describes a failed bridge request.
type RequestError struct {
	Path       string
	StatusCode int
	Err        error
}

func (e *RequestError) Error() string {
	switch {
	case e == nil:
		return "<nil>"
	case e.StatusCode != 0:
		return fmt.Sprintf("unexpected status %d from %s: %v", e.StatusCode, e.Path, e.Err)
	case e.Err != nil:
		return fmt.Sprintf("request to %s failed: %v", e.Path, e.Err)
	default:
		return fmt.Sprintf("request to %s failed", e.Path)
	}
}

func (e *RequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsConnectionError reports whether err indicates that the bridge was
// unreachable before it returned an HTTP response.
func IsConnectionError(err error) bool {
	var requestErr *RequestError
	return errors.As(err, &requestErr) && requestErr != nil && requestErr.Err != nil && requestErr.StatusCode == 0
}

// apiResponse is the standard envelope returned by every CLIP v2 endpoint.
type apiResponse[T any] struct {
	Data   []T        `json:"data"`
	Errors []apiError `json:"errors"`
}

type apiError struct {
	Description string `json:"description"`
}

// Client talks to a Hue bridge using the CLIP v2 API.
type Client struct {
	baseURL    string
	appKey     string
	httpClient *http.Client
}

// ClientOptions holds optional configuration for the Hue client.
type ClientOptions struct {
	// InsecureSkipVerify disables TLS certificate verification. Set this to
	// true only when connecting to a bridge with a self-signed certificate and
	// no CA cert is available.
	InsecureSkipVerify bool
	// CACert is a PEM-encoded CA certificate used to verify the bridge's TLS
	// certificate. When non-nil, InsecureSkipVerify is ignored.
	CACert []byte
}

// NewClient creates a new Hue v2 API client.
//
// bridgeIP is the IP address or hostname of the bridge.
// appKey is the Hue application key (formerly called "username" in v1).
func NewClient(bridgeIP, appKey string, opts ClientOptions) (*Client, error) {
	httpClient, err := newHTTPClient(opts, bridgeIP)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL:    fmt.Sprintf("https://%s/clip/v2/resource", bridgeIP),
		appKey:     appKey,
		httpClient: httpClient,
	}, nil
}

func newHTTPClient(opts ClientOptions, bridgeAddress string) (*http.Client, error) {
	tlsCfg := &tls.Config{}
	verifyName := bridgeTLSServerName(bridgeAddress)

	if len(opts.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(opts.CACert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		pinnedCerts := map[string]struct{}{}
		for remaining := opts.CACert; len(remaining) > 0; {
			block, rest := pem.Decode(remaining)
			if block == nil {
				break
			}
			remaining = rest
			if block.Type != "CERTIFICATE" {
				continue
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("failed to parse CA certificate")
			}
			pinnedCerts[string(cert.Raw)] = struct{}{}
		}
		tlsCfg.RootCAs = pool
		// Hue bridge certificates may not include SAN entries for the configured
		// bridge address. Validate the certificate chain and allow name mismatch
		// only when the bridge certificate itself is pinned in CACert.
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // Verification is enforced in VerifyConnection below.
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("bridge TLS handshake returned no peer certificates")
			}
			baseVerifyOpts := x509.VerifyOptions{
				Roots:       pool,
				CurrentTime: time.Now(),
				KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}
			if len(cs.PeerCertificates) > 1 {
				intermediatePool := x509.NewCertPool()
				for _, cert := range cs.PeerCertificates[1:] {
					intermediatePool.AddCert(cert)
				}
				baseVerifyOpts.Intermediates = intermediatePool
			}
			if _, err := cs.PeerCertificates[0].Verify(baseVerifyOpts); err != nil {
				return fmt.Errorf("verifying bridge certificate: %w", err)
			}
			if verifyName == "" {
				return nil
			}
			verifyOpts := baseVerifyOpts
			verifyOpts.DNSName = verifyName
			_, err := cs.PeerCertificates[0].Verify(verifyOpts)
			if err == nil {
				return nil
			}
			var hostErr x509.HostnameError
			var invalidErr x509.CertificateInvalidError
			if errors.As(err, &hostErr) || (errors.As(err, &invalidErr) && invalidErr.Reason == x509.NameMismatch) {
				if _, ok := pinnedCerts[string(cs.PeerCertificates[0].Raw)]; !ok {
					return fmt.Errorf("verifying bridge certificate: hostname mismatch is only allowed for pinned bridge certificates")
				}
				return nil
			}
			return fmt.Errorf("verifying bridge certificate: %w", err)
		}
	} else if opts.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // Explicitly requested by caller for self-signed bridge cert
	}

	return &http.Client{
		Timeout:   defaultTimeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, nil
}

func bridgeTLSServerName(bridgeAddress string) string {
	host, _, err := net.SplitHostPort(bridgeAddress)
	if err == nil {
		return host
	}
	return bridgeAddress
}

type createAppKeyRequest struct {
	DeviceType string `json:"devicetype"`
}

type createAppKeyResponse struct {
	Success struct {
		Username string `json:"username"`
	} `json:"success"`
	Error *struct {
		Description string `json:"description"`
	} `json:"error,omitempty"`
}

// CreateAppKey requests a new Hue application key from the bridge.
func CreateAppKey(bridgeIP, deviceType string, opts ClientOptions) (string, error) {
	httpClient, err := newHTTPClient(opts, bridgeIP)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(createAppKeyRequest{DeviceType: deviceType})
	if err != nil {
		return "", fmt.Errorf("encoding app key request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://%s/api", bridgeIP), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating app key request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating app key: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading app key response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d when creating app key: %s", resp.StatusCode, respBody)
	}

	var responses []createAppKeyResponse
	if err := json.Unmarshal(respBody, &responses); err != nil {
		return "", fmt.Errorf("decoding app key response: %w", err)
	}
	if len(responses) == 0 {
		return "", fmt.Errorf("app key response was empty")
	}
	if responses[0].Error != nil {
		return "", fmt.Errorf("Hue API error when creating app key: %s", responses[0].Error.Description)
	}
	if responses[0].Success.Username == "" {
		return "", fmt.Errorf("app key response did not include a username")
	}
	return responses[0].Success.Username, nil
}

// get fetches a CLIP v2 resource endpoint and decodes the response.
// On HTTP 429 (Too Many Requests) it retries up to maxRetries times, honouring
// the Retry-After header when present.
func get[T any](c *Client, path string) ([]T, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request for %s: %w", path, err)
		}
		req.Header.Set("hue-application-key", c.appKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, &RequestError{Path: path, Err: err}
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, &RequestError{Path: path, Err: err}
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			wait := retryFallbackWait
			if s := resp.Header.Get("Retry-After"); s != "" {
				if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
					wait = time.Duration(secs) * time.Second
				}
			}
			time.Sleep(wait)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, &RequestError{Path: path, StatusCode: resp.StatusCode, Err: fmt.Errorf("%s", body)}
		}

		var envelope apiResponse[T]
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("decoding response from %s: %w", path, err)
		}
		if len(envelope.Errors) > 0 {
			return nil, fmt.Errorf("API error from %s: %s", path, envelope.Errors[0].Description)
		}
		return envelope.Data, nil
	}
}

// ---- Resource types --------------------------------------------------------

// ResourceRef is a reference to another resource (rid + rtype).
type ResourceRef struct {
	RID   string `json:"rid"`
	RType string `json:"rtype"`
}

// Metadata holds the human-readable name and archetype of a resource.
type Metadata struct {
	Name      string `json:"name"`
	Archetype string `json:"archetype"`
}

// OnState represents an on/off state.
type OnState struct {
	On bool `json:"on"`
}

// Dimming represents a brightness value (0–100%).
type Dimming struct {
	Brightness float64 `json:"brightness"`
}

// ColorTemperature holds the mirek value and a validity flag.
type ColorTemperature struct {
	Mirek      *int `json:"mirek"`
	MirekValid bool `json:"mirek_valid"`
}

// XY is a CIE 1931 xy chromaticity coordinate.
type XY struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Color holds the CIE xy color of a light.
type Color struct {
	XY XY `json:"xy"`
}

// Light is a CLIP v2 light resource.
type Light struct {
	ID               string            `json:"id"`
	Metadata         Metadata          `json:"metadata"`
	On               OnState           `json:"on"`
	Dimming          *Dimming          `json:"dimming"`
	ColorTemperature *ColorTemperature `json:"color_temperature"`
	Color            *Color            `json:"color"`
	Owner            ResourceRef       `json:"owner"`
}

// GroupedLight is a CLIP v2 grouped_light resource (aggregate state of a room/zone).
type GroupedLight struct {
	ID      string      `json:"id"`
	On      OnState     `json:"on"`
	Dimming *Dimming    `json:"dimming"`
	Owner   ResourceRef `json:"owner"`
}

// Room is a CLIP v2 room resource.
type Room struct {
	ID       string        `json:"id"`
	Metadata Metadata      `json:"metadata"`
	Services []ResourceRef `json:"services"`
}

// Zone is a CLIP v2 zone resource.
type Zone struct {
	ID       string        `json:"id"`
	Metadata Metadata      `json:"metadata"`
	Services []ResourceRef `json:"services"`
}

// MotionReport contains the timestamped motion detection state.
type MotionReport struct {
	Changed string `json:"changed"`
	Motion  bool   `json:"motion"`
}

// MotionSensor contains the motion state.
type MotionSensor struct {
	Motion       bool          `json:"motion"`
	MotionReport *MotionReport `json:"motion_report"`
}

// Motion is a CLIP v2 motion resource.
type Motion struct {
	ID      string       `json:"id"`
	Enabled bool         `json:"enabled"`
	Motion  MotionSensor `json:"motion"`
	Owner   ResourceRef  `json:"owner"`
}

// TemperatureReport contains the timestamped temperature reading.
type TemperatureReport struct {
	Changed     string  `json:"changed"`
	Temperature float64 `json:"temperature"`
}

// TemperatureSensor contains the temperature state.
type TemperatureSensor struct {
	Temperature       float64            `json:"temperature"`
	TemperatureValid  bool               `json:"temperature_valid"`
	TemperatureReport *TemperatureReport `json:"temperature_report"`
}

// Temperature is a CLIP v2 temperature resource.
type Temperature struct {
	ID          string            `json:"id"`
	Enabled     bool              `json:"enabled"`
	Temperature TemperatureSensor `json:"temperature"`
	Owner       ResourceRef       `json:"owner"`
}

// LightLevelReport contains the timestamped light level reading.
type LightLevelReport struct {
	Changed    string `json:"changed"`
	LightLevel int    `json:"light_level"`
}

// LightLevelSensor contains the light level state.
type LightLevelSensor struct {
	LightLevel       int               `json:"light_level"`
	LightLevelValid  bool              `json:"light_level_valid"`
	LightLevelReport *LightLevelReport `json:"light_level_report"`
}

// LightLevel is a CLIP v2 light_level resource.
type LightLevel struct {
	ID      string           `json:"id"`
	Enabled bool             `json:"enabled"`
	Light   LightLevelSensor `json:"light"`
	Owner   ResourceRef      `json:"owner"`
}

// PowerState holds battery information.
type PowerState struct {
	BatteryState string `json:"battery_state"`
	BatteryLevel *int   `json:"battery_level"`
}

// DevicePower is a CLIP v2 device_power resource.
type DevicePower struct {
	ID         string      `json:"id"`
	PowerState PowerState  `json:"power_state"`
	Owner      ResourceRef `json:"owner"`
}

// ZigbeeConnectivity is a CLIP v2 zigbee_connectivity resource.
type ZigbeeConnectivity struct {
	ID         string      `json:"id"`
	Status     string      `json:"status"`
	MACAddress string      `json:"mac_address"`
	Owner      ResourceRef `json:"owner"`
}

// Device is a CLIP v2 device resource.
type Device struct {
	ID          string        `json:"id"`
	Metadata    Metadata      `json:"metadata"`
	ProductData ProductData   `json:"product_data"`
	Services    []ResourceRef `json:"services"`
}

// ProductData holds product metadata for a device.
type ProductData struct {
	ModelID          string `json:"model_id"`
	ManufacturerName string `json:"manufacturer_name"`
	ProductName      string `json:"product_name"`
	SoftwareVersion  string `json:"software_version"`
}

// Scene is a CLIP v2 scene resource.
type Scene struct {
	ID       string      `json:"id"`
	Metadata Metadata    `json:"metadata"`
	Group    ResourceRef `json:"group"`
	Status   SceneStatus `json:"status"`
}

// SceneStatus holds the active state of a scene.
type SceneStatus struct {
	Active string `json:"active"`
}

// ButtonReport contains the timestamped last button event.
type ButtonReport struct {
	Updated string `json:"updated"`
	Event   string `json:"event"`
}

// ButtonState holds the last button event.
type ButtonState struct {
	LastEvent    string        `json:"last_event"`
	ButtonReport *ButtonReport `json:"button_report"`
}

// Button is a CLIP v2 button resource.
type Button struct {
	ID     string      `json:"id"`
	Button ButtonState `json:"button"`
	Owner  ResourceRef `json:"owner"`
}

// ---- Bridge interface (enables testing with a mock) ------------------------

// Bridge is the interface used by collectors to fetch Hue resources.
// It can be satisfied by *Client for production use or a mock for testing.
type Bridge interface {
	GetLights() ([]Light, error)
	GetGroupedLights() ([]GroupedLight, error)
	GetRooms() ([]Room, error)
	GetZones() ([]Zone, error)
	GetMotion() ([]Motion, error)
	GetTemperature() ([]Temperature, error)
	GetLightLevel() ([]LightLevel, error)
	GetDevicePower() ([]DevicePower, error)
	GetZigbeeConnectivity() ([]ZigbeeConnectivity, error)
	GetDevices() ([]Device, error)
	GetScenes() ([]Scene, error)
	GetButtons() ([]Button, error)
}

// GetLights returns all lights from the bridge.
func (c *Client) GetLights() ([]Light, error) {
	return get[Light](c, "/light")
}

// GetGroupedLights returns all grouped lights from the bridge.
func (c *Client) GetGroupedLights() ([]GroupedLight, error) {
	return get[GroupedLight](c, "/grouped_light")
}

// GetRooms returns all rooms from the bridge.
func (c *Client) GetRooms() ([]Room, error) {
	return get[Room](c, "/room")
}

// GetZones returns all zones from the bridge.
func (c *Client) GetZones() ([]Zone, error) {
	return get[Zone](c, "/zone")
}

// GetMotion returns all motion sensors from the bridge.
func (c *Client) GetMotion() ([]Motion, error) {
	return get[Motion](c, "/motion")
}

// GetTemperature returns all temperature sensors from the bridge.
func (c *Client) GetTemperature() ([]Temperature, error) {
	return get[Temperature](c, "/temperature")
}

// GetLightLevel returns all light-level sensors from the bridge.
func (c *Client) GetLightLevel() ([]LightLevel, error) {
	return get[LightLevel](c, "/light_level")
}

// GetDevicePower returns all device-power resources from the bridge.
func (c *Client) GetDevicePower() ([]DevicePower, error) {
	return get[DevicePower](c, "/device_power")
}

// GetZigbeeConnectivity returns all Zigbee connectivity resources from the bridge.
func (c *Client) GetZigbeeConnectivity() ([]ZigbeeConnectivity, error) {
	return get[ZigbeeConnectivity](c, "/zigbee_connectivity")
}

// GetDevices returns all devices from the bridge.
func (c *Client) GetDevices() ([]Device, error) {
	return get[Device](c, "/device")
}

// GetScenes returns all scenes from the bridge.
func (c *Client) GetScenes() ([]Scene, error) {
	return get[Scene](c, "/scene")
}

// GetButtons returns all button resources from the bridge.
func (c *Client) GetButtons() ([]Button, error) {
	return get[Button](c, "/button")
}
