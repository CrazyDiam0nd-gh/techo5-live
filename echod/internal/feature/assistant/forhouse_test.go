package assistant

import "testing"

func TestForHouseLeavesMusicToTheHouse(t *testing.T) {
	ts := forHouse(tools())
	for _, x := range ts {
		if x.Name == "play_music" || x.Name == "stop" || x.Name == "weather" || x.Name == "set_volume" {
			t.Errorf("the device's own %s is still offered", x.Name)
		}
	}
	if len(ts) < len(tools())-4 {
		t.Errorf("%d tools left of %d: more than the music ones went", len(ts), len(tools()))
	}
}
