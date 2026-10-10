package voice

import "testing"

func frameOf(v int16) []int16 {
	f := make([]int16, 320)
	for i := range f {
		f[i] = v
	}
	return f
}

func sent(out [][]int16) (frames, loud int) {
	for _, f := range out {
		frames++
		if f[0] != 0 {
			loud++
		}
	}
	return
}

// Echo is never passed on.
func TestGateHoldsBackEcho(t *testing.T) {
	var g echoGate
	for i := 0; i < 400; i++ {
		if _, loud := sent(g.Feed(frameOf(900), false, 0)); loud != 0 {
			t.Fatalf("echo got through at frame %d", i)
		}
	}
}

// Speech over the echo opens it, with the moments before, and a quiet spell closes it.
func TestGateOpensForSpeech(t *testing.T) {
	var g echoGate
	for i := 0; i < 60; i++ {
		g.Feed(frameOf(900), false, 0)
	}
	opened, passed := -1, 0
	for i := 0; i < 20; i++ {
		frames, loud := sent(g.Feed(frameOf(2500), true, 0))
		passed += loud
		if loud > 0 && opened < 0 {
			opened = i
			if frames <= 1 {
				t.Errorf("no moments from before it opened: %d frames", frames)
			}
		}
	}
	if opened < 0 || opened > gateOpenAfter {
		t.Fatalf("opened at %d", opened)
	}
	if passed < 10 {
		t.Errorf("only %d frames of speech passed", passed)
	}
	for i := 0; i < gateCloseAt+2; i++ {
		g.Feed(frameOf(900), false, 0)
	}
	if _, loud := sent(g.Feed(frameOf(900), false, 0)); loud != 0 {
		t.Error("still open after a quiet spell")
	}
}

// A short click does not open it.
func TestGateIgnoresAClick(t *testing.T) {
	var g echoGate
	for i := 0; i < 60; i++ {
		g.Feed(frameOf(900), false, 0)
	}
	for i := 0; i < 3; i++ {
		if _, loud := sent(g.Feed(frameOf(5000), true, 0)); loud != 0 {
			t.Fatal("a click opened the gate")
		}
	}
	for i := 0; i < 30; i++ {
		if _, loud := sent(g.Feed(frameOf(900), false, 0)); loud != 0 {
			t.Fatal("the gate opened late for a click")
		}
	}
}

// Nothing is passed in the first stretch of a reply, however loud.
func TestGateLearnsFirst(t *testing.T) {
	var g echoGate
	for i := 0; i < gateLearn; i++ {
		if _, loud := sent(g.Feed(frameOf(8000), true, 0)); loud != 0 {
			t.Fatal("passed audio while learning")
		}
	}
}
