package voice

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/lib/realtime"
	"github.com/HuskerMinion/techo5/echod/internal/lib/safe"
	"github.com/HuskerMinion/techo5/echod/internal/lib/wyoming"
)

// The realtime pipeline: a turn answered by OpenAI's Realtime API. The microphone goes up as it is
// heard and the model finds the end of the sentence itself, so there is no speech to text step to wait
// for; the text it writes comes back a few words at a time and is cut into sentences, each spoken by
// the Wyoming text-to-speech server so the device keeps its own voice. See config.BrainRealtime.
//
// It is also what makes interrupting possible. The microphone stays open while the reply is spoken (the
// echo canceller takes the device's own voice out of it), and when the model hears someone start to
// talk the reply stops: what has not been said is dropped, and the conversation listens.

// Kit is what the realtime pipeline borrows from the assistant, which imports this package and so
// cannot be imported by it: what to tell the model, what it may do, and how to do it.
type Kit struct {
	Instructions func(now time.Time) string
	Tools        []realtime.Tool
	// Run does one tool call and returns what happened, in words for the model.
	Run func(name, arguments string) string
	// Tidy makes the model's text fit for speech.
	Tidy func(string) string
}

// realtimeURL overrides the API endpoint, for tests.
var realtimeURL string

// RealtimeKit supplies the Kit; set by the assistant feature, nil until it is.
var RealtimeKit func() Kit

// SetRealtimeKit installs the supplier.
func SetRealtimeKit(fn func() Kit) { RealtimeKit = fn }

// continuous is implemented by a backend that wants the microphone kept open through its reply, so
// that talking over it can interrupt it.
type continuous interface{ Continuous() bool }

// realtimeMemory is how long a session, and what was said in it, is kept for a follow-up.
const realtimeMemory = 5 * time.Minute

// quiet is what the model is told to answer with when there was nothing to answer. It is not spoken.
const quiet = "NOTHING"

// isQuiet is whether a sentence is just that.
func isQuiet(s string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(s), ".!?\"' "), quiet)
}

// maxPending bounds the microphone audio held while a session is still opening: 5 s at 16 kHz.
const maxPending = 5 * 16000 * 2

type realtimeBackend struct {
	post func(event)

	mu       sync.Mutex
	cl       *realtime.Client
	ready    bool // the session is configured and takes audio
	opening  bool
	pending  []byte // microphone audio from while it opened
	active   bool   // a turn is open
	lastUsed time.Time
	closer   *time.Timer
	kit      Kit

	// gen counts the times the person has started talking. Everything a reply produces carries the
	// generation it began in, and anything older than the current one is dropped, wherever it is.
	gen     uint64
	jobs    chan speakJob
	text    strings.Builder // the reply so far, for the screen
	split   *realtime.Sentences
	spoke   bool // the reply of this generation has had audio sent
	started time.Time
	// stoppedAt is when the model heard the person finish, which latency is counted from.
	stoppedAt time.Time
}

// speakJob is a sentence to say, or the end of the reply.
type speakJob struct {
	gen  uint64
	text string
	end  bool
}

func newRealtime(post func(event)) *realtimeBackend {
	b := &realtimeBackend{post: post, jobs: make(chan speakJob, 32)}
	safe.Go("realtime speaker", b.speaker)
	return b
}

func (b *realtimeBackend) Name() string       { return "realtime" }
func (b *realtimeBackend) Continuous() bool   { return true }
func (b *realtimeBackend) Ready() bool        { return config.Get().Brain.Realtime() && RealtimeKit != nil }
func (b *realtimeBackend) currentGen() uint64 { b.mu.Lock(); defer b.mu.Unlock(); return b.gen }

// Start opens a turn. The session may already be open from the last one, with its memory; if not it
// is opened in the background, and the microphone audio that arrives meanwhile is held.
func (b *realtimeBackend) Start(string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active = true
	b.lastUsed = time.Now()
	if b.closer != nil {
		b.closer.Stop()
		b.closer = nil
	}
	b.started = time.Now()
	if b.cl != nil && b.ready {
		kit := b.kit
		cl := b.cl
		safe.Go("realtime refresh", func() {
			_ = cl.ClearAudio()
			if kit.Instructions != nil {
				_ = cl.Update(instructionsFor(kit))
			}
		})
		return nil
	}
	if !b.opening {
		b.opening = true
		b.pending = nil
		safe.Go("realtime open", b.open)
	}
	return nil
}

func instructionsFor(k Kit) string {
	return k.Instructions(time.Now()) + "\n\nYou are talking with someone out loud, in real time, and they may interrupt you. " +
		"Reply in text only, in short spoken sentences. " +
		"Do not say anything before using a tool: speak once, after you have its result. " +
		"Counting aloud, spelling, reciting and saying things are answers to speak, not timers or alarms. " +
		"If what you heard is only noise, a fragment, or not meant for you, reply with exactly the word " + quiet +
		" and nothing else: that overrides any instruction to ask people to repeat or restate."
}

