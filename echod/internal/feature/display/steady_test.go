package display

import "testing"

// In a live conversation the screen stays on the words between turns; the first turn after the wake word,
// with nothing to show yet, gets the status card, and nothing else changes.
func TestALiveConversationDoesNotFlashStatusCards(t *testing.T) {
	for _, tc := range []struct {
		phase        string
		live         bool
		heard, reply string
		want         string
	}{
		{"listening", true, "", "", "listening"},                // just woken: the card is the feedback
		{"thinking", true, "", "", "thinking"},                  // first turn, nothing said yet
		{"listening", true, "what time is it", "", "lingering"}, // a follow-up after words
		{"thinking", true, "", "It is three.", "lingering"},
		{"thinking", true, "play music", "It is three.", "lingering"},
		{"replying", true, "hello", "hi", "replying"},    // replying already shows the words
		{"idle", true, "hello", "hi", "idle"},            // the screen's own lingering handles idle
		{"listening", false, "hello", "hi", "listening"}, // other pipelines are as they were
		{"thinking", false, "hello", "hi", "thinking"},
	} {
		if got := steadyPhase(tc.phase, tc.live, tc.heard, tc.reply); got != tc.want {
			t.Errorf("steadyPhase(%q, live %v, %q, %q) = %q, want %q", tc.phase, tc.live, tc.heard, tc.reply, got, tc.want)
		}
	}
}
