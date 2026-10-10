package live

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fake is a Live server that records what it is sent and plays back what the test hands it.
type fake struct {
	t      *testing.T
	srv    *httptest.Server
	auth   string
	got    chan map[string]any
	script func(c *websocket.Conn, first map[string]any)
}

func newFake(t *testing.T, script func(c *websocket.Conn, first map[string]any)) *fake {
	f := &fake{t: t, got: make(chan map[string]any, 64), script: script}
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth = r.Header.Get("Authorization")
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var first map[string]any
		if err := c.ReadJSON(&first); err != nil {
			return
		}
		f.got <- first
		go func() {
			for {
				var m map[string]any
				if c.ReadJSON(&m) != nil {
					return
				}
				f.got <- m
			}
		}()
		f.script(c, first)
		time.Sleep(300 * time.Millisecond)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fake) next() map[string]any {
	f.t.Helper()
	select {
	case m := <-f.got:
		return m
	case <-time.After(3 * time.Second):
		f.t.Fatal("the server was sent nothing")
		return nil
	}
}

func started(c *websocket.Conn) {
	_ = c.WriteJSON(map[string]any{"type": "session.started", "session": map[string]any{"id": "s1"}})
}

func TestTheSessionIsStartedAsTheGuideSays(t *testing.T) {
	f := newFake(t, func(c *websocket.Conn, _ map[string]any) { started(c) })
	cl, err := Dial(context.Background(), Config{Key: "sk-x", Voice: "marin", Instructions: "Be brief.", URL: f.url(),
		Tools: []Tool{{Name: "get_time", Description: "the time", Parameters: map[string]any{"type": "object"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if f.auth != "Bearer sk-x" {
		t.Errorf("authorization %q", f.auth)
	}
	first := f.next()
	b, _ := json.Marshal(first)
	for _, want := range []string{`"type":"session.start"`, `"model":"gpt-live-1"`, `"instructions":"Be brief."`,
		`"audio/pcm"`, `"rate":24000`, `"voice":"marin"`, `"delegation":{"responses":{"model":"gpt-5.6-luna"`,
		`"type":"responses"`, `"name":"get_time"`, `"tool_choice":"auto"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the start message lacks %s:\n%s", want, b)
		}
	}
}

func TestNoToolsMeansNoToolList(t *testing.T) {
	b, _ := json.Marshal(StartMessage(Config{Instructions: "x"}))
	if strings.Contains(string(b), "tools") || strings.Contains(string(b), `"voice"`) {
		t.Errorf("an empty config sent tools or a voice: %s", b)
	}
}

func TestAudioInAndAudioOut(t *testing.T) {
	pcm := []byte{1, 0, 2, 0, 3, 0}
	f := newFake(t, func(c *websocket.Conn, _ map[string]any) {
		started(c)
		_ = c.WriteJSON(map[string]any{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(pcm)})
		_ = c.WriteJSON(map[string]any{"type": "session.output_transcript.delta", "delta": "Hello"})
	})
	cl, err := Dial(context.Background(), Config{Key: "k", URL: f.url()})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	f.next() // session.start
	if err := cl.Audio([]byte{9, 0, 8, 0}); err != nil {
		t.Fatal(err)
	}
	m := f.next()
	if m["type"] != "session.input_audio.append" || m["audio"] != base64.StdEncoding.EncodeToString([]byte{9, 0, 8, 0}) {
		t.Errorf("audio sent as %v", m)
	}
	e := <-cl.Events
	if got, ok := e.Audio(); !ok || string(got) != string(pcm) {
		t.Errorf("audio out %v %v", got, ok)
	}
	e = <-cl.Events
	if _, ok := e.Audio(); ok || e.Delta != "Hello" {
		t.Errorf("a transcript delta read as %+v", e)
	}
}

// A function call arrives inside a response.event envelope; the result goes back with the call's id.
func TestAFunctionCallIsCarriedOutAndAnswered(t *testing.T) {
	f := newFake(t, func(c *websocket.Conn, _ map[string]any) {
		started(c)
		_ = c.WriteJSON(map[string]any{"type": "response.event", "delegation_id": "d1", "event": map[string]any{
			"type": "response.output_item.done", "item": map[string]any{
				"type": "function_call", "call_id": "c1", "name": "get_time", "arguments": `{"zone":"UTC"}`}}})
		_ = c.WriteJSON(map[string]any{"type": "response.event", "delegation_id": "d1", "event": map[string]any{
			"type": "response.output_item.done", "item": map[string]any{"type": "message"}}})
	})
	cl, err := Dial(context.Background(), Config{Key: "k", URL: f.url()})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	f.next()
	call, ok := (<-cl.Events).FunctionCall()
	if !ok || call != (Call{DelegationID: "d1", CallID: "c1", Name: "get_time", Arguments: `{"zone":"UTC"}`}) {
		t.Fatalf("call %+v %v", call, ok)
	}
	if _, ok := (<-cl.Events).FunctionCall(); ok {
		t.Error("a message item read as a function call")
	}
	if err := cl.ToolResult(call, "3 PM"); err != nil {
		t.Fatal(err)
	}
	a, b := f.next(), f.next()
	item, _ := a["item"].(map[string]any)
	if a["type"] != "response.item.create" || item["type"] != "function_call_output" || item["call_id"] != "c1" ||
		item["output"] != "3 PM" || a["delegation_id"] != "d1" {
		t.Errorf("result sent as %v", a)
	}
	if b["type"] != "response.create" {
		t.Errorf("then %v", b)
	}
}

func TestAnErrorWhileStartingIsReturned(t *testing.T) {
	f := newFake(t, func(c *websocket.Conn, _ map[string]any) {
		_ = c.WriteJSON(map[string]any{"type": "error", "error": map[string]any{"code": "invalid_voice", "message": "no such voice"}})
	})
	_, err := Dial(context.Background(), Config{Key: "k", Voice: "nope", URL: f.url()})
	if err == nil || !strings.Contains(err.Error(), "invalid_voice") {
		t.Errorf("got %v", err)
	}
}

func TestNoKeyIsRefusedBeforeConnecting(t *testing.T) {
	if _, err := Dial(context.Background(), Config{}); err == nil {
		t.Error("dialed with no key")
	}
}

func TestMuteAndInstructions(t *testing.T) {
	f := newFake(t, func(c *websocket.Conn, _ map[string]any) { started(c) })
	cl, err := Dial(context.Background(), Config{Key: "k", URL: f.url()})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	f.next()
	_ = cl.Mute()
	_ = cl.Unmute()
	_ = cl.AppendInstructions("It is 3 PM.")
	for _, want := range []string{"session.input_audio.mute", "session.input_audio.unmute", "session.instructions.append"} {
		if m := f.next(); m["type"] != want {
			t.Errorf("sent %v, want %s", m["type"], want)
		}
	}
}
