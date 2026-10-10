package voice

import (
	"encoding/base64"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/live"
	"github.com/HuskerMinion/techo5/echod/internal/lib/realtime"
)

// fakeLive is a Live server: it starts the session when asked, hands the test a way to send events, and
// reports what the client sent.
type fakeLive struct {
	t    *testing.T
	send func(v any)
	got  chan map[string]any
	done chan struct{}
}

func newFakeLive(t *testing.T) (*fakeLive, string) {
	t.Helper()
	f := &fakeLive{t: t, got: make(chan map[string]any, 256), done: make(chan struct{})}
	ready := make(chan struct{})
	var once, doneOnce sync.Once
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var mu sync.Mutex
		f.send = func(v any) { mu.Lock(); defer mu.Unlock(); _ = ws.WriteJSON(v) }
		defer func() { doneOnce.Do(func() { close(f.done) }) }()
		for {
			var m map[string]any
			if ws.ReadJSON(&m) != nil {
				return
			}
			if m["type"] == "session.start" {
				f.send(map[string]any{"type": "session.started", "session": map[string]any{"id": "s"}})
				once.Do(func() { close(ready) })
			}
			f.got <- m
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		select {
		case <-ready:
		default:
			once.Do(func() { close(ready) })
		}
	})
	_ = ready
	return f, "ws" + strings.TrimPrefix(srv.URL, "http")
}

// chunk is a tenth of a second of the model's voice at 24 kHz: a tone, or silence.
func chunk(voiced bool) map[string]any {
	n := live.SampleRate / 10
	s := make([]int16, n)
	if voiced {
		for i := range s {
			s[i] = int16(8000 * math.Sin(2*math.Pi*300*float64(i)/live.SampleRate))
		}
	}
	return map[string]any{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(live.Bytes(s))}
}

func (f *fakeLive) voice(voiced bool, n int) {
	for range n {
		f.send(chunk(voiced))
	}
}

func (f *fakeLive) heard(word string, startMs int) {
	f.send(map[string]any{"type": "session.input_transcript.delta", "delta": word, "start_ms": startMs, "end_ms": startMs + 200})
}

func (f *fakeLive) said(word string, startMs int) {
	f.send(map[string]any{"type": "session.output_transcript.delta", "delta": word, "start_ms": startMs, "end_ms": startMs + 200})
}

// liveSetup configures the pipeline and returns a started backend with its events, and the server it
// opened.
func liveSetup(t *testing.T, run func(name, args string) string) (*liveBackend, chan event, *fakeLive) {
	t.Helper()
	f, url := newFakeLive(t)
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if err := config.Set().Brain().Set(config.Brain{Mode: config.BrainLive, LiveVoice: "cedar"}); err != nil {
		t.Fatal(err)
	}
	if err := config.Set().Brain().SetKey("sk-test"); err != nil {
		t.Fatal(err)
	}
	liveURL = url
	oldKit := RealtimeKit
	RealtimeKit = func() Kit {
		return Kit{
			Instructions: func(time.Time) string { return "be brief" },
			Tools:        []realtime.Tool{{Name: "get_time", Description: "The time. More words.", Parameters: map[string]any{"type": "object"}}},
			Run:          run,
		}
	}
	oldAfter := afterWork
	afterWork = 0 // tests that look at the wait for the voice set it themselves
	t.Cleanup(func() { liveURL, RealtimeKit, afterWork = "", oldKit, oldAfter })

	events := make(chan event, 512)
	b := newLive(func(e event) { events <- e })
	if !b.Ready() {
		t.Fatal("not ready")
	}
	if err := b.Start(""); err != nil {
		t.Fatal(err)
	}
	waitReady(t, b)
	return b, events, f
}

// feed sends ms of microphone audio (silence at 16 kHz) to the backend, in the 20 ms frames it arrives in.
func feed(t *testing.T, b *liveBackend, ms int) {
	t.Helper()
	frame := make([]byte, 640)
	for range ms / 20 {
		if err := b.Audio(frame); err != nil {
			t.Fatal(err)
		}
	}
}

