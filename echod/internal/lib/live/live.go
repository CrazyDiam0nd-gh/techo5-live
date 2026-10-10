// Package live speaks OpenAI's Live API (gpt-live-1): a full-duplex voice model that listens while it
// talks and answers in its own voice. One WebSocket carries the microphone up, the model's voice and
// transcripts down, and the tool calls of the backend model it hands the thinking to.
//
// It is written from the published guides, which name the session and audio events but not every
// event (interruption and the function-call items are only described), so every event keeps its raw
// JSON, and cmd/live-probe prints what a real session sends.
package live

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// URL is the Live endpoint.
const URL = "wss://api.openai.com/v1/live/sessions"

// SampleRate is the audio rate used both ways: 16-bit little-endian mono PCM at 24 kHz.
const SampleRate = 24000

// DefaultModel is the voice model, and DefaultBackend the model it delegates reasoning and tools to.
const (
	DefaultModel   = "gpt-live-1"
	DefaultBackend = "gpt-5.6-luna"
	DefaultVoice   = "marin"
)

// Voices are the names the page offers. OpenAI's guide names only marin; the rest are those reported for
// the model and are not confirmed, so the page takes any name and the server says if it does not know it.
var Voices = []string{"marin", "cedar", "alloy", "ash", "ballad", "coral", "echo", "sage", "shimmer", "verse",
	"juniper", "breeze", "vale", "ember"}

// Tool is a function the backend model may call; the application runs it.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Config is what a session starts with. The model, instructions, audio format, voice and delegation
// are fixed once it has started.
type Config struct {
	Key          string
	Model        string
	Backend      string
	Voice        string
	Instructions string
	Tools        []Tool
	// URL overrides the endpoint, for tests.
	URL string
}

