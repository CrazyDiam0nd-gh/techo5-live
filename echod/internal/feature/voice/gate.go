package voice

import (
	"log/slog"
	"math"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/mic"
)

// echoGate keeps the device's own voice out of a realtime model while it is speaking.
//
// The model listens through the whole reply so that it can be talked over. The echo canceller takes
// most of the device's voice out of the microphone, but what is left is enough for the model to hear
// someone start to talk, and a reply cut off by its own echo, then answered, is a loop. So while a
// reply plays, the model is sent silence unless the microphone is clearly louder than that echo has
// been.
//
// How loud the echo is changes with the volume, the room and the sentence, so it is learned: the
// first stretch of every reply is taken to be echo alone (nobody interrupts in the first half second),
// and after that the echo level is followed as it rises and falls. Speech has to stand out from it for
// a moment before the gate opens, and the half second before that is sent too, so the model hears the
// start of the sentence. It closes again after a quiet spell.
type echoGate struct {
	frames int
	floor  float64 // the echo, as RMS, following the loudest recent frame down slowly
	max    float64
	above  int
	quiet  int
	open   bool
	opened int
	ring   [][]int16
}

const (
	gateLearn     = 20   // frames taken as echo alone: 400 ms
	gatePre       = 15   // frames sent from before the gate opened: 300 ms
	gateOpenAfter = 6    // frames above the threshold before it opens: 120 ms
	gateCloseAt   = 75   // quiet frames before it closes again: 1.5 s
	gateRatio     = 2.5  // how far above the echo speech has to be: 8 dB
	gateMinRMS    = 250. // and never less than this, about -42 dBFS
	gateDecay     = 0.997
)

var silence = make([]int16, mic.FrameSamples)

func rms(f []int16) float64 {
	if len(f) == 0 {
		return 0
	}
	var e float64
	for _, s := range f {
		e += float64(s) * float64(s)
	}
	return math.Sqrt(e / float64(len(f)))
}

func dbfs(r float64) float64 { return math.Round(20*math.Log10(math.Max(r, 1)/32768)*10) / 10 }

// Reset is called when no reply is playing, and reports how the last one went.
func (g *echoGate) Reset() {
	if g.frames > 0 {
		slog.Info("echo gate", "frames", g.frames, "echo_dbfs", dbfs(g.floor), "loudest_dbfs", dbfs(g.max), "opened", g.opened)
	}
	*g = echoGate{}
}

func (g *echoGate) threshold() float64 { return math.Max(g.floor*gateRatio, gateMinRMS) }

// Feed takes one microphone frame heard while a reply plays, and returns what to send the model: silence,
// or speech with the moments before it.
func (g *echoGate) Feed(frame []int16) [][]int16 {
	r := rms(frame)
	g.frames++
	g.max = math.Max(g.max, r)
	g.ring = append(g.ring, frame)
	if len(g.ring) > gatePre {
		g.ring = g.ring[1:]
	}

	if g.frames <= gateLearn {
		g.floor = math.Max(g.floor, r)
		return [][]int16{silence}
	}

	loud := r > g.threshold()
	if g.frames%25 == 0 {
		slog.Info("echo gate trace", "t_s", float64(g.frames)/50, "rms_dbfs", dbfs(r), "echo_dbfs", dbfs(g.floor),
			"threshold_dbfs", dbfs(g.threshold()), "open", g.open)
	}
	if !loud {
		g.floor = math.Max(g.floor*gateDecay, r)
	} else {
		g.floor *= gateDecay
	}

	if g.open {
		if loud {
			g.quiet = 0
		} else if g.quiet++; g.quiet >= gateCloseAt {
			g.open, g.above = false, 0
		}
		return [][]int16{frame}
	}

	if loud {
		g.above++
	} else {
		g.above = max(0, g.above-1)
	}
	if g.above < gateOpenAfter {
		return [][]int16{silence}
	}
	g.open, g.quiet = true, 0
	g.opened++
	slog.Info("echo gate opened", "rms_dbfs", dbfs(r), "echo_dbfs", dbfs(g.floor), "threshold_dbfs", dbfs(g.threshold()))
	out := g.ring
	g.ring = nil
	return out
}
