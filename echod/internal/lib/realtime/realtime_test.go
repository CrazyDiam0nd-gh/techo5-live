package realtime

import (
	"slices"
	"testing"
)

func feed(chunks ...string) []string {
	s := NewSentences()
	var out []string
	for _, c := range chunks {
		out = append(out, s.Push(c)...)
	}
	return append(out, s.Flush()...)
}

func TestSentencesAcrossDeltas(t *testing.T) {
	got := feed("Working. Divert", "ing auxiliary power", " to sensors. Done", ".")
	want := []string{"Working.", "Diverting auxiliary power to sensors.", "Done."}
	if !slices.Equal(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSentencesKeepDecimals(t *testing.T) {
	got := feed("The warp core is at 3.5 percent. Stand by.")
	want := []string{"The warp core is at 3.5 percent.", "Stand by."}
	if !slices.Equal(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSentencesShortWaitsForMore(t *testing.T) {
	got := feed("Yes. ", "Acknowledged.")
	want := []string{"Yes. Acknowledged."}
	if !slices.Equal(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSentencesFirstPieceCutsAtComma(t *testing.T) {
	got := feed("Scanning the entire sector for life signs, none detected, standing by for orders.")
	if len(got) < 2 || got[0] != "Scanning the entire sector for life signs," {
		t.Errorf("got %q", got)
	}
}

func TestTo24kLength(t *testing.T) {
	if n := len(To24k(make([]int16, 320))); n != 480*2 {
		t.Errorf("320 samples at 16 kHz gave %d bytes, want %d", n, 480*2)
	}
	if got := Resample(make([]int16, 22050), 22050, 16000); len(got) != 16000 {
		t.Errorf("resampled to %d samples", len(got))
	}
}