// Event is one server event with the fields a voice turn reads picked out. Raw is all of it.
type Event struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`

	// Delta is base64 audio in session.output_audio.delta, and text in the transcript deltas.
	Delta string `json:"delta"`
	// StartMs and EndMs place a transcript delta on the session's clock, which is the clock of the audio
	// sent and received: a word of the person's that starts after the model began to speak is talking over it.
	StartMs      int    `json:"start_ms"`
	EndMs        int    `json:"end_ms"`
	DelegationID string `json:"delegation_id"`
	Reason       string `json:"reason"`

	Session *struct {
		ID string `json:"id"`
	} `json:"session"`
	// Inner is the backend's event, inside a response.event envelope.
	Inner *Inner `json:"event"`
	Error *struct {
		Type          string `json:"type"`
		Code          string `json:"code"`
		Message       string `json:"message"`
		ClientEventID string `json:"client_event_id"`
	} `json:"error"`
}

// Inner is an event of the backend model, carried in a response.event envelope.
type Inner struct {
	Type string `json:"type"`
	Item *struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	} `json:"item"`
}

// Call is a function the backend asked the application to run.
type Call struct {
	DelegationID, CallID, Name, Arguments string
}

// FunctionCall is the call an event carries, if it carries one.
func (e Event) FunctionCall() (Call, bool) {
	if e.Type != "response.event" || e.Inner == nil || e.Inner.Type != "response.output_item.done" ||
		e.Inner.Item == nil || e.Inner.Item.Type != "function_call" {
		return Call{}, false
	}
	i := e.Inner.Item
	return Call{DelegationID: e.DelegationID, CallID: i.CallID, Name: i.Name, Arguments: i.Arguments}, true
}

// Audio is the model's voice in this event, as 24 kHz 16-bit mono PCM.
func (e Event) Audio() ([]byte, bool) {
	if e.Type != "session.output_audio.delta" || e.Delta == "" {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(e.Delta)
	return b, err == nil
}

// Client is an open session.
type Client struct {
	ws  *websocket.Conn
	wmu sync.Mutex

	// Events delivers what the server sends, and is closed when the connection ends.
	Events chan Event

	errMu sync.Mutex
	err   error
}

// StartMessage is the first message of a session.
func StartMessage(cfg Config) map[string]any {
	model := cfg.Model
	if model == "" {
		model = DefaultModel
	}
	backend := cfg.Backend
	if backend == "" {
		backend = DefaultBackend
	}
	audioOut := map[string]any{}
	if cfg.Voice != "" {
		audioOut["voice"] = cfg.Voice
	}
	responses := map[string]any{"model": backend}
	if len(cfg.Tools) > 0 {
		var tools []map[string]any
		for _, t := range cfg.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": t.Name,
				"description": t.Description, "parameters": t.Parameters})
		}
		responses["tools"] = tools
		responses["tool_choice"] = "auto"
	}
	return map[string]any{"type": "session.start", "session": map[string]any{
		"model":        model,
		"instructions": cfg.Instructions,
		"audio": map[string]any{
			"format": map[string]any{"type": "audio/pcm", "rate": SampleRate},
			"output": audioOut,
		},
		"delegation": map[string]any{"type": "responses", "responses": responses},
	}}
}

// Dial opens a session, starts it, and waits for the server to say it has started.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Key == "" {
		return nil, errors.New("live: no API key")
	}
	url := cfg.URL
	if url == "" {
		url = URL
	}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.DialContext(ctx, url, http.Header{"Authorization": {"Bearer " + cfg.Key}})
	if err != nil {
		return nil, fmt.Errorf("live: %w", err)
	}
	c := &Client{ws: ws, Events: make(chan Event, 512)}
	go c.read()
	if err := c.Send(StartMessage(cfg)); err != nil {
		c.Close()
		return nil, err
	}
	return c, c.waitStarted(ctx)
}

// waitStarted reads events until the session has started; audio sent before then is not accepted.
func (c *Client) waitStarted(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			c.Close()
			return ctx.Err()
		case e, ok := <-c.Events:
			if !ok {
				return fmt.Errorf("live: closed while starting: %v", c.Err())
			}
			if e.Error != nil {
				c.Close()
				return fmt.Errorf("live: %s: %s", e.Error.Code, e.Error.Message)
			}
			if e.Type == "session.started" {
				return nil
			}
		}
	}
}

func (c *Client) read() {
	defer close(c.Events)
	for {
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			c.errMu.Lock()
			c.err = err
			c.errMu.Unlock()
			return
		}
		var e Event
		if json.Unmarshal(msg, &e) != nil {
			continue
		}
		e.Raw = msg
		c.Events <- e
	}
}

// Err is why the connection ended, once Events has closed.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Send writes one client event.
func (c *Client) Send(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteJSON(v)
}

// Audio appends 24 kHz 16-bit little-endian mono PCM from the microphone. There is no reply to it.
func (c *Client) Audio(pcm24 []byte) error {
	return c.Send(map[string]any{"type": "session.input_audio.append",
		"audio": base64.StdEncoding.EncodeToString(pcm24)})
}

// Mute and Unmute stop and resume the microphone being heard. The model goes on with what it was saying.
func (c *Client) Mute() error   { return c.Send(map[string]any{"type": "session.input_audio.mute"}) }
func (c *Client) Unmute() error { return c.Send(map[string]any{"type": "session.input_audio.unmute"}) }

// AppendInstructions adds to what the voice model has been told, as the time moves on.
func (c *Client) AppendInstructions(text string) error {
	return c.Send(map[string]any{"type": "session.instructions.append", "instructions": text})
}

// ToolResult gives the backend what a function call returned, and asks it to go on. The server
// refuses a delegation_id on either message (the probe saw unknown_parameter), so the call is
// identified by its call_id alone.
func (c *Client) ToolResult(call Call, output string) error {
	if err := c.Send(map[string]any{"type": "response.item.create",
		"item": map[string]any{"type": "function_call_output", "call_id": call.CallID, "output": output}}); err != nil {
		return err
	}
	return c.Send(map[string]any{"type": "response.create"})
}

// Close ends the session. The server's closing event, with the final usage, is read from Events.
func (c *Client) Close() {
	_ = c.Send(map[string]any{"type": "session.close"})
	_ = c.ws.Close()
}
