package voice

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
	"github.com/HuskerMinion/techo5/echod/internal/lib/live"
	"github.com/HuskerMinion/techo5/echod/internal/lib/safe"
)

// The live pipeline: OpenAI's Live API (gpt-live-1), a model that listens while it talks and answers
// in its own voice, so there is no text-to-speech step and nothing here finds the end of a sentence.
// See config.BrainLive and package live.
//
// The model's voice is a steady stream, silence included, and the conversation is made of turns, so
// the turns are found here. A reply begins with the first voiced audio and ends after a pause long
// enough that it has finished and not just drawn breath; while the backend is working on something
// the pause may be longer. The person talking over it is found from the transcript: words that begin
// on the session's clock after the model began to speak are talking over it, and the last words of the
// question, whose transcript arrives late, are not.
//
// A session bills for the time it is open, so it is kept only for the follow-up that may come.

const (
	// liveMemory is how long a session is kept after a conversation, for a follow-up, unless the owner chose
	// otherwise (keepFor). liveMaxSession is the longest one is used before it is renewed at the start of a turn.
	liveMemory     = 20 * time.Second
	liveMaxSession = 8 * time.Minute

	// voicedRMS is the level above which a tenth of a second of the model's voice is speech.
	voicedRMS = 250
	// endSilence ends a reply; workSilence does the same while the backend is still thinking or running
	// a tool; talkedOverAfter is how long after the model began to speak the person's words must begin
	// to count as talking over it.
	endSilence      = 1200 * time.Millisecond
	workSilence     = 25 * time.Second
	workGrace       = 6 * time.Second // the backend is taken to be working this long after it last did anything
	resultGrace     = 8 * time.Second // and this long after a tool's result went back
	talkedOverAfter = 300
)

type liveBackend struct {
	post func(event)

	mu       sync.Mutex
	cl       *live.Client
	ready    bool
	opening  bool
	openedAt time.Time
	openWith string // the settings the session was opened with
	pending  []byte // microphone audio from while it opened
	active   bool
	closer   *time.Timer
	kit      Kit

	// gen goes up each time the person talks over a reply; what the reply produces carries the one it
	// began in, and the conversation drops anything older than the newest.
	gen uint64

	// The turn in progress.
	heard       strings.Builder // what the person said
	said        strings.Builder // what the model said, as text
	heardPosted bool
	heardEndMs  int             // where the person's last word ended on the session's clock
	sentMs      int             // how much microphone audio the session has been sent: the same clock
	replyInMs   int             // sentMs when the model began to answer; -1 before
	overlap     strings.Builder // what the person has said since the model began to speak
	speaking    bool            // voiced audio is flowing
	silentMs    int             // the pause so far inside a reply
	held        [][]int16       // the pause, kept back in case speech resumes (at 16 kHz)
	dropping    bool            // the tail of a reply that was talked over: dropped until it goes quiet
	replyFrom   int             // the model's clock when it began to speak, in ms; -1 before it has
	// The backend is working while a tool runs, and for a few seconds after it last did anything:
	// the model is silent while it waits, and that is not the end of what it has to say. It has
	// finished when a response completes that asked for no tool; one that asked for a tool completes
	// at once, before the tool has run, and is not the end.
	workUntil  time.Time
	tools      int             // tools being run
	callInResp bool            // the response now in progress asked for a tool
	res        *live.Resampler // 24 kHz to the voice rate
	spoke      time.Time

	micMu sync.Mutex
	mic   *live.Resampler // the microphone's 16 kHz to the model's 24 kHz
}

func newLive(post func(event)) *liveBackend {
	return &liveBackend{post: post, replyFrom: -1, replyInMs: -1, res: live.NewResampler(live.SampleRate, speaker.VoiceRate),
		mic: live.NewResampler(16000, live.SampleRate)}
}

func (b *liveBackend) Name() string     { return "live" }
func (b *liveBackend) Continuous() bool { return true }
func (b *liveBackend) Ready() bool      { return config.Get().Brain.Live() && RealtimeKit != nil }