// waitReady waits for the backend's session to be open.
func waitReady(t *testing.T, b *liveBackend) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		b.mu.Lock()
		ok := b.ready
		b.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the session never opened")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// waitFor returns the events up to and including the first of kind, or fails.
func waitFor(t *testing.T, events chan event, kind eventKind) []event {
	t.Helper()
	var got []event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			got = append(got, e)
			if e.kind == kind {
				return got
			}
		case <-deadline:
			t.Fatalf("no event %d; got %+v", kind, got)
		}
	}
}

func countKind(es []event, kind eventKind) (n int) {
	for _, e := range es {
		if e.kind == kind {
			n++
		}
	}
	return
}

func samples(es []event) (n int) {
	for _, e := range es {
		if e.kind == evStreamAudio {
			n += len(e.audio) / 2
		}
	}
	return
}

// A question, then an answer: the silence before the voice is not played, the voice is, the silence
// after it is not, and the turn is reported as heard, replied and ended.
func TestLiveAnAnswerBecomesATurn(t *testing.T) {
	b, events, f := liveSetup(t, nil)
	_ = b
	f.voice(false, 15) // listening
	f.heard(" What", 400)
	f.heard(" time is it", 600)
	f.said(" It is", 2000)
	f.said(" three.", 2200)
	f.voice(true, 8)
	f.voice(false, 3) // a breath inside the reply
	f.voice(true, 4)
	f.voice(false, 14) // and the end of it

	got := waitFor(t, events, evStreamEnd)
	if first := got[0]; first.kind != evSpeaking && first.kind != evHeard {
		t.Errorf("the first event was %+v", first)
	}
	if countKind(got, evHeard) != 1 {
		t.Errorf("heard %d times", countKind(got, evHeard))
	}
	for _, e := range got {
		if e.kind == evHeard && e.text != "What time is it" {
			t.Errorf("heard %q", e.text)
		}
		if e.kind == evReplyText && e.text != "It is three." {
			t.Errorf("replied %q", e.text)
		}
	}
	// 12 voiced tenths and a breath of 3 between them, at 16 kHz; not the 15 before or 14 after.
	want := 15 * 1600
	if n := samples(got); n < want-200 || n > want+200 {
		t.Errorf("%d samples played, want about %d", n, want)
	}
	if countKind(got, evReplyText) != 1 {
		t.Errorf("replied %d times", countKind(got, evReplyText))
	}
}

// Words that begin after the model began to speak are talking over it; the last words of the question,
// which the transcript delivers late, are not. What the model has still to say of the reply that was
// talked over is dropped until it falls quiet.
func TestLiveTalkingOverAReply(t *testing.T) {
	b, events, f := liveSetup(t, nil)
	f.heard(" Tell me a story", 400)
	feed(t, b, 2000) // the microphone has been heard for two seconds when the model begins
	// The model's own clock is nowhere near the microphone's, as on the device; only the latter counts.
	f.said(" Once", 120000)
	f.voice(true, 5)
	f.heard(" please", 1500) // a late word of the question: not talking over
	f.voice(true, 2)
	got := waitFor(t, events, evStreamAudio) // let the above through
	_ = got
	time.Sleep(100 * time.Millisecond)
	for len(events) > 0 {
		if e := <-events; e.kind == evBargeIn {
			t.Fatal("the last words of the question were taken for talking over")
		}
	}

	f.heard(" Stop", 3500) // after the model began at 2000
	got = waitFor(t, events, evBargeIn)
	bi := got[len(got)-1]
	if bi.gen == 0 {
		t.Errorf("barge-in carries generation %d", bi.gen)
	}
	f.voice(true, 3) // the tail the model had already sent
	f.voice(false, 2)
	f.voice(true, 3) // its answer to the interruption
	f.said(" Sure.", 124200)
	f.voice(false, 14)
	got = waitFor(t, events, evStreamEnd)
	if n := samples(got); n < 3*1600-200 || n > 3*1600+200 {
		t.Errorf("%d samples after the barge-in, want the 3 tenths of the new answer", n)
	}
	for _, e := range got {
		if e.kind == evStreamAudio && e.gen != bi.gen {
			t.Errorf("audio carries generation %d, want %d", e.gen, bi.gen)
		}
	}
}

