package voice

import (
	"math"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
)

// tone is a frame of a sine at the given RMS.
func tone(level float64) []int16 {
	f := make([]int16, mic.FrameSamples)
	for i := range f {
		f[i] = int16(level * math.Sqrt2 * math.Sin(float64(i)*0.3))
	}
	return f
}

func sent(out [][]int16) (frames, loud int) {
	for _, f := range out {
		frames++
		if rms(f) > 0 {
			loud++
		}
	}
	return
}

// The device's own voice, however loud, is never passed on.
func TestGateHoldsBackEcho(t *testing.T) {
	var g echoGate
	for i := 0; i < 400; i++ {
		// Echo that swells and fades with the sentence.
		level := 300 + 200*math.Sin(float64(i)/20)
		if _, loud := sent(g.Feed(tone(level))); loud != 0 {
			t.Fatalf("echo got through at frame %d", i)
		}
	}
}

// Speech over the echo opens it, with the moments before, and a quiet spell closes it.
func TestGateOpensForSpeech(t *testing.T) {
	var g echoGate
	for i := 0; i < 60; i++ {
		g.Feed(tone(400)) // echo alone
	}
	var got int
	opened := -1
	for i := 0; i < 20; i++ {
		frames, loud := sent(g.Feed(tone(2500)))
		got += loud
		if loud > 0 && opened < 0 {
			opened = i
			if frames <= 1 {
				t.Errorf("no moments from before it opened: %d frames", frames)
			}
		}
	}
	if opened < 0 || opened > gateOpenAfter+1 {
		t.Fatalf("opened at %d", opened)
	}
	if got < 10 {
		t.Errorf("only %d frames of speech passed", got)
	}
	// Once open it passes everything, then closes after a quiet spell.
	for i := 0; i < gateCloseAt+2; i++ {
		g.Feed(tone(400))
	}
	if _, loud := sent(g.Feed(tone(400))); loud != 0 {
		t.Error("still open after a quiet spell")
	}
}

// A short click in the echo does not open it.
func TestGateIgnoresAClick(t *testing.T) {
	var g echoGate
	for i := 0; i < 60; i++ {
		g.Feed(tone(400))
	}
	for i := 0; i < 3; i++ {
		if _, loud := sent(g.Feed(tone(5000))); loud != 0 {
			t.Fatal("a click opened the gate")
		}
	}
	for i := 0; i < 30; i++ {
		if _, loud := sent(g.Feed(tone(400))); loud != 0 {
			t.Fatal("the gate opened late for a click")
		}
	}
}

// Nothing is passed in the first stretch of a reply, which is where the echo level is learned, however loud.
func TestGateLearnsFirst(t *testing.T) {
	var g echoGate
	for i := 0; i < gateLearn; i++ {
		if _, loud := sent(g.Feed(tone(8000))); loud != 0 {
			t.Fatal("passed audio while learning")
		}
	}
}
