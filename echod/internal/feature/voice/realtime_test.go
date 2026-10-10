package voice

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/realtime"
)

// fakeVoice is a Wyoming text-to-speech server: every synthesize is answered with a tenth of a second
// of silence at 16 kHz, after delay.
func fakeVoice(t *testing.T, delay time.Duration) (addr string, said *[]string, mu *sync.Mutex) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	said, mu = new([]string), new(sync.Mutex)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				line, err := r.ReadBytes('\n')
				if err != nil {
					return
				}
				var h struct {
					Data       map[string]any `json:"data"`
					DataLength int            `json:"data_length"`
				}
				_ = json.Unmarshal(line, &h)
				if h.DataLength > 0 {
					b := make([]byte, h.DataLength)
					_, _ = r.Read(b)
					_ = json.Unmarshal(b, &h.Data)
				}
				if s, ok := h.Data["text"].(string); ok {
					mu.Lock()
					*said = append(*said, s)
					mu.Unlock()
				}
				time.Sleep(delay)
				f := `"data":{"rate":16000,"width":2,"channels":1}`
				_, _ = c.Write([]byte(`{"type":"audio-start",` + f + "}\n"))
				_, _ = c.Write(append([]byte(`{"type":"audio-chunk",`+f+`,"payload_length":3200}`+"\n"), make([]byte, 3200)...))
				_, _ = c.Write([]byte(`{"type":"audio-stop"}` + "\n"))
			}()
		}
	}()
	return l.Addr().String(), said, mu
}

// fakeAPI is a Realtime server. onAudio is called with each client event as it arrives, with a way to
// send server events back.
func fakeAPI(t *testing.T, onEvent func(typ string, raw map[string]any, send func(v any))) string {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		var mu sync.Mutex
		send := func(v any) {
			mu.Lock()
			defer mu.Unlock()
			_ = ws.WriteJSON(v)
		}
		for {
			var m map[string]any
			if ws.ReadJSON(&m) != nil {
				return
			}
			typ, _ := m["type"].(string)
			if typ == "session.update" {
				send(map[string]any{"type": "session.updated"})
			}
			onEvent(typ, m, send)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func realtimeSetup(t *testing.T, voiceAddr, apiURL string, run func(name, args string) string) *realtimeBackend {
	t.Helper()
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if err := config.Set().Brain().Set(config.Brain{Mode: config.BrainRealtime, TTS: voiceAddr, Voice: "v"}); err != nil {
		t.Fatal(err)
	}
	if err := config.Set().Brain().SetKey("sk-test"); err != nil {
		t.Fatal(err)
	}
	realtimeURL = apiURL
	oldKit := RealtimeKit
	RealtimeKit = func() Kit {
		return Kit{
			Instructions: func(time.Time) string { return "be brief" },
			Tools:        []realtime.Tool{{Name: "lights", Description: "d", Parameters: map[string]any{"type": "object"}}},
			Run:          run,
			Tidy:         strings.TrimSpace,
		}
	}
	t.Cleanup(func() { realtimeURL, RealtimeKit = "", oldKit })
	return nil
}

// collect gathers the backend's events until one of kind stop arrives, or fails after a while.
func collect(t *testing.T, events chan event, stop eventKind) []event {
	t.Helper()
	var got []event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			got = append(got, e)
			if e.kind == stop {
				return got
			}
		case <-timeout:
			t.Fatalf("no event %d; got %+v", stop, got)
		}
	}
}

func kinds(es []event) []eventKind {
	var out []eventKind
	for _, e := range es {
		out = append(out, e.kind)
	}
	return out
}

func count(es []event, k eventKind) int {
	n := 0
	for _, e := range es {
		if e.kind == k {
			n++
		}
	}
	return n
}

func frame() []byte { return make([]byte, 640) } // 20 ms at 16 kHz