// A function call is carried out and answered by call id.
func TestLiveAToolCall(t *testing.T) {
	var mu sync.Mutex
	var ran []string
	_, _, f := liveSetup(t, func(name, args string) string {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, name+args)
		return "3 PM"
	})
	f.send(map[string]any{"type": "response.event", "delegation_id": "d1", "event": map[string]any{
		"type": "response.output_item.done", "item": map[string]any{
			"type": "function_call", "call_id": "c9", "name": "get_time", "arguments": "{}"}}})

	var item, create bool
	deadline := time.After(3 * time.Second)
	for !(item && create) {
		select {
		case m := <-f.got:
			switch m["type"] {
			case "response.item.create":
				it, _ := m["item"].(map[string]any)
				item = it["call_id"] == "c9" && it["output"] == "3 PM"
			case "response.create":
				create = true
			}
		case <-deadline:
			t.Fatalf("result not sent (item %v, create %v)", item, create)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 || ran[0] != "get_time{}" {
		t.Errorf("ran %v", ran)
	}
}

// While the backend is working, a long pause is not the end of the reply; when it has finished and the
// voice has gone quiet again, it is.
func TestLiveWaitsForTheBackend(t *testing.T) {
	_, events, f := liveSetup(t, nil)
	f.heard(" Is the door locked", 400)
	f.send(map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "d"}})
	f.said(" Checking.", 1500)
	f.voice(true, 6)
	f.voice(false, 40) // four seconds of pause while the backend works

	time.Sleep(200 * time.Millisecond)
	for len(events) > 0 {
		if e := <-events; e.kind == evStreamEnd {
			t.Fatal("the reply ended while the backend was working")
		}
	}
	f.send(map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{"type": "response.completed"}})
	f.said(" It is locked.", 6000)
	f.voice(true, 8)
	f.voice(false, 14)
	got := waitFor(t, events, evStreamEnd)
	if n := samples(got); n < (8+40)*1600-200 {
		// the pause between "Checking." and the answer is played, so the answer follows it naturally
		t.Errorf("%d samples played, want the answer and the pause before it", n)
	}
}

// Stopping while the model is talking ends the session, which silences it.
func TestLiveStopSilencesATalkingModel(t *testing.T) {
	b, events, f := liveSetup(t, nil)
	f.heard(" Hello", 400)
	f.said(" Hi", 1500)
	f.voice(true, 4)
	waitFor(t, events, evStreamAudio)
	if err := b.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the session was left open with the model talking")
	}
}

// A conversation that ended quietly keeps its session for the follow-up.
func TestLiveStopKeepsAnIdleSessionForAFollowUp(t *testing.T) {
	b, _, f := liveSetup(t, nil)
	if err := b.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.done:
		t.Fatal("the session was closed at once")
	case <-time.After(300 * time.Millisecond):
	}
	b.mu.Lock()
	timer := b.closer
	b.mu.Unlock()
	if timer == nil {
		t.Error("no timer to close it")
	}
	timer.Stop()
}

func TestLiveInstructionsNameTheTools(t *testing.T) {
	k := Kit{
		Instructions: func(time.Time) string { return "You are the ship's computer." },
		Tools: []realtime.Tool{
			{Name: "home_status", Description: "Look at the state of devices. Takes names."},
			{Name: "set_timer", Description: strings.Repeat("long ", 80)},
		},
	}
	s := instructionsForLive(k)
	for _, want := range []string{"You are the ship's computer.", "Delegate to the backend whenever", "Backend tools:",
		"- home_status: Look at the state of devices", "- set_timer: long"} {
		if !strings.Contains(s, want) {
			t.Errorf("the instructions lack %q", want)
		}
	}
	if strings.Contains(s, "Takes names") {
		t.Error("a tool description ran past its first sentence")
	}
	if len(s) > 4000 {
		t.Errorf("%d characters of instructions", len(s))
	}
}

