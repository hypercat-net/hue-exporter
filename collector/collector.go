// Package collector implements Prometheus collectors for Hue v2 resources.
package collector

import (
	"math"
	"sync"

	"github.com/hypercat-net/hue-exporter/hue"
	"github.com/prometheus/client_golang/prometheus"
)

const namespace = "hue"

// HueCollector collects metrics from a Hue bridge via the CLIP v2 API.
type HueCollector struct {
	bridge hue.Bridge
	mu     sync.Mutex

	groupOwnerNames  map[string]string
	seenGroupOwners  map[string]struct{}
	deviceOwnerNames map[string]string
	seenDeviceOwners map[string]struct{}

	// lights
	lightOn           *prometheus.GaugeVec
	lightBrightness   *prometheus.GaugeVec
	lightColorTemp    *prometheus.GaugeVec
	lightColorX       *prometheus.GaugeVec
	lightColorY       *prometheus.GaugeVec
	lightScrapesTotal prometheus.Counter

	// grouped lights
	groupedLightOn           *prometheus.GaugeVec
	groupedLightBrightness   *prometheus.GaugeVec
	groupedLightScrapesTotal prometheus.Counter

	// motion sensors
	motionDetected     *prometheus.GaugeVec
	motionEnabled      *prometheus.GaugeVec
	motionScrapesTotal prometheus.Counter

	// temperature sensors
	temperatureCelsius      *prometheus.GaugeVec
	temperatureScrapesTotal prometheus.Counter

	// light-level sensors
	lightLevelLux          *prometheus.GaugeVec
	lightLevelScrapesTotal prometheus.Counter

	// device power
	deviceBatteryLevel *prometheus.GaugeVec
	deviceScrapesTotal prometheus.Counter

	// Zigbee connectivity
	zigbeeConnected    *prometheus.GaugeVec
	zigbeeScrapesTotal prometheus.Counter

	// scenes
	sceneActive       *prometheus.GaugeVec
	sceneScrapesTotal prometheus.Counter
}

// New creates a new HueCollector.
func New(bridge hue.Bridge) *HueCollector {
	lightLabels := []string{"name"}
	groupLabels := []string{"group_type", "name"}
	deviceLabels := []string{"name"}
	sceneLabels := []string{"name", "group_name", "group_type"}

	return &HueCollector{
		bridge:           bridge,
		groupOwnerNames:  map[string]string{},
		seenGroupOwners:  map[string]struct{}{},
		deviceOwnerNames: map[string]string{},
		seenDeviceOwners: map[string]struct{}{},

		lightOn: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "light",
			Name:      "on",
			Help:      "Whether the light is on (1) or off (0).",
		}, lightLabels),
		lightBrightness: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "light",
			Name:      "brightness_percent",
			Help:      "Current brightness of the light as a percentage (0–100).",
		}, lightLabels),
		lightColorTemp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "light",
			Name:      "color_temperature_mirek",
			Help:      "Current color temperature of the light in mirek (153–500).",
		}, lightLabels),
		lightColorX: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "light",
			Name:      "color_x",
			Help:      "CIE 1931 xy color coordinate X of the light.",
		}, lightLabels),
		lightColorY: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "light",
			Name:      "color_y",
			Help:      "CIE 1931 xy color coordinate Y of the light.",
		}, lightLabels),
		lightScrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "light",
			Name:      "scrapes_failed_total",
			Help:      "Total number of failed light scrapes.",
		}),

		groupedLightOn: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "grouped_light",
			Name:      "on",
			Help:      "Whether any light in the group is on (1) or all are off (0).",
		}, groupLabels),
		groupedLightBrightness: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "grouped_light",
			Name:      "brightness_percent",
			Help:      "Current brightness of the grouped light as a percentage (0–100).",
		}, groupLabels),
		groupedLightScrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "grouped_light",
			Name:      "scrapes_failed_total",
			Help:      "Total number of failed grouped-light scrapes.",
		}),

		motionDetected: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "motion",
			Name:      "detected",
			Help:      "Whether motion is currently detected (1) or not (0).",
		}, deviceLabels),
		motionEnabled: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "motion",
			Name:      "enabled",
			Help:      "Whether the motion sensor is enabled (1) or disabled (0).",
		}, deviceLabels),
		motionScrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "motion",
			Name:      "scrapes_failed_total",
			Help:      "Total number of failed motion-sensor scrapes.",
		}),

		temperatureCelsius: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "temperature",
			Name:      "celsius",
			Help:      "Current temperature reading in degrees Celsius.",
		}, deviceLabels),
		temperatureScrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "temperature",
			Name:      "scrapes_failed_total",
			Help:      "Total number of failed temperature-sensor scrapes.",
		}),

		lightLevelLux: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "light_level",
			Name:      "lux",
			Help:      "Current ambient light level in lux.",
		}, deviceLabels),
		lightLevelScrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "light_level",
			Name:      "scrapes_failed_total",
			Help:      "Total number of failed light-level sensor scrapes.",
		}),

		deviceBatteryLevel: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "device",
			Name:      "battery_level_percent",
			Help:      "Battery level of the device as a percentage (0–100).",
		}, deviceLabels),
		deviceScrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "device",
			Name:      "scrapes_failed_total",
			Help:      "Total number of failed device-power scrapes.",
		}),

		zigbeeConnected: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "zigbee",
			Name:      "connected",
			Help:      "Whether the Zigbee device is connected (1) or not (0).",
		}, deviceLabels),
		zigbeeScrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "zigbee",
			Name:      "scrapes_failed_total",
			Help:      "Total number of failed Zigbee connectivity scrapes.",
		}),

		sceneActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "scene",
			Name:      "active",
			Help:      "Whether the scene is currently active (1) or not (0).",
		}, sceneLabels),
		sceneScrapesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "scene",
			Name:      "scrapes_failed_total",
			Help:      "Total number of failed scene scrapes.",
		}),
	}
}

