package collector_test

import (
	"testing"

	"github.com/hypercat-net/hue-exporter/collector"
	"github.com/hypercat-net/hue-exporter/hue"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"strings"
)

// mockBridge implements hue.Bridge for testing.
type mockBridge struct {
	lights             []hue.Light
	groupedLights      []hue.GroupedLight
	rooms              []hue.Room
	zones              []hue.Zone
	motion             []hue.Motion
	temperature        []hue.Temperature
	lightLevel         []hue.LightLevel
	devicePower        []hue.DevicePower
	zigbeeConnectivity []hue.ZigbeeConnectivity
	devices            []hue.Device
	scenes             []hue.Scene
	buttons            []hue.Button
}

func (m *mockBridge) GetLights() ([]hue.Light, error)               { return m.lights, nil }
func (m *mockBridge) GetGroupedLights() ([]hue.GroupedLight, error) { return m.groupedLights, nil }
func (m *mockBridge) GetRooms() ([]hue.Room, error)                 { return m.rooms, nil }
func (m *mockBridge) GetZones() ([]hue.Zone, error)                 { return m.zones, nil }
func (m *mockBridge) GetMotion() ([]hue.Motion, error)              { return m.motion, nil }
func (m *mockBridge) GetTemperature() ([]hue.Temperature, error)    { return m.temperature, nil }
func (m *mockBridge) GetLightLevel() ([]hue.LightLevel, error)      { return m.lightLevel, nil }
func (m *mockBridge) GetDevicePower() ([]hue.DevicePower, error)    { return m.devicePower, nil }
func (m *mockBridge) GetZigbeeConnectivity() ([]hue.ZigbeeConnectivity, error) {
	return m.zigbeeConnectivity, nil
}
func (m *mockBridge) GetDevices() ([]hue.Device, error) { return m.devices, nil }
func (m *mockBridge) GetScenes() ([]hue.Scene, error)   { return m.scenes, nil }
func (m *mockBridge) GetButtons() ([]hue.Button, error) { return m.buttons, nil }

func newTestRegistry(bridge hue.Bridge) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector.New(bridge))
	return reg
}

