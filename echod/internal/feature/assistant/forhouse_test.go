package assistant

import "testing"

func TestForHouseLeavesMusicToTheHouse(t *testing.T) {
	ts := forHouse(tools())
	for _, x := range ts {
		if x.Name == "play_music" || x.Name == "stop" {
			t.Errorf("the device's own %s is still offered", x.Name)
		}
	}
	if len(ts) < len(tools())-2 {
		t.Errorf("%d tools left of %d: more than the music ones went", len(ts), len(tools()))
	}
}
