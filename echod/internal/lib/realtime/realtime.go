// Package realtime is a small client for OpenAI's Realtime API over a WebSocket, used in text-output
// mode: audio goes up, the model hears it with its own voice activity detection, and text comes back
// as it is written. Speaking the text is somebody else's job (Wyoming), which is the point: the
// device keeps its own voice, and gets turn taking and interruption from the model's side.
package realtime

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

// URL is the Realtime endpoint; the model is a query parameter.
const URL = "wss://api.openai.com/v1/realtime"

// SampleRate is what the model takes as audio/pcm: 16-bit little-endian mono at 24 kHz.
const SampleRate = 24000

// Tool is a function the model may call.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Config is what a session is opened with.
type Config struct {
	Key          string
	Model        string
	Instructions string
	Tools        []Tool
	// VAD is the turn_detection object. Nil means semantic_vad with the defaults.
	VAD map[string]any
	// URL overrides the endpoint, for tests.
	URL string
}

// Event is one server event, with the fields a voice turn reads picked out. Raw is the whole event.
type Event struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`

	Delta      string `json:"delta"`
	ItemID     string `json:"item_id"`
	Transcript string `json:"transcript"`
	AudioMs    int    `json:"audio_start_ms"`
	AudioEndMs int    `json:"audio_end_ms"`

	Response *Response `json:"response"`
	Error    *struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Response is the part of response.done that matters: how it ended and what it produced.
type Response struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Output []struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	} `json:"output"`
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

// Dial opens a session and configures it.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Key == "" {
		return nil, errors.New("realtime: no API key")
	}
	url := cfg.URL
	if url == "" {
		url = fmt.Sprintf("%s?model=%s", URL, cfg.Model)
	}
	hdr := http.Header{"Authorization": {"Bearer " + cfg.Key}}
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.DialContext(ctx, url, hdr)
	if err != nil {
		return nil, fmt.Errorf("realtime: %w", err)
	}
	c := &Client{ws: ws, Events: make(chan Event, 256)}
	go c.read()

	vad := cfg.VAD
	if vad == nil {
		vad = map[string]any{"type": "semantic_vad"}
	}
	var tools []map[string]any
	for _, t := range cfg.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": t.Name,
			"description": t.Description, "parameters": t.Parameters})
	}
	session := map[string]any{
		"type":              "realtime",
		"model":             cfg.Model,
		"output_modalities": []string{"text"},
		"instructions":      cfg.Instructions,
		"audio": map[string]any{"input": map[string]any{
			"format":         map[string]any{"type": "audio/pcm", "rate": SampleRate},
			"turn_detection": vad,
		}},
	}
	if len(tools) > 0 {
		session["tools"] = tools
		session["tool_choice"] = "auto"
	}
	if err := c.Send(map[string]any{"type": "session.update", "session": session}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
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

// Audio appends 24 kHz 16-bit little-endian mono PCM to the input buffer.
func (c *Client) Audio(pcm24 []byte) error {
	return c.Send(map[string]any{"type": "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(pcm24)})
}

// Cancel stops the response being generated, if any.
func (c *Client) Cancel() error { return c.Send(map[string]any{"type": "response.cancel"}) }

// ToolResult gives the model what a function call returned, and asks it to carry on.
func (c *Client) ToolResult(callID, output string) error {
	if err := c.Send(map[string]any{"type": "conversation.item.create", "item": map[string]any{
		"type": "function_call_output", "call_id": callID, "output": output}}); err != nil {
		return err
	}
	return c.Send(map[string]any{"type": "response.create"})
}

// Close ends the session.
func (c *Client) Close() { _ = c.ws.Close() }

// To24k converts 16 kHz 16-bit mono samples to 24 kHz by linear interpolation: three output samples
// for every two input ones. It is for speech recognition, where that is plenty.
func To24k(in []int16) []byte {
	n := len(in) * 3 / 2
	out := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		pos := float64(i) * 2 / 3
		j := int(pos)
		frac := pos - float64(j)
		a := float64(in[j])
		b := a
		if j+1 < len(in) {
			b = float64(in[j+1])
		}
		s := int16(a + (b-a)*frac)
		out = append(out, byte(s), byte(s>>8))
	}
	return out
}

// Resample converts samples at any rate to another by linear interpolation.
func Resample(in []int16, from, to int) []int16 {
	if from == to || len(in) == 0 {
		return in
	}
	n := len(in) * to / from
	out := make([]int16, n)
	for i := range out {
		pos := float64(i) * float64(from) / float64(to)
		j := int(pos)
		frac := pos - float64(j)
		a := float64(in[j])
		b := a
		if j+1 < len(in) {
			b = float64(in[j+1])
		}
		out[i] = int16(a + (b-a)*frac)
	}
	return out
}

// Sentences splits streamed text into speakable pieces. Push text as it arrives; each call returns
// the pieces that are complete. Flush returns what is left at the end of the response.
type Sentences struct {
	buf   []rune
	first bool
}

// NewSentences starts a splitter. The first piece of a reply may be cut at a comma once it is long
// enough, so speech starts sooner.
func NewSentences() *Sentences { return &Sentences{first: true} }

func (s *Sentences) Push(text string) []string {
	s.buf = append(s.buf, []rune(text)...)
	var out []string
	for {
		cut := s.cut()
		if cut < 0 {
			return out
		}
		piece := string(s.buf[:cut])
		s.buf = s.buf[cut:]
		s.first = false
		if p := trim(piece); p != "" {
			out = append(out, p)
		}
	}
}

// minPiece is the shortest sentence spoken on its own, in characters; shorter ones wait for the next.
const minPiece = 8

// cut is where the first complete piece ends, or -1. A full stop counts only when a space follows,
// so "3.5" and "e.g." mid-number are not ends; a ! or ? counts at once.
func (s *Sentences) cut() int {
	for i, r := range s.buf {
		end := r == '!' || r == '?' || r == '\n'
		if r == '.' && i+1 < len(s.buf) && (s.buf[i+1] == ' ' || s.buf[i+1] == '\n') {
			end = true
		}
		soft := s.first && (r == ',' || r == ';' || r == ':') && i >= 40
		if !end && !soft {
			continue
		}
		if i+1 < minPiece && !soft {
			continue
		}
		return i + 1
	}
	return -1
}

// Flush returns the unfinished remainder.
func (s *Sentences) Flush() []string {
	p := trim(string(s.buf))
	s.buf = nil
	s.first = true
	if p == "" {
		return nil
	}
	return []string{p}
}

func trim(s string) string {
	b := []rune(s)
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\t') {
		j--
	}
	return string(b[i:j])
}
