package speaker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wavBytes is ms of a constant level as a WAVE file at rate.
func wavBytes(t *testing.T, rate, channels, ms int) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.wav")
	writeWAV(t, p, rate, channels, ms)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A saved recording plays in the clip's place straight away, saving again replaces it, and removing
// it puts the stock sound back.
func TestSaveReplaceAndRemoveAnOwnSound(t *testing.T) {
	dir := ownSounds(t)
	c := &Clip{file: "mute_switch_on"}
	stock := c.Ms()

	if err := c.SaveOwn(wavBytes(t, Rate, 1, 300)); err != nil {
		t.Fatal(err)
	}
	if !c.Own() || c.Ms() != 300 {
		t.Fatalf("after saving: own %v, %d ms", c.Own(), c.Ms())
	}
	if err := c.SaveOwn(wavBytes(t, Rate, 1, 500)); err != nil {
		t.Fatal(err)
	}
	if c.Ms() != 500 {
		t.Fatalf("after replacing: %d ms, want 500", c.Ms())
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".*"))
	if len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
	if err := c.RemoveOwn(); err != nil {
		t.Fatal(err)
	}
	if c.Own() || c.Ms() != stock {
		t.Errorf("after removing: own %v, %d ms, want the stock %d", c.Own(), c.Ms(), stock)
	}
	if err := c.RemoveOwn(); err != nil {
		t.Errorf("removing what is not there is not a failure: %v", err)
	}
}

// What the device would not play is not kept, and the recording already there is left alone.
func TestAnUnplayableUploadIsRefusedAndKeepsWhatWasThere(t *testing.T) {
	dir := ownSounds(t)
	c := &Clip{file: "timer_finished"}
	if err := c.SaveOwn(wavBytes(t, Rate, 1, 200)); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]byte{
		"wrong rate":    wavBytes(t, 44100, 1, 200),
		"too long":      wavBytes(t, Rate, 1, 10500),
		"not a wave":    []byte(strings.Repeat("ID3 this is an mp3 ", 100)),
		"empty":         nil,
		"header only":   wavBytes(t, Rate, 1, 0),
		"truncated wav": wavBytes(t, Rate, 1, 200)[:20],
	}
	for name, b := range bad {
		if err := c.SaveOwn(b); err == nil {
			t.Errorf("%s: was kept", name)
		}
	}
	if c.Ms() != 200 {
		t.Errorf("the recording that was there now lasts %d ms, want 200", c.Ms())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want just the one recording", len(entries))
	}
}

func TestSavingMakesTheFolderAndReadsBackStereo(t *testing.T) {
	dir := ownSounds(t)
	SoundsDir = filepath.Join(dir, "not", "yet")
	c := &Clip{file: "canceled"}
	if err := c.SaveOwn(wavBytes(t, Rate, 2, 250)); err != nil {
		t.Fatal(err)
	}
	if c.Ms() != 250 {
		t.Errorf("lasts %d ms", c.Ms())
	}
	info, err := os.Stat(filepath.Join(SoundsDir, "canceled.wav"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("stat %v, %v", info, err)
	}
}

func TestOwnSoundsAreTheKnownClips(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range OwnSounds() {
		if s.Name == "" || s.Label == "" || s.Clip == nil || s.Clip.Name() != s.Name {
			t.Errorf("bad entry %+v", s)
		}
		if seen[s.Name] {
			t.Errorf("%s listed twice", s.Name)
		}
		seen[s.Name] = true
		if got, ok := OwnSoundNamed(s.Name); !ok || got.Clip != s.Clip {
			t.Errorf("%s is not found by name", s.Name)
		}
	}
	for _, name := range []string{"", "../x", "wake_word_triggered.wav", "nope"} {
		if _, ok := OwnSoundNamed(name); ok {
			t.Errorf("%q should not be a sound", name)
		}
	}
	if !ClipWake.HasStock() || ClipFailure.HasStock() {
		t.Error("the wake sound has a stock recording and the failure sound is a tone")
	}
}
