package assistant

import (
	"strings"
	"testing"
)

func TestForHouseLeavesMusicToTheScripts(t *testing.T) {
	ts := forHouse(tools())
	var stop *tool
	for i, x := range ts {
		if x.Name == "play_music" {
			t.Error("the device's own play_music is still offered")
		}
		if x.Name == "stop" {
			stop = &ts[i]
		}
	}
	if stop == nil {
		t.Fatal("the device's stop is gone: a ringing alarm could not be silenced")
	}
	if strings.Contains(stop.Description, "radio or music") || !strings.Contains(stop.Description, "Stop music script") {
		t.Errorf("stop still claims to stop music: %q", stop.Description)
	}
	// Everything else is kept.
	if len(ts) < len(tools())-1 {
		t.Errorf("%d tools left of %d", len(ts), len(tools()))
	}
}