func TestLiveIsChosenOnlyWhenSetUp(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	_ = config.Set().Brain().Set(config.Brain{Mode: config.BrainLive})
	if config.Get().Brain.Live() {
		t.Error("live with no key")
	}
	_ = config.Set().Brain().SetKey("k")
	if !config.Get().Brain.Live() {
		t.Error("live with a key is not live")
	}
	if config.Get().Brain.Realtime() || config.Get().Brain.Direct() {
		t.Error("live is also another mode")
	}
}

// The backend's first response completes the moment it asks for a tool, before the tool has run. That is
// not the end of the reply: the model says "Checking", waits, and then gives the result, all in one reply.
func TestLiveAReplyThatUsesAToolIsOneReply(t *testing.T) {
	release := make(chan struct{})
	_, events, f := liveSetup(t, func(name, args string) string {
		<-release
		return "locked"
	})
	f.heard(" Is the door locked", 400)
	f.send(map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "d"}})
	f.said(" Checking.", 1500)
	f.voice(true, 6)
	f.send(map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{
		"type": "response.output_item.done", "item": map[string]any{
			"type": "function_call", "call_id": "c", "name": "get_time", "arguments": "{}"}}})
	f.send(map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{"type": "response.completed"}})
	f.voice(false, 40) // four seconds while the tool runs

	time.Sleep(200 * time.Millisecond)
	for len(events) > 0 {
		if e := <-events; e.kind == evStreamEnd {
			t.Fatal("the reply ended while the tool was running")
		}
	}
	close(release)
	var result bool
	deadline := time.After(3 * time.Second)
	for !result {
		select {
		case m := <-f.got:
			result = m["type"] == "response.create"
		case <-deadline:
			t.Fatal("the result was not sent")
		}
	}
	f.send(map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{"type": "response.created"}})
	f.said(" It is locked.", 7000)
	f.voice(true, 8)
	f.send(map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{"type": "response.completed"}})
	f.voice(false, 14)
	got := waitFor(t, events, evStreamEnd)
	// The filler was read off the queue above; what is left is the wait, played out, and the answer.
	if n := samples(got); n < (40+8)*1600-300 {
		t.Errorf("%d samples in the reply, want the wait and the answer", n)
	}
	time.Sleep(100 * time.Millisecond)
	for len(events) > 0 {
		if e := <-events; e.kind == evStreamEnd {
			t.Error("the reply ended twice")
		}
	}
}

// Audio after a reply was declared finished is dropped, not sent into a closed channel.
func TestAStreamThatIsDoneTakesNoMoreAudio(t *testing.T) {
	s := newStream(0, 0, 0)
	s.send([]byte{1, 0})
	s.done()
	s.done() // twice is fine
	s.send([]byte{2, 0})
	n := 0
	for range s.chunks {
		n++
	}
	if n != 1 {
		t.Errorf("%d chunks, want the one sent before the end", n)
	}
}

// The live model is in the conversation all the time: a follow-up does not announce itself.
func TestQuietFollowUpOnlyForLiveFollowUps(t *testing.T) {
	l := newLive(func(event) {})
	if !quietFollowUp(l, true) {
		t.Error("a live follow-up makes a sound")
	}
	if quietFollowUp(l, false) {
		t.Error("a live wake word makes no sound")
	}
	if quietFollowUp(newRealtime(func(event) {}), true) {
		t.Error("the half-duplex mode lost its follow-up sound")
	}
}

// The backend has finished before the voice has said what it found. The reply is kept open for the voice
// to speak, so the answer is part of it and not lost to a turn that has already ended.
func TestLiveTheVoiceIsGivenTimeToSayTheResult(t *testing.T) {
	_, events, f := liveSetup(t, func(name, args string) string { return "off" })
	afterWork = 400 * time.Millisecond
	f.heard(" Is the light on", 400)
	f.send(map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "d"}})
	f.said(" Checking.", 1500)
	f.voice(true, 5)
	f.send(map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{
		"type": "response.output_item.done", "item": map[string]any{
			"type": "function_call", "call_id": "c", "name": "get_time", "arguments": "{}"}}})
	f.send(map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{"type": "response.completed"}})
	for m := range f.got { // the result goes back
		if m["type"] == "response.create" {
			break
		}
	}
	f.send(map[string]any{"type": "response.event", "delegation_id": "d", "event": map[string]any{"type": "response.completed"}})
	f.voice(false, 20) // two seconds of the voice thinking, by its own clock: more than a pause

	time.Sleep(100 * time.Millisecond)
	for len(events) > 0 {
		if e := <-events; e.kind == evStreamEnd {
			t.Fatal("the reply ended before the voice said the result")
		}
	}
	f.said(" It is off.", 5000)
	f.voice(true, 6)
	time.Sleep(500 * time.Millisecond) // the wait for the voice runs out
	f.voice(false, 14)
	got := waitFor(t, events, evStreamEnd)
	if n := samples(got); n < 6*1600-200 {
		t.Errorf("%d samples after the pause, want the 6 tenths of the answer", n)
	}
}