func TestLightOn(t *testing.T) {
	mirek := 300
	bridge := &mockBridge{
		lights: []hue.Light{
			{
				ID:       "light-1",
				Metadata: hue.Metadata{Name: "Living Room", Archetype: "sultan_bulb"},
				On:       hue.OnState{On: true},
				Dimming:  &hue.Dimming{Brightness: 75.5},
				ColorTemperature: &hue.ColorTemperature{
					Mirek:      &mirek,
					MirekValid: true,
				},
				Color: &hue.Color{XY: hue.XY{X: 0.3, Y: 0.4}},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_light_on Whether the light is on (1) or off (0).
# TYPE hue_light_on gauge
hue_light_on{archetype="sultan_bulb",id="light-1",name="Living Room"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_light_on"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestLightOff(t *testing.T) {
	bridge := &mockBridge{
		lights: []hue.Light{
			{
				ID:       "light-2",
				Metadata: hue.Metadata{Name: "Bedroom", Archetype: "classic_bulb"},
				On:       hue.OnState{On: false},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_light_on Whether the light is on (1) or off (0).
# TYPE hue_light_on gauge
hue_light_on{archetype="classic_bulb",id="light-2",name="Bedroom"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_light_on"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestLightBrightness(t *testing.T) {
	bridge := &mockBridge{
		lights: []hue.Light{
			{
				ID:       "light-3",
				Metadata: hue.Metadata{Name: "Kitchen", Archetype: "pendant_round"},
				On:       hue.OnState{On: true},
				Dimming:  &hue.Dimming{Brightness: 50.0},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_light_brightness_percent Current brightness of the light as a percentage (0–100).
# TYPE hue_light_brightness_percent gauge
hue_light_brightness_percent{archetype="pendant_round",id="light-3",name="Kitchen"} 50
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_light_brightness_percent"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestLightColorTemperature(t *testing.T) {
	mirek := 400
	bridge := &mockBridge{
		lights: []hue.Light{
			{
				ID:       "light-4",
				Metadata: hue.Metadata{Name: "Office", Archetype: "flexible_lamp"},
				On:       hue.OnState{On: true},
				ColorTemperature: &hue.ColorTemperature{
					Mirek:      &mirek,
					MirekValid: true,
				},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_light_color_temperature_mirek Current color temperature of the light in mirek (153–500).
# TYPE hue_light_color_temperature_mirek gauge
hue_light_color_temperature_mirek{archetype="flexible_lamp",id="light-4",name="Office"} 400
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_light_color_temperature_mirek"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestLightColorTemperatureInvalidSkipped(t *testing.T) {
	mirek := 400
	bridge := &mockBridge{
		lights: []hue.Light{
			{
				ID:       "light-5",
				Metadata: hue.Metadata{Name: "Lamp", Archetype: "floor_shade"},
				On:       hue.OnState{On: true},
				ColorTemperature: &hue.ColorTemperature{
					Mirek:      &mirek,
					MirekValid: false, // invalid — should be skipped
				},
			},
		},
	}

	reg := newTestRegistry(bridge)

	// Metric should not be emitted when mirek_valid is false.
	expected := ``
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_light_color_temperature_mirek"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestGroupedLight(t *testing.T) {
	bridge := &mockBridge{
		groupedLights: []hue.GroupedLight{
			{
				ID:      "group-1",
				On:      hue.OnState{On: true},
				Dimming: &hue.Dimming{Brightness: 80.0},
				Owner:   hue.ResourceRef{RID: "room-1", RType: "room"},
			},
		},
		rooms: []hue.Room{
			{
				ID:       "room-1",
				Metadata: hue.Metadata{Name: "Living Room"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_grouped_light_on Whether any light in the group is on (1) or all are off (0).
# TYPE hue_grouped_light_on gauge
hue_grouped_light_on{id="group-1",owner_id="room-1",owner_name="Living Room",owner_type="room"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_grouped_light_on"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestMotionDetected(t *testing.T) {
	bridge := &mockBridge{
		motion: []hue.Motion{
			{
				ID:      "motion-1",
				Enabled: true,
				Motion:  hue.MotionSensor{Motion: true},
				Owner:   hue.ResourceRef{RID: "device-1", RType: "device"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_motion_detected Whether motion is currently detected (1) or not (0).
# TYPE hue_motion_detected gauge
hue_motion_detected{id="motion-1",owner_id="device-1"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_motion_detected"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestMotionReport(t *testing.T) {
	bridge := &mockBridge{
		motion: []hue.Motion{
			{
				ID:      "motion-2",
				Enabled: true,
				Motion: hue.MotionSensor{
					Motion: false,
					MotionReport: &hue.MotionReport{
						Changed: "2024-01-01T00:00:00Z",
						Motion:  true, // report takes precedence
					},
				},
				Owner: hue.ResourceRef{RID: "device-2", RType: "device"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_motion_detected Whether motion is currently detected (1) or not (0).
# TYPE hue_motion_detected gauge
hue_motion_detected{id="motion-2",owner_id="device-2"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_motion_detected"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestTemperature(t *testing.T) {
	bridge := &mockBridge{
		temperature: []hue.Temperature{
			{
				ID:      "temp-1",
				Enabled: true,
				Temperature: hue.TemperatureSensor{
					Temperature:      21.5,
					TemperatureValid: true,
				},
				Owner: hue.ResourceRef{RID: "device-3", RType: "device"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_temperature_celsius Current temperature reading in degrees Celsius.
# TYPE hue_temperature_celsius gauge
hue_temperature_celsius{id="temp-1",owner_id="device-3"} 21.5
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_temperature_celsius"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestTemperatureInvalidSkipped(t *testing.T) {
	bridge := &mockBridge{
		temperature: []hue.Temperature{
			{
				ID:      "temp-2",
				Enabled: true,
				Temperature: hue.TemperatureSensor{
					Temperature:      0,
					TemperatureValid: false,
				},
				Owner: hue.ResourceRef{RID: "device-4", RType: "device"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := ``
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_temperature_celsius"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestLightLevel(t *testing.T) {
	// light_level = 10000 * log10(lux) + 1 => lux = 10^((level-1)/10000)
	// For level = 20001: lux = 10^(20000/10000) = 10^2 = 100
	bridge := &mockBridge{
		lightLevel: []hue.LightLevel{
			{
				ID:      "ll-1",
				Enabled: true,
				Light: hue.LightLevelSensor{
					LightLevel:      20001,
					LightLevelValid: true,
				},
				Owner: hue.ResourceRef{RID: "device-5", RType: "device"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_light_level_lux Current ambient light level in lux.
# TYPE hue_light_level_lux gauge
hue_light_level_lux{id="ll-1",owner_id="device-5"} 100
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_light_level_lux"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestDeviceBattery(t *testing.T) {
	level := 85
	bridge := &mockBridge{
		devicePower: []hue.DevicePower{
			{
				ID: "dp-1",
				PowerState: hue.PowerState{
					BatteryState: "normal",
					BatteryLevel: &level,
				},
				Owner: hue.ResourceRef{RID: "device-6", RType: "device"},
			},
		},
		devices: []hue.Device{
			{
				ID:       "device-6",
				Metadata: hue.Metadata{Name: "Hall Sensor"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_device_battery_level_percent Battery level of the device as a percentage (0–100).
# TYPE hue_device_battery_level_percent gauge
hue_device_battery_level_percent{id="dp-1",owner_id="device-6",owner_name="Hall Sensor"} 85
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_device_battery_level_percent"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestDeviceBatteryNilSkipped(t *testing.T) {
	bridge := &mockBridge{
		devicePower: []hue.DevicePower{
			{
				ID: "dp-2",
				PowerState: hue.PowerState{
					BatteryState: "normal",
					BatteryLevel: nil, // mains-powered device, no battery
				},
				Owner: hue.ResourceRef{RID: "device-7", RType: "device"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := ``
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_device_battery_level_percent"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestZigbeeConnected(t *testing.T) {
	bridge := &mockBridge{
		zigbeeConnectivity: []hue.ZigbeeConnectivity{
			{
				ID:     "zb-1",
				Status: "connected",
				Owner:  hue.ResourceRef{RID: "device-8", RType: "device"},
			},
			{
				ID:     "zb-2",
				Status: "disconnected",
				Owner:  hue.ResourceRef{RID: "device-9", RType: "device"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_zigbee_connected Whether the Zigbee device is connected (1) or not (0).
# TYPE hue_zigbee_connected gauge
hue_zigbee_connected{id="zb-1",owner_id="device-8"} 1
hue_zigbee_connected{id="zb-2",owner_id="device-9"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_zigbee_connected"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestSceneActive(t *testing.T) {
	bridge := &mockBridge{
		scenes: []hue.Scene{
			{
				ID:       "scene-1",
				Metadata: hue.Metadata{Name: "Relax"},
				Group:    hue.ResourceRef{RID: "room-1", RType: "room"},
				Status:   hue.SceneStatus{Active: "static"},
			},
			{
				ID:       "scene-2",
				Metadata: hue.Metadata{Name: "Concentrate"},
				Group:    hue.ResourceRef{RID: "room-1", RType: "room"},
				Status:   hue.SceneStatus{Active: "inactive"},
			},
		},
	}

	reg := newTestRegistry(bridge)

	expected := `
# HELP hue_scene_active Whether the scene is currently active (1) or not (0).
# TYPE hue_scene_active gauge
hue_scene_active{group_id="room-1",group_type="room",id="scene-1",name="Relax"} 1
hue_scene_active{group_id="room-1",group_type="room",id="scene-2",name="Concentrate"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "hue_scene_active"); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}