// A person speaks, the model answers in two sentences, and each is spoken: the events the conversation
// needs arrive in the order it needs them, with the words and the audio.
func TestRealtimeAnswers(t *testing.T) {
	voiceAddr, said, mu := fakeVoice(t, 0)
	var audio int
	api := fakeAPI(t, func(typ string, m map[string]any, send func(any)) {
		if typ != "input_audio_buffer.append" {
			return
		}
		audio++
		if audio != 1 {
			return
		}
		send(map[string]any{"type": "input_audio_buffer.speech_started"})
		send(map[string]any{"type": "input_audio_buffer.speech_stopped"})
		send(map[string]any{"type": "conversation.item.input_audio_transcription.completed", "transcript": "what is up"})
		for _, d := range []string{"Working. Divert", "ing power now."} {
			send(map[string]any{"type": "response.output_text.delta", "delta": d})
		}
		send(map[string]any{"type": "response.done", "response": map[string]any{"status": "completed"}})
	})
	realtimeSetup(t, voiceAddr, api, nil)

	events := make(chan event, 64)
	b := newRealtime(func(e event) { events <- e })
	if err := b.Start("computer"); err != nil {
		t.Fatal(err)
	}
	for range 3 { // sent while the session is still opening, so held
		if err := b.Audio(frame()); err != nil {
			t.Fatal(err)
		}
	}
	got := collect(t, events, evStreamEnd)

	if count(got, evStreamAudio) != 2 || count(got, evReplyText) != 1 {
		t.Fatalf("events %v", kinds(got))
	}
	for _, e := range got {
		if e.kind == evReplyText && e.text != "Working. Diverting power now." {
			t.Errorf("reply text %q", e.text)
		}
		if e.kind == evStreamAudio && len(e.audio) == 0 {
			t.Error("empty audio")
		}
	}
	var heard string
	for _, e := range got {
		if e.kind == evHeard && e.text != "" {
			heard = e.text
		}
	}
	if heard != "what is up" {
		t.Errorf("heard %q", heard)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(*said, "|") != "Working.|Diverting power now." {
		t.Errorf("spoken %q", *said)
	}
}

// Talking over a reply drops the sentences that were still to be said, and says so.
func TestRealtimeBargeIn(t *testing.T) {
	voiceAddr, _, _ := fakeVoice(t, 150*time.Millisecond)
	var audio int
	api := fakeAPI(t, func(typ string, m map[string]any, send func(any)) {
		if typ != "input_audio_buffer.append" {
			return
		}
		audio++
		switch audio {
		case 1:
			send(map[string]any{"type": "input_audio_buffer.speech_started"})
			send(map[string]any{"type": "input_audio_buffer.speech_stopped"})
			send(map[string]any{"type": "response.output_text.delta",
				"delta": "The first sentence is here. The second sentence is here. The third sentence is here."})
			send(map[string]any{"type": "response.done", "response": map[string]any{"status": "completed"}})
		case 2:
			time.Sleep(200 * time.Millisecond) // the first sentence is out, the others are being made
			send(map[string]any{"type": "input_audio_buffer.speech_started"})
		}
	})
	realtimeSetup(t, voiceAddr, api, nil)

	events := make(chan event, 64)
	b := newRealtime(func(e event) { events <- e })
	_ = b.Start("computer")
	_ = b.Audio(frame())
	time.Sleep(600 * time.Millisecond) // let it open and begin
	_ = b.Audio(frame())
	got := collect(t, events, evBargeIn)
	var barge event
	for _, e := range got {
		if e.kind == evBargeIn {
			barge = e
		}
	}
	// The first barge-in is the opening of the turn; wait for the one that follows speech.
	if barge.gen == 0 {
		t.Fatal("no generation on the barge-in")
	}
	first := barge.gen
	more := collect(t, events, evBargeIn)
	second := more[len(more)-1].gen
	if second <= first {
		t.Errorf("generations %d then %d", first, second)
	}
	time.Sleep(600 * time.Millisecond)
	for len(events) > 0 {
		e := <-events
		if e.kind == evStreamAudio && e.gen < second {
			t.Errorf("audio from before the interruption was sent: gen %d", e.gen)
		}
		if e.kind == evStreamEnd {
			t.Error("the interrupted reply was ended as if it finished")
		}
	}
}

// A tool the model calls is run, and its result goes back with a request to carry on.
func TestRealtimeToolCall(t *testing.T) {
	voiceAddr, _, _ := fakeVoice(t, 0)
	gotResult := make(chan string, 1)
	var audio int
	api := fakeAPI(t, func(typ string, m map[string]any, send func(any)) {
		switch typ {
		case "input_audio_buffer.append":
			audio++
			if audio == 1 {
				send(map[string]any{"type": "input_audio_buffer.speech_started"})
				send(map[string]any{"type": "input_audio_buffer.speech_stopped"})
				send(map[string]any{"type": "response.done", "response": map[string]any{"status": "completed",
					"output": []any{map[string]any{"type": "function_call", "name": "lights", "call_id": "c1", "arguments": `{"on":true}`}}}})
			}
		case "conversation.item.create":
			item, _ := m["item"].(map[string]any)
			if item["type"] == "function_call_output" {
				gotResult <- item["output"].(string)
			}
		case "response.create":
			send(map[string]any{"type": "response.output_text.delta", "delta": "The lights are on."})
			send(map[string]any{"type": "response.done", "response": map[string]any{"status": "completed"}})
		}
	})
	var ran string
	realtimeSetup(t, voiceAddr, api, func(name, args string) string { ran = name + args; return "done: on" })

	events := make(chan event, 64)
	b := newRealtime(func(e event) { events <- e })
	_ = b.Start("computer")
	_ = b.Audio(frame())
	got := collect(t, events, evStreamEnd)
	if ran != `lights{"on":true}` {
		t.Errorf("ran %q", ran)
	}
	select {
	case r := <-gotResult:
		if r != "done: on" {
			t.Errorf("result %q", r)
		}
	default:
		t.Error("the result never went back")
	}
	if count(got, evReplyText) != 1 {
		t.Errorf("events %v", kinds(got))
	}
}

// Talking over a reply puts the turn back to listening, and audio from before it is not played.
func TestConversationBargeIn(t *testing.T) {
	c := thinkingTurn(t)
	pcm := make([]byte, 6400)

	c.handle(event{kind: evStreamAudio, audio: pcm, gen: 1})
	if c.phase != phaseReplying {
		t.Fatalf("phase %v, want replying", c.phase)
	}

	c.handle(event{kind: evBargeIn, gen: 2})
	if c.phase != phaseListening {
		t.Fatalf("after being talked over: phase %v, want listening", c.phase)
	}

	// The rest of the old reply is still on its way.
	c.handle(event{kind: evStreamAudio, audio: pcm, gen: 1})
	c.handle(event{kind: evStreamEnd, gen: 1})
	if c.phase != phaseListening {
		t.Errorf("stale audio moved the turn to %v", c.phase)
	}

	// The new answer is wanted.
	c.handle(event{kind: evStreamAudio, audio: pcm, gen: 2})
	if c.phase != phaseReplying {
		t.Errorf("phase %v, want replying for the new answer", c.phase)
	}
}

// The first words of a request are not an interruption of anything.
func TestBargeInAtTheStartOfATurnIsIgnored(t *testing.T) {
	c := thinkingTurn(t)
	c.phase = phaseListening
	c.handle(event{kind: evBargeIn, gen: 1})
	if c.phase != phaseListening {
		t.Errorf("phase %v", c.phase)
	}
}