// A voice, a model or the household's text changed on the setup page applies to the next conversation: a
// session opened before is not reused for it.
func TestLiveASettingChangeOpensANewSession(t *testing.T) {
	b, _, f := liveSetup(t, nil)
	first := f.done
	if err := b.Stop(); err != nil {
		t.Fatal(err)
	}
	// Nothing changed: the same session is kept.
	if err := b.Start(""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
		t.Fatal("the session was closed with nothing changed")
	case <-time.After(150 * time.Millisecond):
	}
	_ = b.Stop()

	br := config.Get().Brain
	br.LiveVoice = "coral"
	if err := config.Set().Brain().Set(br); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("the old session stayed open after the voice was changed")
	}
	waitReady(t, b) // and the new one opens before this test's settings are put back
}

func TestLiveSettingsAreWhatASessionIsOpenedWith(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	base := config.Brain{Mode: config.BrainLive, LiveVoice: "marin", Prompt: "a"}
	set := func(b config.Brain) string {
		_ = config.Set().Brain().Set(b)
		return liveSettings()
	}
	was := set(base)
	for name, mutate := range map[string]func(*config.Brain){
		"voice":   func(b *config.Brain) { b.LiveVoice = "cedar" },
		"model":   func(b *config.Brain) { b.LiveModel = "x" },
		"backend": func(b *config.Brain) { b.LiveBackend = "y" },
		"prompt":  func(b *config.Brain) { b.Prompt = "b" },
		"persona": func(b *config.Brain) { b.LiveNoPrompt = true },
	} {
		b := base
		mutate(&b)
		if set(b) == was {
			t.Errorf("changing the %s does not change the settings", name)
		}
	}
	if set(base) != was {
		t.Error("the same settings differ")
	}
}

// With the household's text switched off the voice is told the device's rules and not the persona.
func TestLiveCanLeaveOutThePersona(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	k := Kit{
		Instructions: func(time.Time) string { return "RULES and PERSONA" },
		Base:         func(time.Time) string { return "RULES" },
	}
	_ = config.Set().Brain().Set(config.Brain{Mode: config.BrainLive})
	if s := instructionsForLive(k); !strings.Contains(s, "PERSONA") {
		t.Error("the persona is missing by default")
	}
	_ = config.Set().Brain().Set(config.Brain{Mode: config.BrainLive, LiveNoPrompt: true})
	if s := instructionsForLive(k); strings.Contains(s, "PERSONA") || !strings.Contains(s, "RULES") {
		t.Errorf("switched off: %q", s[:40])
	}
	k.Base = nil // nothing to fall back to: the full instructions, not none
	if s := instructionsForLive(k); !strings.Contains(s, "RULES") {
		t.Error("no instructions at all without a Base")
	}
}

// The instructions no longer tell the model to say anything before the backend's result.
func TestLiveInstructionsAskForNoFiller(t *testing.T) {
	s := instructionsForLive(Kit{Instructions: func(time.Time) string { return "x" }})
	if !strings.Contains(s, "When someone interrupts you, stop at once") {
		t.Error("the model is not told what to do when interrupted")
	}
	if !strings.Contains(s, "Never say Checking") || strings.Contains(s, "at most the one word") {
		t.Errorf("the instructions about filler: %s", s)
	}
}