// Describe sends the descriptors of each metric to the channel.
func (c *HueCollector) Describe(ch chan<- *prometheus.Desc) {
	c.lightOn.Describe(ch)
	c.lightBrightness.Describe(ch)
	c.lightColorTemp.Describe(ch)
	c.lightColorX.Describe(ch)
	c.lightColorY.Describe(ch)
	c.lightScrapesTotal.Describe(ch)

	c.groupedLightOn.Describe(ch)
	c.groupedLightBrightness.Describe(ch)
	c.groupedLightScrapesTotal.Describe(ch)

	c.motionDetected.Describe(ch)
	c.motionEnabled.Describe(ch)
	c.motionScrapesTotal.Describe(ch)

	c.temperatureCelsius.Describe(ch)
	c.temperatureScrapesTotal.Describe(ch)

	c.lightLevelLux.Describe(ch)
	c.lightLevelScrapesTotal.Describe(ch)

	c.deviceBatteryLevel.Describe(ch)
	c.deviceScrapesTotal.Describe(ch)

	c.zigbeeConnected.Describe(ch)
	c.zigbeeScrapesTotal.Describe(ch)

	c.sceneActive.Describe(ch)
	c.sceneScrapesTotal.Describe(ch)
}

// Collect fetches metrics from the Hue bridge and sends them to the channel.
func (c *HueCollector) Collect(ch chan<- prometheus.Metric) {
	// Serialize concurrent scrapes so that Reset-populate-Collect is atomic.
	c.mu.Lock()
	defer c.mu.Unlock()

	// Reset all GaugeVecs so removed resources don't linger.
	c.lightOn.Reset()
	c.lightBrightness.Reset()
	c.lightColorTemp.Reset()
	c.lightColorX.Reset()
	c.lightColorY.Reset()

	c.groupedLightOn.Reset()
	c.groupedLightBrightness.Reset()

	c.motionDetected.Reset()
	c.motionEnabled.Reset()

	c.temperatureCelsius.Reset()

	c.lightLevelLux.Reset()

	c.deviceBatteryLevel.Reset()

	c.zigbeeConnected.Reset()

	c.sceneActive.Reset()

	c.collectLights()
	c.collectGroupedLights()
	c.collectMotion()
	c.collectTemperature()
	c.collectLightLevel()
	c.collectDevicePower()
	c.collectZigbee()
	c.collectScenes()

	// Collect all metrics.
	c.lightOn.Collect(ch)
	c.lightBrightness.Collect(ch)
	c.lightColorTemp.Collect(ch)
	c.lightColorX.Collect(ch)
	c.lightColorY.Collect(ch)
	c.lightScrapesTotal.Collect(ch)

	c.groupedLightOn.Collect(ch)
	c.groupedLightBrightness.Collect(ch)
	c.groupedLightScrapesTotal.Collect(ch)

	c.motionDetected.Collect(ch)
	c.motionEnabled.Collect(ch)
	c.motionScrapesTotal.Collect(ch)

	c.temperatureCelsius.Collect(ch)
	c.temperatureScrapesTotal.Collect(ch)

	c.lightLevelLux.Collect(ch)
	c.lightLevelScrapesTotal.Collect(ch)

	c.deviceBatteryLevel.Collect(ch)
	c.deviceScrapesTotal.Collect(ch)

	c.zigbeeConnected.Collect(ch)
	c.zigbeeScrapesTotal.Collect(ch)

	c.sceneActive.Collect(ch)
	c.sceneScrapesTotal.Collect(ch)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// lightLevelToLux converts the Hue light-level encoding to lux.
// The bridge encodes lux as: light_level = 10000 * log10(lux) + 1
func lightLevelToLux(lightLevel int) float64 {
	return math.Pow(10, float64(lightLevel-1)/10000)
}

func resourceKey(rtype, rid string) string {
	return rtype + ":" + rid
}

func isIgnoredGroupType(rtype string) bool {
	return rtype == "private_group"
}

func (c *HueCollector) fetchGroupedLightOwnerNames() map[string]string {
	names := map[string]string{}

	if rooms, err := c.bridge.GetRooms(); err == nil {
		for _, room := range rooms {
			names[resourceKey("room", room.ID)] = room.Metadata.Name
		}
	}

	if zones, err := c.bridge.GetZones(); err == nil {
		for _, zone := range zones {
			names[resourceKey("zone", zone.ID)] = zone.Metadata.Name
		}
	}

	return names
}

func (c *HueCollector) groupedLightOwnerName(ref hue.ResourceRef) string {
	key := resourceKey(ref.RType, ref.RID)
	if name, ok := c.groupOwnerNames[key]; ok {
		return name
	}
	if _, seen := c.seenGroupOwners[key]; seen {
		return ""
	}

	for key, name := range c.fetchGroupedLightOwnerNames() {
		c.groupOwnerNames[key] = name
	}
	c.seenGroupOwners[key] = struct{}{}
	return c.groupOwnerNames[key]
}

func (c *HueCollector) fetchDeviceOwnerNames() map[string]string {
	devices, err := c.bridge.GetDevices()
	if err != nil {
		return map[string]string{}
	}

	names := make(map[string]string, len(devices))
	for _, device := range devices {
		names[resourceKey("device", device.ID)] = device.Metadata.Name
	}

	return names
}

func (c *HueCollector) deviceOwnerName(ref hue.ResourceRef) string {
	key := resourceKey(ref.RType, ref.RID)
	if name, ok := c.deviceOwnerNames[key]; ok {
		return name
	}
	if _, seen := c.seenDeviceOwners[key]; seen {
		return ""
	}

	for key, name := range c.fetchDeviceOwnerNames() {
		c.deviceOwnerNames[key] = name
	}
	c.seenDeviceOwners[key] = struct{}{}
	return c.deviceOwnerNames[key]
}

func (c *HueCollector) collectLights() {
	lights, err := c.bridge.GetLights()
	if err != nil {
		c.lightScrapesTotal.Add(1)
		return
	}
	for _, l := range lights {
		labels := prometheus.Labels{
			"name": l.Metadata.Name,
		}
		c.lightOn.With(labels).Set(boolToFloat(l.On.On))
		if l.Dimming != nil {
			c.lightBrightness.With(labels).Set(l.Dimming.Brightness)
		}
		if l.ColorTemperature != nil && l.ColorTemperature.MirekValid && l.ColorTemperature.Mirek != nil {
			c.lightColorTemp.With(labels).Set(float64(*l.ColorTemperature.Mirek))
		}
		if l.Color != nil {
			c.lightColorX.With(labels).Set(l.Color.XY.X)
			c.lightColorY.With(labels).Set(l.Color.XY.Y)
		}
	}
}

func (c *HueCollector) collectGroupedLights() {
	groups, err := c.bridge.GetGroupedLights()
	if err != nil {
		c.groupedLightScrapesTotal.Add(1)
		return
	}
	for _, g := range groups {
		if isIgnoredGroupType(g.Owner.RType) {
			continue
		}
		groupName := c.groupedLightOwnerName(g.Owner)
		if groupName == "" {
			continue
		}
		labels := prometheus.Labels{
			"group_type": g.Owner.RType,
			"name":       groupName,
		}
		c.groupedLightOn.With(labels).Set(boolToFloat(g.On.On))
		if g.Dimming != nil {
			c.groupedLightBrightness.With(labels).Set(g.Dimming.Brightness)
		}
	}
}

func (c *HueCollector) collectMotion() {
	sensors, err := c.bridge.GetMotion()
	if err != nil {
		c.motionScrapesTotal.Add(1)
		return
	}
	for _, s := range sensors {
		deviceName := c.deviceOwnerName(s.Owner)
		if deviceName == "" {
			continue
		}
		labels := prometheus.Labels{
			"name": deviceName,
		}
		motion := s.Motion.Motion
		if s.Motion.MotionReport != nil {
			motion = s.Motion.MotionReport.Motion
		}
		c.motionDetected.With(labels).Set(boolToFloat(motion))
		c.motionEnabled.With(labels).Set(boolToFloat(s.Enabled))
	}
}

func (c *HueCollector) collectTemperature() {
	sensors, err := c.bridge.GetTemperature()
	if err != nil {
		c.temperatureScrapesTotal.Add(1)
		return
	}
	for _, s := range sensors {
		if !s.Temperature.TemperatureValid {
			continue
		}
		deviceName := c.deviceOwnerName(s.Owner)
		if deviceName == "" {
			continue
		}
		labels := prometheus.Labels{
			"name": deviceName,
		}
		temp := s.Temperature.Temperature
		if s.Temperature.TemperatureReport != nil {
			temp = s.Temperature.TemperatureReport.Temperature
		}
		c.temperatureCelsius.With(labels).Set(temp)
	}
}

func (c *HueCollector) collectLightLevel() {
	sensors, err := c.bridge.GetLightLevel()
	if err != nil {
		c.lightLevelScrapesTotal.Add(1)
		return
	}
	for _, s := range sensors {
		if !s.Light.LightLevelValid {
			continue
		}
		deviceName := c.deviceOwnerName(s.Owner)
		if deviceName == "" {
			continue
		}
		labels := prometheus.Labels{
			"name": deviceName,
		}
		level := s.Light.LightLevel
		if s.Light.LightLevelReport != nil {
			level = s.Light.LightLevelReport.LightLevel
		}
		c.lightLevelLux.With(labels).Set(lightLevelToLux(level))
	}
}

func (c *HueCollector) collectDevicePower() {
	devices, err := c.bridge.GetDevicePower()
	if err != nil {
		c.deviceScrapesTotal.Add(1)
		return
	}
	for _, d := range devices {
		if d.PowerState.BatteryLevel == nil {
			continue
		}
		deviceName := c.deviceOwnerName(d.Owner)
		if deviceName == "" {
			continue
		}
		labels := prometheus.Labels{
			"name": deviceName,
		}
		c.deviceBatteryLevel.With(labels).Set(float64(*d.PowerState.BatteryLevel))
	}
}

func (c *HueCollector) collectZigbee() {
	devices, err := c.bridge.GetZigbeeConnectivity()
	if err != nil {
		c.zigbeeScrapesTotal.Add(1)
		return
	}
	for _, d := range devices {
		deviceName := c.deviceOwnerName(d.Owner)
		if deviceName == "" {
			continue
		}
		labels := prometheus.Labels{
			"name": deviceName,
		}
		connected := d.Status == "connected"
		c.zigbeeConnected.With(labels).Set(boolToFloat(connected))
	}
}

func (c *HueCollector) collectScenes() {
	scenes, err := c.bridge.GetScenes()
	if err != nil {
		c.sceneScrapesTotal.Add(1)
		return
	}
	for _, s := range scenes {
		if isIgnoredGroupType(s.Group.RType) {
			continue
		}
		groupName := c.groupedLightOwnerName(s.Group)
		if groupName == "" {
			continue
		}
		labels := prometheus.Labels{
			"name":       s.Metadata.Name,
			"group_name": groupName,
			"group_type": s.Group.RType,
		}
		active := s.Status.Active != "inactive" && s.Status.Active != ""
		c.sceneActive.With(labels).Set(boolToFloat(active))
	}
}
