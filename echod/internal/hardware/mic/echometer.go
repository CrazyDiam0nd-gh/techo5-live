package mic

import (
	"log/slog"
	"math"
	"sync/atomic"
)

// echoMeter tells someone talking over the device's own voice from the echo of that voice.
//
// What is left of the echo after canceling follows the playback: loud while a word plays, quiet in the
// gaps. So a level cannot tell the two apart, since the echo of one loud word is louder than a person a
// few feet away, but the echo is a roughly fixed fraction of what is playing at that moment. What the
// person says is not, so it stands out wherever the playback is quiet, and wherever it is louder than
// that fraction would allow.
//
// The fraction is learned from the first stretch of each reply, which nobody talks over, and kept as the
// loudest it has been since, so it follows the filter converging or the volume changing.
type echoMeter struct {
	frames int
	refEnv float64 // the playback's recent level, held a moment so the room's tail is allowed for
	refTop float64 // the loudest the playback has been, which quiet moments are judged against
	k      float64 // the most residual per unit of playback seen so far
	over   atomic.Bool

	// ratio and level are published for the log.
	level atomic.Uint64
}

const (
	// meterLearn is how many frames at the start of playback are taken as echo alone: 400 ms.
	meterLearn = 20

	// refFall is how much of the playback level is kept from one 20 ms frame to the next once it falls,
	// for the room still ringing: 4 dB a frame, so 60 ms after a word the tail allowed for is 13 dB down.
	refFall = 0.6

	// meterMargin is how far over the learned fraction a frame has to be: 9 dB. Echo is not exactly
	// proportional, and a mistake here is a reply cut off by itself.
	meterMargin = 2.8

	// meterFloor is the least residual taken for speech, whatever the playback: about -55 dBFS, above any
	// room and a good deal below someone talking near the device.
	meterFloor = 60.0

	// meterRefMin is the playback level below which there is no echo worth a ratio.
	meterRefMin = 20.0

	// kFall lets the learned fraction drift down again, slowly, as the filter converges.
	kFall = 0.9995
)

func frameRMS(f []int16) float64 {
	if len(f) == 0 {
		return 0
	}
	var e float64
	for _, s := range f {
		e += float64(s) * float64(s)
	}
	return math.Sqrt(e / float64(len(f)))
}

// reset is for the playback ending.
func (m *echoMeter) reset() {
	m.frames, m.refEnv, m.refTop, m.k = 0, 0, 0, 0
	m.over.Store(false)
}

// feed takes one frame of the playback and what the microphone has left after canceling it, and says
// whether that is more than echo.
func (m *echoMeter) feed(ref, out []int16) bool {
	r, o := frameRMS(ref), frameRMS(out)
	m.frames++
	m.refEnv = math.Max(r, m.refEnv*refFall)
	m.refTop = math.Max(r, m.refTop*kFall)
	m.level.Store(math.Float64bits(o))
	if m.frames%25 == 0 {
		slog.Info("echo meter", "t_s", float64(m.frames)/50, "ref", int(r), "ref_held", int(m.refEnv), "residual", int(o),
			"fraction", math.Round(m.k*1000)/1000, "over", m.over.Load())
	}

	if m.refEnv >= meterRefMin {
		ratio := o / m.refEnv
		if m.frames <= meterLearn {
			m.k = math.Max(m.k, ratio)
			m.over.Store(false)
			return false
		}
		expected := m.k * m.refEnv * meterMargin
		speech := o > math.Max(expected, meterFloor)
		m.k *= kFall
		// Learned only where the echo dominates: in a gap a person's voice is most of what is there, and
		// would be taken for a louder echo. And it may only creep up, or one loud word could hide the next.
		if !speech && r >= 0.3*m.refTop {
			m.k = math.Min(math.Max(m.k, ratio), m.k*1.03+1e-9)
		}
		m.over.Store(speech)
		return speech
	}

	// Next to no playback: anything above the floor is not echo.
	if m.frames <= meterLearn {
		m.over.Store(false)
		return false
	}
	speech := o > meterFloor
	m.over.Store(speech)
	return speech
}