// A murmur of agreement said over the model is not an interruption: the model goes on, and so does its
// voice. A real word after it is.
func TestLiveABackchannelIsNotAnInterruption(t *testing.T) {
	b, events, f := liveSetup(t, nil)
	f.heard(" Tell me a story", 400)
	feed(t, b, 2000)
	f.said(" Once", 120000)
	f.voice(true, 5)
	f.heard(" Mm", 4000) // after the model began
	f.heard(" yeah", 4300)
	f.voice(true, 3)
	waitFor(t, events, evStreamAudio)
	time.Sleep(150 * time.Millisecond)
	for len(events) > 0 {
		if e := <-events; e.kind == evBargeIn {
			t.Fatal("a backchannel was taken for talking over")
		}
	}
	f.heard(" actually", 5000) // a real word
	got := waitFor(t, events, evBargeIn)
	if got[len(got)-1].gen == 0 {
		t.Error("the barge-in has no generation")
	}
}

func TestRealWords(t *testing.T) {
	for s, want := range map[string]bool{
		"": false, " Mm": false, " Mm-hm.": false, " uh huh": false, " Yeah, okay": false, "OK!": false, " mmm yeah right": false,
		" stop": true, " Mm, actually": true, " no": true, " 5": true, " don't": true,
	} {
		if got := realWords(s); got != want {
			t.Errorf("realWords(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestCleanSaidDropsSoundTags(t *testing.T) {
	for in, want := range map[string]string{
		"[breath] Done, the light is on.": "Done, the light is on.",
		"Okay [laughs] that is funny":     "Okay that is funny",
		"No tags here.":                   "No tags here.",
		"[breath]":                        "",
		"Keep [this one that is far too long to be a tag at all].": "Keep [this one that is far too long to be a tag at all].",
		"  Spaces   ": "Spaces",
	} {
		if got := cleanSaid(in); got != want {
			t.Errorf("cleanSaid(%q) = %q, want %q", in, got, want)
		}
	}
}

// A pause held back while the backend worked is delivered with the speech after it as one piece of audio,
// not as dozens of events that would overflow the conversation's queue.
func TestLiveAHeldPauseIsOneEvent(t *testing.T) {
	_, events, f := liveSetup(t, nil)
	f.heard(" Is it on", 400)
	f.send(map[string]any{"type": "session.delegation.created", "delegation": map[string]any{"id": "d"}})
	f.voice(true, 3)
	f.voice(false, 60) // six seconds of waiting, held back
	f.voice(true, 4)
	time.Sleep(300 * time.Millisecond)
	var audio []event
	for len(events) > 0 {
		if e := <-events; e.kind == evStreamAudio {
			audio = append(audio, e)
		}
	}
	if len(audio) > 12 { // 7 as it should be; one per chunk would be 67
		t.Errorf("%d audio events; the held pause should come out with the speech after it, in one", len(audio))
	}
	total := 0
	for _, e := range audio {
		total += len(e.audio) / 2
	}
	if want := (3 + 60 + 4) * 1600; total < want-300 || total > want+300 {
		t.Errorf("%d samples delivered, want about %d: the pause is part of the reply", total, want)
	}
}

// With a boost set the voice that reaches the speaker is louder, and what counts as speaking is judged on
// the level it was spoken at.
func TestLiveTheVoiceIsPlayedLouderWhenAsked(t *testing.T) {
	level := func(boost int) float64 {
		_, events, f := liveSetup(t, nil)
		br := config.Get().Brain
		br.LiveBoost = boost
		if err := config.Set().Brain().Set(br); err != nil {
			t.Fatal(err)
		}
		f.heard(" Hello", 400)
		f.voice(true, 4)
		f.voice(false, 14)
		got := waitFor(t, events, evStreamEnd)
		var all []int16
		for _, e := range got {
			if e.kind == evStreamAudio {
				all = append(all, live.PCM16(e.audio)...)
			}
		}
		return live.RMS(all)
	}
	normal, loud := level(0), level(6)
	if loud < normal*1.9 || loud > normal*2.1 {
		t.Errorf("+6 dB: %.0f against %.0f, want about twice", loud, normal)
	}
}
