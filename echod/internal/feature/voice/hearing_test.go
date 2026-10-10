package voice

import "testing"

func TestHearingLevels(t *testing.T) {
	var prev float64 = 100
	for _, m := range []string{hearCareful, hearBalanced, hearSensitive} {
		margin, open := hearing(m)
		if open || margin >= prev {
			t.Errorf("%q: margin %v, open %v after %v: each level should let a quieter voice through", m, margin, open, prev)
		}
		prev = margin
	}
	if _, open := hearing(hearOpen); !open {
		t.Error("open microphone does not bypass the gate")
	}
	if margin, open := hearing("nonsense"); margin != 2.8 || open {
		t.Errorf("an unknown level should be careful, got %v %v", margin, open)
	}
	if margin, _ := hearing(hearCareful); margin != 2.8 {
		t.Errorf("careful changed from the 9 dB the meter was tuned with: %v", margin)
	}
}
