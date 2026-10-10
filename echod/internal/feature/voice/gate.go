package voice

import (
	"log/slog"
)

// echoGate keeps the device's own voice out of a realtime model while it is speaking.
//
// The model listens through the whole reply so that it can be talked over. The echo canceller takes most
// of the device's voice out of the microphone, but what is left is enough for the model to hear someone
// start to talk, and a reply cut off by its own echo, then answered, is a loop. So while a reply plays,
// the model is sent silence unless the microphone has more in it than the echo of what is playing (the
// microphone package judges that, mic.Source.OverEcho), for long enough not to be a click. Then it is
// sent that, with the moments before it, so the model hears the start of the sentence. It closes again
// after a quiet spell.
type echoGate struct {
	frames int
	above  int
	quiet  int
	open   bool
	opened int
	ring   [][]int16
}

const (
	gateLearn     = 25 // frames at the start of a reply that are echo alone: 500 ms
	gatePre       = 15 // frames sent from before the gate opened: 300 ms
	gateOpenAfter = 6  // frames of speech before it opens: 120 ms
	gateCloseAt   = 75 // quiet frames before it closes again: 1.5 s
)

var silence = make([]int16, 320)

// Reset is called when no reply is playing, and reports how the last one went.
func (g *echoGate) Reset() {
	if g.frames > 0 {
		slog.Info("echo gate", "frames", g.frames, "opened", g.opened)
	}
	*g = echoGate{}
}

// Feed takes one microphone frame heard while a reply plays, whether it holds more than the echo, and
// the level of what is left after canceling (for the log), and returns what to send the model: silence,
// or speech with the moments before it.
func (g *echoGate) Feed(frame []int16, over bool, residual float64) [][]int16 {
	g.frames++
	g.ring = append(g.ring, frame)
	if len(g.ring) > gatePre {
		g.ring = g.ring[1:]
	}
	if g.frames%25 == 0 {
		slog.Info("echo gate trace", "t_s", float64(g.frames)/50, "over_echo", over, "residual", int(residual), "open", g.open)
	}

	if g.frames <= gateLearn {
		return [][]int16{silence}
	}

	if g.open {
		if over {
			g.quiet = 0
		} else if g.quiet++; g.quiet >= gateCloseAt {
			g.open, g.above = false, 0
		}
		return [][]int16{frame}
	}

	if over {
		g.above++
	} else {
		g.above = max(0, g.above-1)
	}
	if g.above < gateOpenAfter {
		return [][]int16{silence}
	}
	g.open, g.quiet = true, 0
	g.opened++
	slog.Info("echo gate opened", "residual", int(residual))
	out := g.ring
	g.ring = nil
	return out
}
