package voice

// How readily the model is sent your voice while it is speaking (config.Brain.LiveHear).
//
// Its own voice is in the microphones at the volume of the reply, and the model, hearing that, may take it for
// someone speaking and stop. So while a reply plays the model is sent silence unless the microphone has clearly more
// in it than the echo of what is playing: how much more is the margin. A wide one is safe and means raising your
// voice over it; a narrow one lets an ordinary voice through and risks the device hearing itself; none at all is how
// a phone does it, with the echo canceller alone between the speaker and the model.
//
// Careful is what it has always been (9 dB over the echo).
const (
	hearCareful   = ""
	hearBalanced  = "balanced"
	hearSensitive = "sensitive"
	hearOpen      = "open"
)

// hearing is the margin to use, in times the expected echo, and whether the gate is bypassed altogether.
func hearing(mode string) (margin float64, open bool) {
	switch mode {
	case hearBalanced:
		return 1.8, false // 5 dB
	case hearSensitive:
		return 1.3, false // 2 dB
	case hearOpen:
		return 1.3, true
	}
	return 2.8, false // 9 dB: careful
}