// instructionsForLive tells the voice model what it is, and, since it decides for itself whether to
// hand a request to the backend that has the tools, which requests those are.
func instructionsForLive(k Kit) string {
	var sb strings.Builder
	if config.Get().Brain.LiveNoPrompt && k.Base != nil {
		sb.WriteString(k.Base(time.Now()))
	} else {
		sb.WriteString(k.Instructions(time.Now()))
	}
	sb.WriteString("\n\nYou are talking with someone out loud, in real time, and they may interrupt you. " +
		"Keep replies to one or two short spoken sentences. Never use lists or markdown. " +
		"Delegate to the backend whenever the request is about the current time or date, timers, alarms, " +
		"volume, the weather, music or the radio, the screen or the cameras, or anything about the house and its devices, and whenever " +
		"someone asks you to change any of them. Delegate before giving an answer that depends on backend " +
		"work, and do not guess the result while you wait: say nothing while the backend works, then give " +
		"the result in one short sentence. Never say Checking, Working or anything like it before or while " +
		"you wait, and do not say what you are about to do. Do not repeat what the backend reported, and do " +
		"not add praise. Counting aloud, spelling, reciting and saying things are answers to speak, not " +
		"timers or alarms. When someone interrupts you, stop at once and do what they now ask; never go back " +
		"to what you were saying unless they ask you to. If what you heard was only noise, a fragment, or not " +
		"meant for you, say nothing.")
	if len(k.Tools) > 0 {
		sb.WriteString("\n\nBackend tools:")
		for _, t := range k.Tools {
			desc := t.Description
			if i := strings.IndexAny(desc, ".\n"); i > 0 && i < 140 {
				desc = desc[:i]
			}
			if len(desc) > 140 {
				desc = desc[:140]
			}
			fmt.Fprintf(&sb, "\n- %s: %s", t.Name, desc)
		}
	}
	return sb.String()
}

// Start opens a turn, and the session if it is not open: the microphone audio that arrives while it
// opens is held and sent when it has.
func (b *liveBackend) Start(string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active = true
	if b.closer != nil {
		b.closer.Stop()
		b.closer = nil
	}
	b.newTurn()
	b.micMu.Lock()
	b.mic.Reset()
	b.micMu.Unlock()
	if b.cl != nil && b.ready && time.Since(b.openedAt) > liveMaxSession {
		slog.Info("live: renewing a long session")
		b.cl.Close()
		b.cl, b.ready = nil, false
	}
	if b.cl != nil && b.ready && b.openWith != liveSettings() {
		slog.Info("live: the settings changed, opening a new session")
		b.cl.Close()
		b.cl, b.ready = nil, false
	}
	if b.cl != nil && b.ready && RealtimeKit != nil && len(RealtimeKit().Tools) != len(b.kit.Tools) {
		slog.Info("live: the tools changed, opening a new session")
		b.cl.Close()
		b.cl, b.ready = nil, false
	}
	if b.cl != nil && b.ready {
		return nil
	}
	if !b.opening {
		b.opening = true
		b.pending = nil
		safe.Go("live open", b.open)
	}
	return nil
}

// newTurn forgets the last turn. Called with mu held.
func (b *liveBackend) newTurn() {
	b.heard.Reset()
	b.said.Reset()
	b.heardPosted, b.speaking, b.dropping = false, false, false
	b.silentMs, b.held, b.replyFrom, b.replyInMs = 0, nil, -1, -1
	b.workUntil, b.callInResp = time.Time{}, false
	b.res.Reset()
	b.spoke = time.Time{}
}

