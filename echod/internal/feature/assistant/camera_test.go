package assistant

import (
	"strings"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

var testCameras = []config.Camera{
	{Entity: "local", Name: "Show camera"},
	{Entity: "camera.front_door", Name: "Front Door"},
	{Entity: "camera.alley", Name: "Alley"},
	{Entity: "camera.back_garden", Name: "Back garden"},
	{Entity: "camera.side_garden", Name: "Side garden"},
}

func TestAWordOrTwoPicksTheCamera(t *testing.T) {
	for query, want := range map[string]string{
		"alley":                 "camera.alley",
		"the alley camera":      "camera.alley",
		"Show the alley camera": "camera.alley",
		"front door":            "camera.front_door",
		"my front door cam":     "camera.front_door",
		"back garden":           "camera.back_garden",
		"side":                  "camera.side_garden",
		"show":                  "local", // "show" is a word of the request, but "Show camera" is a name: see below
	} {
		c, ok := pickCamera(query, testCameras)
		if query == "show" {
			if ok {
				t.Errorf("%q alone names nothing, got %s", query, c.Entity)
			}
			continue
		}
		if !ok || c.Entity != want {
			t.Errorf("%q picked %q (%v), want %s", query, c.Entity, ok, want)
		}
	}
}

func TestDoubtfulNamesAreNotGuessed(t *testing.T) {
	for _, q := range []string{"garden", "", "the camera", "kitchen", "front garden"} {
		if c, ok := pickCamera(q, testCameras); ok {
			t.Errorf("%q picked %s; it should have been left to ask", q, c.Entity)
		}
	}
	// A name that is exactly one camera wins over longer names that also hold its words.
	cams := []config.Camera{{Entity: "camera.garden", Name: "Garden"}, {Entity: "camera.garden_shed", Name: "Garden shed"}}
	if c, ok := pickCamera("garden", cams); !ok || c.Entity != "camera.garden" {
		t.Errorf("garden picked %v %v", c, ok)
	}
}

func TestShowingACamera(t *testing.T) {
	var shown []string
	show := func(e string) { shown = append(shown, e) }

	name, err := showCameraNamed("the alley camera", testCameras, show)
	if err != nil || name != "Alley" || len(shown) != 1 || shown[0] != "camera.alley" {
		t.Fatalf("showed %v: %q, %v", shown, name, err)
	}
	// An unknown one shows nothing and lists the cameras there are.
	_, err = showCameraNamed("kitchen", testCameras, show)
	if err == nil || len(shown) != 1 {
		t.Fatalf("kitchen: %v, shown %v", err, shown)
	}
	for _, c := range testCameras {
		if !strings.Contains(err.Error(), c.Name) {
			t.Errorf("the answer does not list %s: %v", c.Name, err)
		}
	}
	// No cameras at all says so.
	if _, err := showCameraNamed("alley", nil, show); err == nil || !strings.Contains(err.Error(), "no cameras") {
		t.Errorf("no cameras: %v", err)
	}
}

// A device with no screen is not offered the tool, and one with a screen is.
func TestTheCameraToolIsOnlyForDevicesWithAScreen(t *testing.T) {
	was := showCamera
	defer func() { showCamera = was }()
	showCamera = nil
	if len(cameraTools()) != 0 {
		t.Error("a device with no screen was offered the camera tool")
	}
	SetCameraShower(func(string) {})
	ts := cameraTools()
	if len(ts) != 1 || ts[0].Name != "show_camera" {
		t.Fatalf("tools %v", ts)
	}
	found := false
	for _, tl := range tools() {
		found = found || tl.Name == "show_camera"
	}
	if !found {
		t.Error("show_camera is not among the device's tools")
	}
	// And a house with Home Assistant keeps it.
	for _, tl := range forHouse(tools()) {
		if tl.Name == "show_camera" {
			return
		}
	}
	t.Error("forHouse withdrew the camera tool")
}

// The cameras of a real house: two of them begin with the same word.
func TestARealHousesCameras(t *testing.T) {
	cams := []config.Camera{
		{Entity: "camera.alley_camera_stream", Name: "Alley Camera Stream"},
		{Entity: "camera.front_camera_live", Name: "Front Camera Live"},
		{Entity: "camera.garden_camera_live", Name: "Garden Camera Live"},
		{Entity: "camera.alley_camera", Name: "Alley Camera"},
		{Entity: "camera.show_camera", Name: "Show Camera"},
		{Entity: "camera.galaxy_tab_a7_lite", Name: "Living Room Tablet (Fully Kiosk)"},
	}
	for query, want := range map[string]string{
		"the alley camera":    "camera.alley_camera",
		"alley":               "camera.alley_camera",
		"alley camera stream": "camera.alley_camera_stream",
		"the alley stream":    "camera.alley_camera_stream",
		"front camera":        "camera.front_camera_live",
		"front":               "camera.front_camera_live",
		"garden":              "camera.garden_camera_live",
		"living room tablet":  "camera.galaxy_tab_a7_lite",
	} {
		c, ok := pickCamera(query, cams)
		if !ok || c.Entity != want {
			t.Errorf("%q picked %q (%v), want %s", query, c.Entity, ok, want)
		}
	}
	if c, ok := pickCamera("camera", cams); ok {
		t.Errorf("a bare 'camera' picked %s", c.Entity)
	}
}
