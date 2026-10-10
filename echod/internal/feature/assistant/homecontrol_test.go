package assistant

import (
	"strings"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/lib/hass"
)

func house() []hass.Exposed {
	return []hass.Exposed{
		{ID: "light.kitchen_island", Domain: "light", Name: "Kitchen Island", Area: "Kitchen", State: "on", Attrs: map[string]any{"brightness": 128.0}},
		{ID: "light.kitchen_spots", Domain: "light", Name: "Kitchen Spots", Area: "Kitchen", State: "off"},
		{ID: "light.bedroom_lights", Domain: "light", Name: "Bedroom Lights", Area: "Bedroom", State: "off"},
		{ID: "cover.bedroom_blind", Domain: "cover", Name: "Bedroom Blind", Area: "Bedroom", State: "open", Attrs: map[string]any{"current_position": 100.0}},
		{ID: "lock.front_door", Domain: "lock", Name: "Front Door", Area: "Hall", State: "locked"},
		{ID: "climate.nest", Domain: "climate", Name: "Nest", State: "heat", Attrs: map[string]any{"current_temperature": 19.5, "temperature": 21.0}},
		{ID: "media_player.home_theater", Domain: "media_player", Name: "Home Theater", State: "off"},
		{ID: "script.stop_music", Domain: "script", Name: "Stop music", State: "off"},
	}
}

type call struct {
	domain, service string
	data            map[string]any
}

func control(t *testing.T, device, action string, value float64, hasValue bool, all bool) (string, error, []call) {
	t.Helper()
	var calls []call
	out, err := homeControl(house(), device, action, value, hasValue, "", all, func(d, s string, data map[string]any) error {
		calls = append(calls, call{d, s, data})
		return nil
	})
	return out, err, calls
}

func TestStatus(t *testing.T) {
	got := homeStatus(house(), "kitchen island")
	if !strings.Contains(got, "Kitchen Island (light, Kitchen): on (brightness 50 percent)") {
		t.Errorf("got %q", got)
	}
	if got := homeStatus(house(), "kitchen lights"); !strings.Contains(got, "Kitchen Spots") || !strings.Contains(got, "Kitchen Island") {
		t.Errorf("plural and room: %q", got)
	}
	if got := homeStatus(house(), "thermostat banana"); !strings.Contains(got, "no device matches") {
		t.Errorf("got %q", got)
	}
	if got := homeStatus(house(), ""); !strings.Contains(got, "8 devices") {
		t.Errorf("overview: %q", got)
	}
	if got := homeStatus(house(), "nest"); !strings.Contains(got, "now 19.5") || !strings.Contains(got, "set to 21") {
		t.Errorf("climate: %q", got)
	}
}

func TestControlExactNameWins(t *testing.T) {
	_, err, calls := control(t, "kitchen island", "turn_off", 0, false, false)
	if err != nil || len(calls) != 1 || calls[0].data["entity_id"] != "light.kitchen_island" || calls[0].service != "turn_off" {
		t.Errorf("err=%v calls=%v", err, calls)
	}
}

func TestControlAmbiguousAsks(t *testing.T) {
	_, err, calls := control(t, "kitchen lights", "turn_on", 0, false, false)
	if err == nil || !strings.Contains(err.Error(), "several devices match") || len(calls) != 0 {
		t.Errorf("err=%v calls=%v", err, calls)
	}
	out, err, calls := control(t, "kitchen lights", "turn_on", 0, false, true)
	if err != nil || len(calls) != 2 || !strings.HasPrefix(out, "done:") {
		t.Errorf("all: out=%q err=%v calls=%v", out, err, calls)
	}
}

func TestControlValues(t *testing.T) {
	_, err, calls := control(t, "bedroom lights", "set_brightness", 40, true, false)
	if err != nil || calls[0].service != "turn_on" || calls[0].data["brightness_pct"] != 40 {
		t.Errorf("err=%v calls=%v", err, calls)
	}
	if _, err, _ := control(t, "bedroom lights", "set_brightness", 140, true, false); err == nil {
		t.Error("brightness 140 accepted")
	}
	if _, err, _ := control(t, "bedroom lights", "set_brightness", 0, false, false); err == nil {
		t.Error("brightness with no value accepted")
	}
	_, err, calls = control(t, "nest", "set_temperature", 21.5, true, false)
	if err != nil || calls[0].domain != "climate" || calls[0].data["temperature"] != 21.5 {
		t.Errorf("err=%v calls=%v", err, calls)
	}
	if _, err, _ := control(t, "nest", "set_temperature", 80, true, false); err == nil {
		t.Error("80 degrees accepted")
	}
	_, err, calls = control(t, "home theater", "set_volume", 35, true, false)
	if err != nil || calls[0].data["volume_level"] != 0.35 {
		t.Errorf("err=%v calls=%v", err, calls)
	}
	_, err, calls = control(t, "stop music", "turn_on", 0, false, false)
	if err != nil || calls[0].domain != "script" || calls[0].service != "turn_on" {
		t.Errorf("script: err=%v calls=%v", err, calls)
	}
}

func TestLocksCannotBeCommanded(t *testing.T) {
	for _, action := range []string{"unlock", "turn_off", "open", "lock"} {
		_, err, calls := control(t, "front door", action, 0, false, false)
		if err == nil || len(calls) != 0 {
			t.Errorf("%s: err=%v calls=%v", action, err, calls)
		}
	}
}

func TestUnknownDeviceAndAction(t *testing.T) {
	if _, err, _ := control(t, "garage", "open", 0, false, false); err == nil {
		t.Error("unknown device accepted")
	}
	if _, err, _ := control(t, "bedroom blind", "explode", 0, false, false); err == nil {
		t.Error("unknown action accepted")
	}
}