func (b *liveBackend) open() {
	br := config.Get().Brain
	settings := liveSettings()
	kit := RealtimeKit()
	opening := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var tools []live.Tool
	for _, t := range kit.Tools {
		tools = append(tools, live.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	cl, err := live.Dial(ctx, live.Config{Key: br.Key, Model: br.LiveModel, Backend: br.LiveBackend,
		Voice: br.LiveVoice, Instructions: instructionsForLive(kit), Tools: tools, URL: liveURL})
	b.mu.Lock()
	b.opening = false
	if err != nil {
		b.pending = nil
		b.mu.Unlock()
		slog.Error("live: could not open a session", "err", err)
		b.post(event{kind: evError, code: "live-connect", msg: err.Error()})
		return
	}
	b.cl, b.ready, b.kit, b.openedAt, b.openWith = cl, true, kit, time.Now(), settings
	b.sentMs, b.heardEndMs = 0, 0
	held := b.pending
	b.pending = nil
	b.mu.Unlock()
	slog.Info("live: session open", "took_ms", time.Since(opening).Milliseconds(), "held_ms", len(held)/32,
		"voice", br.LiveVoice, "persona", !br.LiveNoPrompt)
	if len(held) > 0 {
		b.micMu.Lock()
		out := b.mic.Push(live.PCM16(held))
		b.micMu.Unlock()
		b.mu.Lock()
		b.sentMs += len(out) * 1000 / live.SampleRate
		b.mu.Unlock()
		_ = cl.Audio(live.Bytes(out))
	}
	safe.Go("live events", func() { b.loop(cl) })
}

// afterWork is how long the voice is given, once the backend has finished, to say what it found: the
// backend is done before the voice has spoken the result, and a reply that ended in that gap would have
// its answer cut off.
var afterWork = 3 * time.Second

// liveURL overrides the API endpoint, for tests.
var liveURL string

// liveSettings is what a session is opened with, so that one opened with other settings is not reused:
// the voice, the models and the instructions are fixed when a session starts.
func liveSettings() string {
	br := config.Get().Brain
	return strings.Join([]string{br.LiveVoice, br.LiveModel, br.LiveBackend, br.Key, br.Prompt,
		fmt.Sprint(br.LiveNoPrompt)}, "\x00")
}

// maxLiveHeld bounds the microphone audio held while a session opens: 5 s at 16 kHz.
const maxLiveHeld = 5 * 16000 * 2

// Audio sends one microphone frame (16 kHz, 16-bit, mono), or holds it while the session opens.
func (b *liveBackend) Audio(frame []byte) error {
	b.mu.Lock()
	if !b.ready {
		if b.opening && len(b.pending)+len(frame) <= maxLiveHeld {
			b.pending = append(b.pending, frame...)
		}
		b.mu.Unlock()
		return nil
	}
	cl := b.cl
	b.mu.Unlock()
	b.micMu.Lock()
	out := b.mic.Push(live.PCM16(frame))
	b.micMu.Unlock()
	b.mu.Lock()
	b.sentMs += len(out) * 1000 / live.SampleRate
	b.mu.Unlock()
	return cl.Audio(live.Bytes(out))
}

// End is the microphone being closed. The model hears the end of the question itself.
func (b *liveBackend) End() error { return nil }

// Stop closes the turn. A model still talking is stopped by ending the session, which also ends the
// billing; one that has finished is kept a short while for a follow-up.
func (b *liveBackend) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active = false
	b.gen++
	b.pending = nil
	talking := b.speaking
	b.newTurn()
	if b.closer != nil {
		b.closer.Stop()
	}
	if talking && b.cl != nil {
		slog.Info("live: stopped while the model was talking, closing the session")
		b.cl.Close()
		b.cl, b.ready = nil, false
		return nil
	}
	b.closer = time.AfterFunc(keepFor(), b.expire)
	return nil
}

func (b *liveBackend) expire() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active || b.cl == nil {
		return
	}
	slog.Info("live: session closed after a quiet spell")
	b.cl.Close()
	b.cl, b.ready = nil, false
}

func (b *liveBackend) loop(cl *live.Client) {
	for e := range cl.Events {
		b.handle(cl, e)
	}
	b.mu.Lock()
	if b.cl == cl {
		b.cl, b.ready = nil, false
		if b.active {
			b.mu.Unlock()
			slog.Error("live: the session ended", "err", cl.Err())
			b.post(event{kind: evError, code: "live-closed", msg: fmt.Sprint(cl.Err())})
			return
		}
	}
	b.mu.Unlock()
}

