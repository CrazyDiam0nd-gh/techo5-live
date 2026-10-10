package config

// Brain is where a turn's speech and answer come from when they do not come from Home Assistant: the
// direct pipeline (feature/voice/direct.go). Speech to text and text to speech are Wyoming servers
// (faster-whisper and Piper, as Home Assistant itself uses), and the answer is a chat model behind an
// OpenAI-style endpoint (llama.cpp's server, Ollama, and so on), which acts through the device's own
// abilities as tools. Empty Mode is Home Assistant's Assist pipeline, as always.
type Brain struct {
	Mode BrainMode `json:"mode,omitempty"`

	// STT and TTS are the Wyoming servers, host:port.
	STT string `json:"stt,omitempty"`
	TTS string `json:"tts,omitempty"`
	// Voice is the Piper voice, like en_US-lessac-medium; empty is the server's default.
	Voice string `json:"voice,omitempty"`
	// Language is what the speech is in, for the recognizer; empty is English.
	Language string `json:"language,omitempty"`

	// LLM is the chat endpoint's base, like http://192.168.1.20:8080/v1, and Model the model to ask
	// for (a server with one model ignores it). Key is sent as a bearer token when set; it is a
	// secret, never shown again once saved.
	LLM   string `json:"llm,omitempty"`
	Model string `json:"model,omitempty"`
	Key   string `json:"key,omitempty"`

	// Search is a SearXNG server's address, like http://192.168.1.20:8888, with its JSON format on: the
	// model looks things up there and reads the pages it finds. Empty is no looking anything up.
	Search string `json:"search,omitempty"`

	// LiveVoice, LiveModel and LiveBackend are for BrainLive: the model's own voice (marin when empty),
	// the live model (gpt-live-1) and the model it hands the thinking and the tools to (see lib/live).
	LiveVoice   string `json:"live_voice,omitempty"`
	LiveModel   string `json:"live_model,omitempty"`
	LiveBackend string `json:"live_backend,omitempty"`

	// Prompt is added to the device's own instructions to the model: a name, a tone, what the
	// household wants it to know.
	Prompt string `json:"prompt,omitempty"`
}

type BrainMode string

const (
	BrainHomeAssistant BrainMode = ""
	BrainDirect        BrainMode = "direct"

	// BrainRealtime is OpenAI's Realtime API: it hears the microphone itself, so it needs no speech to
	// text server, and its text is spoken by the text-to-speech server so the device keeps its voice.
	// Model is a realtime model here (gpt-realtime-2.1-mini when empty), Key the OpenAI key.
	BrainRealtime BrainMode = "realtime"

	// BrainLive is OpenAI's Live API (gpt-live-1): full duplex, in the model's own voice, so it needs
	// no text-to-speech server and no speech to text. Key is the OpenAI key. It bills for the time a
	// session is open, which the device keeps short.
	BrainLive BrainMode = "live"
)

// DefaultRealtimeModel is what the realtime pipeline asks for when none is set.
const DefaultRealtimeModel = "gpt-realtime-2.1-mini"

// Direct is whether turns go to the direct pipeline, and it is set up enough to run one.
func (b Brain) Direct() bool {
	return b.Mode == BrainDirect && b.STT != "" && b.TTS != "" && b.LLM != ""
}

// Realtime is whether turns go to the realtime pipeline, and it is set up enough to run one.
func (b Brain) Realtime() bool {
	return b.Mode == BrainRealtime && b.TTS != "" && b.Key != ""
}

// Live is whether turns go to the live pipeline, and it is set up enough to run one.
func (b Brain) Live() bool {
	return b.Mode == BrainLive && b.Key != ""
}

type BrainWriter struct{ st *Store }

// Set replaces everything but the key, which only SetKey changes: a form that shows the key as
// "set" and posts nothing for it must not clear it.
func (w BrainWriter) Set(b Brain) error {
	return w.st.Update(func(c *Config) {
		key := c.Brain.Key
		c.Brain = b
		c.Brain.Key = key
	})
}

// SetVoice changes the speaking voice alone ("" for the server's default), so a choice on the screen
// cannot undo a setup page save made at the same moment.
func (w BrainWriter) SetVoice(voice string) error {
	return w.st.Update(func(c *Config) { c.Brain.Voice = voice })
}

func (w BrainWriter) SetKey(key string) error {
	return w.st.Update(func(c *Config) { c.Brain.Key = key })
}
