// realtime-probe measures the realtime path without a device: a Wyoming voice speaks a question into
// the Realtime API as if it were the microphone, the text that comes back is cut into sentences and
// each is synthesized, and every step is timed from the end of the question.
//
//	realtime-probe -key-file keyfile -piper 192.168.1.20:10200 -voice en_US-example-medium \
//	    -say "What is the status of the ship?" -prompt-file prompt.txt
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/lib/realtime"
	"github.com/HuskerMinion/techo5/echod/internal/lib/wyoming"
)

func main() {
	keyFile := flag.String("key-file", "", "file holding the OpenAI API key")
	model := flag.String("model", "gpt-realtime-2.1-mini", "realtime model")
	piper := flag.String("piper", "", "Wyoming TTS host:port")
	voice := flag.String("voice", "en_US-example-medium", "voice for the reply")
	askVoice := flag.String("ask-voice", "", "voice for the question (default: same as -voice)")
	say := flag.String("say", "What is the status of the ship?", "the question")
	promptFile := flag.String("prompt-file", "", "file with the instructions")
	vad := flag.String("vad", "semantic_vad", "semantic_vad or server_vad")
	flag.Parse()

	key, err := os.ReadFile(*keyFile)
	check(err)
	instr := "Reply in short, speakable sentences. No markdown."
	if *promptFile != "" {
		b, err := os.ReadFile(*promptFile)
		check(err)
		instr = string(b)
	}
	if *askVoice == "" {
		*askVoice = *voice
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	q, f, err := wyoming.Synthesize(ctx, *piper, *say, *askVoice)
	check(err)
	q16 := realtime.Resample(q, f.Rate, 16000)
	fmt.Printf("question: %q, %.1f s of audio\n", *say, float64(len(q16))/16000)

	cfg := realtime.Config{Key: strings.TrimSpace(string(key)), Model: *model, Instructions: instr}
	cfg.VAD = map[string]any{"type": *vad}
	t0 := time.Now()
	c, err := realtime.Dial(ctx, cfg)
	check(err)
	defer c.Close()

	// Wait for the session to be configured before talking.
	for e := range c.Events {
		if e.Type == "session.updated" {
			break
		}
		if e.Error != nil {
			check(fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message))
		}
	}
	fmt.Printf("session ready after %d ms\n", time.Since(t0).Milliseconds())

	// The microphone: 20 ms frames at real time, then silence for the server to hear the end.
	var endOfAudio time.Time
	var mu sync.Mutex
	since := func() int64 {
		mu.Lock()
		defer mu.Unlock()
		if endOfAudio.IsZero() {
			return -1
		}
		return time.Since(endOfAudio).Milliseconds()
	}
	go func() {
		pad := append(append([]int16{}, q16...), make([]int16, 16000*2)...)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for i := 0; i+320 <= len(pad); i += 320 {
			<-tick.C
			if err := c.Audio(realtime.To24k(pad[i : i+320])); err != nil {
				return
			}
			if i+320 >= len(q16) {
				mu.Lock()
				if endOfAudio.IsZero() {
					endOfAudio = time.Now()
				}
				mu.Unlock()
			}
		}
	}()

	speak := make(chan string, 16)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		n := 0
		for text := range speak {
			ts := time.Now()
			pcm, pf, err := wyoming.Synthesize(ctx, *piper, text, *voice)
			if err != nil {
				fmt.Println("tts error:", err)
				continue
			}
			n++
			fmt.Printf("%6d ms  sentence %d SPOKEN-READY (synth %d ms, %.1f s audio): %q\n",
				since(), n, time.Since(ts).Milliseconds(), float64(len(pcm))/float64(pf.Rate), text)
		}
	}()

	split := realtime.NewSentences()
	first := true
loop:
	for e := range c.Events {
		switch e.Type {
		case "input_audio_buffer.speech_started":
			fmt.Printf("%6d ms  speech started\n", since())
		case "input_audio_buffer.speech_stopped":
			fmt.Printf("%6d ms  speech stopped (server heard the end)\n", since())
		case "response.output_text.delta":
			if first {
				fmt.Printf("%6d ms  first text delta\n", since())
				first = false
			}
			for _, s := range split.Push(e.Delta) {
				speak <- s
			}
		case "response.done":
			fmt.Printf("%6d ms  response done (%s)\n", since(), e.Response.Status)
			for _, s := range split.Flush() {
				speak <- s
			}
			break loop
		case "error":
			fmt.Printf("error: %s\n", e.Raw)
			break loop
		}
	}
	close(speak)
	wg.Wait()
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