func (b *liveBackend) handle(cl *live.Client, e live.Event) {
	switch e.Type {
	case "session.input_transcript.delta":
		b.onHeard(e)

	case "session.output_transcript.delta":
		b.mu.Lock()
		if b.replyFrom < 0 {
			b.replyFrom = e.StartMs
			// The microphone's clock is the person's, and the model's own voice has another that does not
			// move with it, so what the person says is compared with what has been sent, not with this.
			b.replyInMs = b.sentMs
			slog.Info("live: the model began to answer", "words", strings.TrimSpace(e.Delta),
				"mic_sent_ms", b.sentMs, "last_word_ended_ms", b.heardEndMs)
		}
		b.said.WriteString(e.Delta)
		b.mu.Unlock()

	case "session.output_audio.delta":
		if pcm, ok := e.Audio(); ok {
			b.onVoice(live.PCM16(pcm))
		}

	case "session.delegation.created":
		b.mu.Lock()
		b.workUntil = time.Now().Add(workGrace)
		b.mu.Unlock()
		slog.Info("live: the model handed the request to the backend")

	case "response.event":
		call, isCall := e.FunctionCall()
		b.mu.Lock()
		if isCall {
			b.callInResp = true
			b.tools++
		}
		b.workUntil = time.Now().Add(workGrace)
		if e.Inner != nil {
			switch e.Inner.Type {
			case "response.completed", "response.failed", "response.incomplete", "response.cancelled":
				if b.callInResp {
					b.callInResp = false // it asked for a tool: the answer comes after the result
				} else {
					b.workUntil = time.Now().Add(afterWork)
				}
			}
		}
		b.mu.Unlock()
		if isCall {
			b.runTool(cl, call)
		}

	case "error":
		if e.Error != nil {
			slog.Error("live: error from the API", "code", e.Error.Code, "message", e.Error.Message)
			b.post(event{kind: evError, code: "live", msg: e.Error.Message})
		}
	}
}

// onHeard takes a piece of the person's words. If the model is already speaking and the words began
// after it did, they are talking over it.
func (b *liveBackend) onHeard(e live.Event) {
	b.mu.Lock()
	b.heard.WriteString(e.Delta)
	b.heardEndMs = max(b.heardEndMs, e.EndMs)
	// Words that begin after the model began to speak are said over it; but a murmur of agreement is not
	// a reason for it to stop, and the model, hearing one, goes on. Only a real word counts.
	over := b.speaking && b.replyInMs >= 0 && e.StartMs >= b.replyInMs+talkedOverAfter
	talkedOver := false
	if over {
		b.overlap.WriteString(e.Delta)
		talkedOver = realWords(b.overlap.String())
	}
	var gen uint64
	if talkedOver {
		b.gen++
		gen = b.gen
		b.speaking, b.dropping, b.held = false, true, nil
		b.said.Reset()
		b.heard.Reset()
		b.heard.WriteString(b.overlap.String())
		b.overlap.Reset()
		b.heardPosted, b.replyFrom, b.replyInMs = false, -1, -1
		b.workUntil, b.callInResp = time.Time{}, false
		b.res.Reset()
	}
	active := b.active
	b.mu.Unlock()
	if !active {
		return
	}
	b.post(event{kind: evSpeaking})
	if talkedOver {
		slog.Info("live: talked over", "at_ms", e.StartMs)
		b.post(event{kind: evBargeIn, gen: gen})
	}
}

