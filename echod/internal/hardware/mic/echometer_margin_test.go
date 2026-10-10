package mic

import "testing"

func steadyFrame(v int16) []int16 {
	f := make([]int16, 320)
	for i := range f {
		f[i] = v
	}
	return f
}

// A voice only a little over the echo is speech with a small margin and echo with a large one, which is the whole of
// what the setting changes.
func TestTheMarginDecidesHowFarOverTheEchoSpeechHasToBe(t *testing.T) {
	t.Cleanup(DefaultEchoMargin)
	judge := func(margin float64, over int16) bool {
		SetEchoMargin(margin)
		m := &echoMeter{}
		for i := 0; i < meterLearn+2; i++ {
			m.feed(steadyFrame(1000), steadyFrame(100)) // echo alone: a tenth of what plays
		}
		return m.feed(steadyFrame(1000), steadyFrame(over))
	}
	if judge(2.8, 250) {
		t.Error("9 dB margin: 2.5 times the echo was taken for speech")
	}
	if !judge(1.8, 250) {
		t.Error("5 dB margin: 2.5 times the echo was not taken for speech")
	}
	if !judge(2.8, 400) {
		t.Error("9 dB margin: 4 times the echo was not speech")
	}
	if judge(1.3, 120) {
		t.Error("2 dB margin: a hair over the echo was taken for speech")
	}
	SetEchoMargin(0.2)
	if EchoMargin() < 1.1 {
		t.Errorf("a margin under 1.1 was accepted: %v", EchoMargin())
	}
	DefaultEchoMargin()
	if EchoMargin() != meterMargin {
		t.Errorf("default %v", EchoMargin())
	}
}
