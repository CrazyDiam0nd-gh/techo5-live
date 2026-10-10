package mic

import (
	"math"
	"testing"
)

// playback is a frame of "speech": a tone with the given RMS.
func echoTone(level float64, phase float64) []int16 {
	f := make([]int16, FrameSamples)
	for i := range f {
		f[i] = int16(level * math.Sqrt2 * math.Sin(phase+float64(i)*0.3))
	}
	return f
}

func addFrames(a, b []int16) []int16 {
	out := make([]int16, len(a))
	for i := range out {
		out[i] = a[i] + b[i]
	}
	return out
}

// words is a playback envelope: loud words with short gaps, as counting is.
func words(i int) float64 {
	if (i/8)%2 == 0 {
		return 9000
	}
	return 80
}

// The echo of a loud word is far louder than a person nearby, and is still not them.
func TestMeterEchoIsNotSpeech(t *testing.T) {
	var m echoMeter
	for i := 0; i < 300; i++ {
		ref := echoTone(words(i), 0)
		echo := echoTone(words(i)*0.04, 1) // 28 dB down on the playback
		if m.feed(ref, echo) {
			t.Fatalf("echo taken for speech at frame %d", i)
		}
	}
}

// Someone talking is heard in the gaps between words, though the echo of the words is louder than they are.
func TestMeterHearsSpeechInTheGaps(t *testing.T) {
	var m echoMeter
	for i := 0; i < 100; i++ {
		m.feed(echoTone(words(i), 0), echoTone(words(i)*0.04, 1))
	}
	heard := 0
	for i := 100; i < 200; i++ {
		ref := echoTone(words(i), 0)
		out := addFrames(echoTone(words(i)*0.04, 1), echoTone(250, 2)) // a person at 250 RMS, under the loud echo
		if m.feed(ref, out) {
			heard++
		}
	}
	if heard < 10 {
		t.Errorf("heard speech in %d of 100 frames", heard)
	}
}

// Nothing playing: only the floor matters.
func TestMeterWithNoPlayback(t *testing.T) {
	var m echoMeter
	for i := 0; i < meterLearn+5; i++ {
		m.feed(make([]int16, FrameSamples), echoTone(10, 0))
	}
	if m.feed(make([]int16, FrameSamples), echoTone(10, 0)) {
		t.Error("room noise taken for speech")
	}
	if !m.feed(make([]int16, FrameSamples), echoTone(400, 0)) {
		t.Error("speech not heard in a silent reply")
	}
}

func TestMeterResets(t *testing.T) {
	var m echoMeter
	for i := 0; i < 50; i++ {
		m.feed(echoTone(5000, 0), echoTone(200, 1))
	}
	m.reset()
	if m.k != 0 || m.frames != 0 {
		t.Error("not reset")
	}
}
