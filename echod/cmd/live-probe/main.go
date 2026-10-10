// live-probe opens a GPT-Live session without a device, speaks a question into it with a Wyoming voice
// as if it were the microphone, and prints every kind of event the server sends, with the first of each.
// The published guides leave some events unnamed (interruption, function calls); this is how to see them.
//
//	live-probe -key-file keyfile -piper 192.168.1.20:10200 -say "What is two plus two?"
//	live-probe ... -scenario tool        # the backend is given a clock to ask for
//	live-probe ... -scenario interrupt   # a second question is asked while the model is answering
//
// The model's voice is saved to -out as a WAV file.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/lib/live"
	"github.com/HuskerMinion/techo5/echod/internal/lib/realtime"
	"github.com/HuskerMinion/techo5/echod/internal/lib/wyoming"
)

func main() {
	keyFile := flag.String("key-file", "", "file holding the OpenAI API key")
	model := flag.String("model", live.DefaultModel, "live model")
	backend := flag.String("backend", live.DefaultBackend, "the model the voice hands its thinking to")
	voice := flag.String("voice", "marin", "the model's voice")
	piper := flag.String("piper", "", "Wyoming TTS host:port, for the question")
	askVoice := flag.String("ask-voice", "", "Wyoming voice for the question (empty: the server's default)")
	say := flag.String("say", "What is two plus two? Then tell me a short fact about the moon.", "the question")
	after := flag.Duration("interrupt-after", 1500*time.Millisecond, "for -scenario interrupt: how long after the model starts speaking")
	again := flag.String("interrupt-with", "Stop. Tell me about Mars instead.", "the second question, for -scenario interrupt")
	scenario := flag.String("scenario", "basic", "basic, tool or interrupt")
	out := flag.String("out", "live-reply.wav", "where the model's voice is saved")
	wait := flag.Duration("wait", 25*time.Second, "how long to listen after the question")
	flag.Parse()

	key, err := os.ReadFile(*keyFile)
	check(err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ask := func(text string) []byte {
		q, f, err := wyoming.Synthesize(ctx, *piper, text, *askVoice)
		check(err)
		return realtime.To24k(realtime.Resample(q, f.Rate, 16000))
	}
	switch *scenario {
	case "tool":
		*say = "What time is it?"
	case "interrupt":
		*say = "Tell me a long story about a ship that sails to the moon. Take your time."
	}
	question := ask(*say)
	fmt.Printf("question: %q, %.1f s\n", *say, float64(len(question))/2/live.SampleRate)

	cfg := live.Config{Key: strings.TrimSpace(string(key)), Model: *model, Backend: *backend, Voice: *voice,
		Instructions: "You are a ship's computer. Answer in short spoken sentences."}
	if *scenario == "tool" {
		cfg.Tools = []live.Tool{{Name: "get_time", Description: "The current time of day.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}
	}
	t0 := time.Now()
	c, err := live.Dial(ctx, cfg)
	check(err)
	defer func() {
		c.Close()
		drain(c, 3*time.Second)
	}()
	fmt.Printf("session started after %d ms\n", time.Since(t0).Milliseconds())

	var mu sync.Mutex
	var voicePCM []byte
	seen := map[string]int{}
	first := map[string]string{}
	var spoke time.Time
	var firstAudio time.Time
	var audioN int
	var lastLog time.Time
	var speechStarted = make(chan struct{})
	var speechOnce sync.Once

	go func() {
		for e := range c.Events {
			mu.Lock()
			seen[e.Type]++
			if _, ok := first[e.Type]; !ok {
				first[e.Type] = redact(e.Raw)
				fmt.Printf("[%5d ms] new event: %s\n", time.Since(t0).Milliseconds(), e.Type)
			}
			if b, ok := e.Audio(); ok {
				if firstAudio.IsZero() {
					firstAudio = time.Now()
				}
				voicePCM = append(voicePCM, b...)
				audioN++
				// How the voice arrives: wall time against the length of audio received so far. A voice
				// paced in real time keeps these level; one that comes in bursts runs ahead.
				if time.Since(lastLog) > 2*time.Second {
					lastLog = time.Now()
					fmt.Printf("[%5d ms] %d audio events, %.1f s of voice received\n", time.Since(t0).Milliseconds(),
						audioN, float64(len(voicePCM))/2/live.SampleRate)
				}
			}
			if e.Type == "session.output_transcript.delta" {
				fmt.Printf("[%5d ms] model says: %q\n", time.Since(t0).Milliseconds(), e.Delta)
				speechOnce.Do(func() { close(speechStarted) })
			}
			if e.Type == "session.input_transcript.delta" {
				fmt.Printf("[%5d ms] heard: %q\n", time.Since(t0).Milliseconds(), e.Delta)
			}
			if e.Inner != nil {
				k := e.Type + " > " + e.Inner.Type
				seen[k]++
				if _, ok := first[k]; !ok {
					first[k] = redact(e.Raw)
				}
			}
			mu.Unlock()
			if call, ok := e.FunctionCall(); ok && *scenario == "tool" {
				fmt.Printf("function call: %s(%s)\n", call.Name, call.Arguments)
				if err := c.ToolResult(call, time.Now().Format("3:04 PM")); err != nil {
					fmt.Println("could not send the result:", err)
				}
			}
			if e.Error != nil {
				fmt.Printf("ERROR %s: %s (client event %s)\n", e.Error.Code, e.Error.Message, e.Error.ClientEventID)
			}
		}
	}()

	stream := func(pcm []byte) {
		const frame = live.SampleRate / 50 * 2 // 20 ms
		for i := 0; i < len(pcm); i += frame {
			end := min(i+frame, len(pcm))
			check(c.Audio(pcm[i:end]))
			time.Sleep(20 * time.Millisecond)
		}
		// Silence, so the server can hear that the question is over.
		silence := make([]byte, frame)
		for range 40 {
			check(c.Audio(silence))
			time.Sleep(20 * time.Millisecond)
		}
	}
	stream(question)
	spoke = time.Now()
	if *scenario == "interrupt" {
		select { // the model is part way through its answer
		case <-speechStarted:
		case <-time.After(20 * time.Second):
		}
		time.Sleep(*after)
		fmt.Printf("[%5d ms] interrupting\n", time.Since(t0).Milliseconds())
		stream(ask(*again))
	}
	time.Sleep(*wait)

	mu.Lock()
	defer mu.Unlock()
	if !firstAudio.IsZero() {
		fmt.Printf("\nfirst audio %d ms after the question was sent\n", firstAudio.Sub(spoke).Milliseconds())
	}
	fmt.Printf("voice: %.1f s saved to %s\n", float64(len(voicePCM))/2/live.SampleRate, *out)
	check(writeWAV(*out, voicePCM, live.SampleRate))
	fmt.Println("\nevents seen:")
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-60s x%d\n", n, seen[n])
	}
	fmt.Println("\nfirst of each:")
	for _, n := range names {
		if f, ok := first[n]; ok {
			fmt.Printf("  %s\n    %s\n", n, f)
		}
	}
}

// redact shortens audio so a line stays readable.
func redact(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return string(raw)
	}
	for k, v := range m {
		if s, ok := v.(string); ok && len(s) > 80 && (k == "delta" || k == "audio") {
			m[k] = fmt.Sprintf("<%d characters>", len(s))
		}
	}
	b, _ := json.Marshal(m)
	if len(b) > 700 {
		return string(b[:700]) + "..."
	}
	return string(b)
}

func drain(c *live.Client, d time.Duration) { time.Sleep(d) }

func writeWAV(path string, pcm []byte, rate int) error {
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return os.WriteFile(path, append(h, pcm...), 0o644)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