// open dials the session.
func (b *realtimeBackend) open() {
	br := config.Get().Brain
	kit := RealtimeKit()
	model := br.Model
	if model == "" {
		model = config.DefaultRealtimeModel
	}
	lang := br.Language
	if lang == "" {
		lang = "en"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cl, err := realtime.Dial(ctx, realtime.Config{
		Key: br.Key, Model: model, Instructions: instructionsFor(kit), Tools: kit.Tools,
		Transcribe: "gpt-4o-mini-transcribe", Language: lang, URL: realtimeURL,
	})
	if err == nil {
		// Configured when the server says so; audio sent before then is held.
		err = waitUpdated(ctx, cl)
	}
	b.mu.Lock()
	b.opening = false
	if err != nil {
		b.pending = nil
		b.mu.Unlock()
		if cl != nil {
			cl.Close()
		}
		slog.Error("realtime: could not open a session", "err", err)
		b.post(event{kind: evError, code: "realtime-connect", msg: err.Error()})
		return
	}
	b.cl, b.ready, b.kit = cl, true, kit
	held := b.pending
	b.pending = nil
	b.mu.Unlock()
	slog.Info("realtime: session open", "model", model, "held_ms", len(held)/32)
	if len(held) > 0 {
		_ = cl.Audio(realtime.To24k(pcm16(held)))
	}
	safe.Go("realtime events", func() { b.loop(cl) })
}

// waitUpdated reads events until the session reports it is configured, or fails.
func waitUpdated(ctx context.Context, cl *realtime.Client) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-cl.Events:
			if !ok {
				return fmt.Errorf("closed while opening: %v", cl.Err())
			}
			if e.Error != nil {
				return fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message)
			}
			if e.Type == "session.updated" {
				return nil
			}
		}
	}
}

func pcm16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

// Audio sends one microphone frame (16 kHz, 16-bit, mono), or holds it while the session opens.
func (b *realtimeBackend) Audio(frame []byte) error {
	b.mu.Lock()
	if !b.ready {
		if b.opening && len(b.pending)+len(frame) <= maxPending {
			b.pending = append(b.pending, frame...)
		}
		b.mu.Unlock()
		return nil
	}
	cl := b.cl
	b.mu.Unlock()
	return cl.Audio(realtime.To24k(pcm16(frame)))
}

// End is the microphone being closed. The model finds the end of the sentence itself, so there is
// nothing to say.
func (b *realtimeBackend) End() error { return nil }

// Stop closes the turn: whatever is being said is dropped, and the session is kept for a follow-up.
func (b *realtimeBackend) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active = false
	b.gen++
	b.pending = nil
	b.text.Reset()
	b.split = nil
	if b.cl != nil && b.ready {
		cl := b.cl
		safe.Go("realtime stop", func() { _ = cl.Cancel(); _ = cl.ClearAudio() })
	}
	if b.closer != nil {
		b.closer.Stop()
	}
	b.closer = time.AfterFunc(realtimeMemory, b.expire)
	return nil
}

// expire closes a session nobody has used for the memory time.
func (b *realtimeBackend) expire() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active || b.cl == nil {
		return
	}
	slog.Info("realtime: session closed after a quiet spell")
	b.cl.Close()
	b.cl, b.ready = nil, false
}

// loop reads the session's events until it ends.
func (b *realtimeBackend) loop(cl *realtime.Client) {
	for e := range cl.Events {
		b.handle(cl, e)
	}
	b.mu.Lock()
	if b.cl == cl {
		b.cl, b.ready = nil, false
		if b.active {
			b.mu.Unlock()
			slog.Error("realtime: the session ended", "err", cl.Err())
			b.post(event{kind: evError, code: "realtime-closed", msg: fmt.Sprint(cl.Err())})
			return
		}
	}
	b.mu.Unlock()
}

