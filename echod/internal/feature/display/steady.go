package display

// steadyPhase is the phase the screen draws. In a live conversation the turns follow one another every few
// seconds, and a full-screen "Listening…" or "Thinking…" card for each of them is a flash; once there are
// words from this conversation on the screen, they stay, and only the first turn after the wake word (with
// nothing yet to show) gets the status card, as feedback that it was heard.
func steadyPhase(phase string, live bool, heard, reply string) string {
	if live && (phase == "listening" || phase == "thinking") && (heard != "" || reply != "") {
		return "lingering"
	}
	return phase
}
