package led

import "time"

// steady is the whole ring lit and still. For a conversation it is the plainest way to say that the
// device is in one: it comes on when the wake word is heard and stays on, through listening, thinking and
// speaking, until the conversation is over.
func steady(p Palette) Frame {
	return func(time.Duration) []Color { return around(p, 1) }
}