func (b *realtimeBackend) handle(cl *realtime.Client, e realtime.Event) {
	switch e.Type {
	case "input_audio_buffer.speech_started":
		b.mu.Lock()
		active := b.active
		b.gen++
		gen := b.gen
		b.text.Reset()
		b.split = nil
		b.spoke = false
		b.mu.Unlock()
		if !active {
			return
		}
		// Whatever was being said is stale now. The server cancels the response itself; the queue of
		// sentences and the speaker are this side's to stop.
		b.post(event{kind: evSpeaking})
		b.post(event{kind: evBargeIn, gen: gen})

	case "input_audio_buffer.speech_stopped":
		b.mu.Lock()
		b.stoppedAt = time.Now()
		b.mu.Unlock()
		b.post(event{kind: evHeard})

	case "conversation.item.input_audio_transcription.completed":
		if t := strings.TrimSpace(e.Transcript); t != "" {
			b.post(event{kind: evHeard, text: t})
		}

	case "response.output_text.delta":
		b.mu.Lock()
		if b.split == nil {
			b.split = realtime.NewSentences()
		}
		b.text.WriteString(e.Delta)
		pieces := b.split.Push(e.Delta)
		gen := b.gen
		b.mu.Unlock()
		for _, p := range pieces {
			b.say(gen, p)
		}

	case "response.done":
		b.responseDone(cl, e)

	case "error":
		// A cancel with nothing to cancel is not a problem.
		if e.Error != nil && e.Error.Code == "response_cancel_not_active" {
			return
		}
		if e.Error != nil {
			slog.Error("realtime: error from the API", "code", e.Error.Code, "message", e.Error.Message)
			b.post(event{kind: evError, code: "realtime", msg: e.Error.Message})
		}
	}
}

// say queues a sentence, dropping it if the person has spoken since.
func (b *realtimeBackend) say(gen uint64, text string) {
	if kit := b.kit; kit.Tidy != nil {
		text = kit.Tidy(text)
	}
	if text == "" || isQuiet(text) {
		return
	}
	select {
	case b.jobs <- speakJob{gen: gen, text: text}:
	default:
		slog.Warn("realtime: too many sentences waiting, one dropped")
	}
}

// responseDone finishes a response: tool calls are carried out and the model asked to go on, and a
// response that only talked is the end of the reply.
func (b *realtimeBackend) responseDone(cl *realtime.Client, e realtime.Event) {
	if e.Response == nil {
		return
	}
	var calls []struct{ id, name, args string }
	for _, o := range e.Response.Output {
		if o.Type == "function_call" {
			calls = append(calls, struct{ id, name, args string }{o.CallID, o.Name, o.Arguments})
		}
	}
	b.mu.Lock()
	gen := b.gen
	pieces := []string(nil)
	if b.split != nil {
		pieces = b.split.Flush()
	}
	full := strings.TrimSpace(b.text.String())
	kit := b.kit
	b.mu.Unlock()

	if e.Response.Status == "cancelled" {
		return
	}
	for _, p := range pieces {
		b.say(gen, p)
	}
	if len(calls) > 0 && kit.Run != nil {
		b.mu.Lock()
		if b.text.Len() > 0 {
			b.text.WriteString(" ")
		}
		b.mu.Unlock()
		safe.Go("realtime tools", func() {
			for _, c := range calls {
				result := kit.Run(c.name, c.args)
				slog.Info("realtime: tool", "name", c.name, "args", c.args, "result", result)
				if b.currentGen() != gen {
					return // the person talked over it; the model is already on a new answer
				}
				if err := cl.ToolResult(c.id, result); err != nil {
					b.post(event{kind: evError, code: "realtime", msg: err.Error()})
					return
				}
			}
		})
		return
	}
	if full != "" && !isQuiet(full) {
		b.post(event{kind: evReplyText, text: full, gen: gen})
	}
	b.jobs <- speakJob{gen: gen, end: true}
}

// speaker says sentences in order, each as soon as the last has been handed over: while one plays the
// next is being made.
func (b *realtimeBackend) speaker() {
	for j := range b.jobs {
		if j.gen != b.currentGen() {
			continue
		}
		if j.end {
			b.mu.Lock()
			spoke := b.spoke
			b.spoke = false
			b.text.Reset()
			b.mu.Unlock()
			if spoke {
				b.post(event{kind: evStreamEnd, gen: j.gen})
			} else {
				b.post(event{kind: evRunEnd, gen: j.gen})
			}
			continue
		}
		br := config.Get().Brain
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		voice, f, err := wyoming.Synthesize(ctx, br.TTS, j.text, br.Voice)
		cancel()
		if err != nil {
			slog.Error("realtime: speaking failed", "err", err)
			if j.gen == b.currentGen() {
				b.post(event{kind: evError, code: "tts-failed", msg: err.Error()})
			}
			continue
		}
		if j.gen != b.currentGen() {
			continue // spoken over while it was being made
		}
		voice = media.ToVoiceRate(voice, f.Rate)
		out := make([]byte, 2*len(voice))
		for i, s := range voice {
			binary.LittleEndian.PutUint16(out[2*i:], uint16(s))
		}
		b.mu.Lock()
		first := !b.spoke
		b.spoke = true
		since := time.Since(b.stoppedAt)
		b.mu.Unlock()
		if first {
			slog.Info("realtime: first sentence ready", "since_speech_end_ms", since.Milliseconds(), "text", j.text)
		}
		b.post(event{kind: evStreamAudio, audio: out, gen: j.gen})
	}
}