// onVoice takes a tenth of a second or so of the model's voice, at 24 kHz.
func (b *liveBackend) onVoice(samples []int16) {
	voiced := live.RMS(samples) > voicedRMS // judged before any boost: the level it was spoken at
	samples = live.Boost(samples, config.Get().Brain.LiveBoost)
	ms := len(samples) * 1000 / live.SampleRate
	b.mu.Lock()
	if b.dropping {
		if voiced {
			b.mu.Unlock()
			return
		}
		b.dropping = false
	}
	if !b.speaking {
		if !voiced || !b.active {
			b.mu.Unlock()
			return
		}
		b.speaking, b.silentMs, b.held = true, 0, nil
		b.res.Reset()
		b.spoke = time.Now()
	}
	out := b.res.Push(samples)
	var forward [][]int16
	ended := false
	if voiced {
		b.silentMs = 0
		forward = append(b.held, out)
		b.held = nil
	} else {
		b.silentMs += ms
		b.held = append(b.held, out)
		limit := endSilence
		if b.tools > 0 || time.Now().Before(b.workUntil) {
			limit = workSilence
		}
		ended = time.Duration(b.silentMs)*time.Millisecond >= limit
	}
	gen := b.gen
	postHeard := !b.heardPosted
	b.heardPosted = true
	heard := strings.TrimSpace(b.heard.String())
	var said string
	if ended {
		said = cleanSaid(b.said.String())
		b.speaking, b.held, b.silentMs, b.replyFrom, b.replyInMs = false, nil, 0, -1, -1
		b.overlap.Reset()
		b.said.Reset()
		b.heard.Reset()
		b.heardPosted = false
	}
	b.mu.Unlock()

	if postHeard {
		b.post(event{kind: evHeard, text: heard})
	}
	// One event for the lot: a pause held back while the backend worked comes out with the speech that ends
	// it, forty chunks or more at once, and the conversation's queue holds thirty-two. Sent one by one the
	// overflow was dropped, which is a piece of the model's voice gone.
	var audio []byte
	for _, chunk := range forward {
		audio = append(audio, live.Bytes(chunk)...)
	}
	if len(audio) > 0 {
		b.post(event{kind: evStreamAudio, audio: audio, gen: gen})
	}
	if ended {
		if said != "" {
			b.post(event{kind: evReplyText, text: said, gen: gen})
		}
		b.post(event{kind: evStreamEnd, gen: gen})
	}
}

// runTool does what the backend asked and gives it the result, whatever has happened since: the
// backend waits for the answer, and the person may have talked over the reply that asked.
func (b *liveBackend) runTool(cl *live.Client, call live.Call) {
	b.mu.Lock()
	kit := b.kit
	b.mu.Unlock()
	if kit.Run == nil {
		_ = cl.ToolResult(call, "That is not something this device can do.")
		b.mu.Lock()
		b.tools = max(0, b.tools-1)
		b.mu.Unlock()
		return
	}
	safe.Go("live tool", func() {
		result := kit.Run(call.Name, call.Arguments)
		slog.Info("live: tool", "name", call.Name, "args", call.Arguments, "result", result)
		err := cl.ToolResult(call, result)
		b.mu.Lock()
		b.tools = max(0, b.tools-1)
		b.workUntil = time.Now().Add(resultGrace) // the backend now writes its answer
		b.mu.Unlock()
		if err != nil {
			b.post(event{kind: evError, code: "live", msg: err.Error()})
		}
	})
}

// backchannels are the sounds and short words people make to say they are listening, which are not
// an attempt to take the turn.
var backchannels = map[string]bool{
	"mm": true, "mmm": true, "mhm": true, "mmhm": true, "hm": true, "hmm": true, "uh": true, "um": true, "huh": true,
	"ah": true, "oh": true, "yeah": true, "yep": true, "yes": true, "ok": true, "okay": true, "right": true,
	"sure": true, "alright": true, "cool": true, "nice": true, "wow": true, "great": true, "aha": true,
}

// realWords is whether what was said over the model holds anything but backchannel.
func realWords(s string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '\''
	}) {
		if !backchannels[w] {
			return true
		}
	}
	return false
}

// stageDirection is a bracketed word or two the model puts in its transcript for a sound it makes, like
// [breath] or [laughs]. They belong to the audio, and on a screen or in the log they are noise.
var stageDirection = regexp.MustCompile(`\s*\[[a-zA-Z ,'-]{1,24}\]\s*`)

// cleanSaid is the model's transcript of its reply as text for the screen and the log.
func cleanSaid(s string) string {
	return strings.TrimSpace(stageDirection.ReplaceAllString(s, " "))
}

// keepFor is how long a session stays open after a conversation. A longer wait costs the time it is open and
// saves the two or three seconds a new session takes to start.
func keepFor() time.Duration {
	if n := config.Get().Brain.LiveKeep; n > 0 {
		return time.Duration(n) * time.Second
	}
	return liveMemory
}
