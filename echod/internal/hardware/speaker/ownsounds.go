package speaker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// The sounds an owner may replace from the setup page, in the order it lists them.
var ownable = []struct {
	clip        *Clip
	label, when string
}{
	{ClipWake, "Wake word heard", "plays when the wake word is heard"},
	{ClipFailure, "Something went wrong", "plays when a request fails"},
	{ClipCanceled, "Canceled", "plays when a request is canceled"},
	{ClipTimer, "Timer finished", "plays when a timer ends"},
	{ClipMuteOn, "Microphone muted", "plays when the mute switch is turned on"},
	{ClipMuteOff, "Microphone live", "plays when the mute switch is turned off"},
}

// OwnSound is one replaceable sound as the setup page shows it.
type OwnSound struct {
	Name  string // the file name without .wav, which is also the clip's name
	Label string
	When  string
	Clip  *Clip
}

// OwnSounds lists the sounds that can be replaced.
func OwnSounds() []OwnSound {
	out := make([]OwnSound, len(ownable))
	for i, o := range ownable {
		out[i] = OwnSound{Name: o.clip.file, Label: o.label, When: o.when, Clip: o.clip}
	}
	return out
}

// OwnSoundNamed is the replaceable sound with that name.
func OwnSoundNamed(name string) (OwnSound, bool) {
	for _, s := range OwnSounds() {
		if s.Name == name {
			return s, true
		}
	}
	return OwnSound{}, false
}

// CheckOwn reports whether b is a recording the device would play, and how long it is in
// milliseconds. It is the rule the device applies to a file it finds, applied before one is kept.
func CheckOwn(b []byte) (ms int, err error) {
	if len(b) > largestOwn {
		return 0, fmt.Errorf("that file is longer than a sound needs to be")
	}
	samples, err := parseOwn(b)
	if err != nil {
		return 0, err
	}
	return len(samples) * 1000 / Rate, nil
}

// SaveOwn keeps b as the owner's recording for the clip. It is written beside its place and moved
// into it, so a sound that is playing, or the device losing power half way, never meets half a file;
// the clip notices the new file the next time it plays.
func (c *Clip) SaveOwn(b []byte) error { return c.SaveOwnFrom(b, "") }

// SaveOwnFrom is SaveOwn for a recording that was made from a file with a name worth remembering: the
// setup page shows it beside the sound, since the device keeps the recording under the sound's name.
func (c *Clip) SaveOwnFrom(b []byte, from string) error {
	if _, err := CheckOwn(b); err != nil {
		return err
	}
	if err := os.MkdirAll(SoundsDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(SoundsDir, "."+c.file+"-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once it has been renamed
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), c.ownPath()); err != nil {
		return err
	}
	c.rememberFrom(from)
	return nil
}

// The name a recording came from is kept beside it as <sound>.from, with the time of the recording it
// belongs to: a file put there by hand later has another time, and is not given the old name.
func (c *Clip) fromPath() string { return filepath.Join(SoundsDir, c.file+".from") }

func (c *Clip) rememberFrom(from string) {
	from = cleanName(from)
	info, err := os.Stat(c.ownPath())
	if from == "" || err != nil {
		_ = os.Remove(c.fromPath())
		return
	}
	// Losing the name costs nothing but the name.
	_ = os.WriteFile(c.fromPath(), []byte(strconv.FormatInt(info.ModTime().UnixNano(), 10)+"\n"+from+"\n"), 0o644)
}

// cleanName is a file name as far as it can be shown: no folders, no control characters, not long.
func cleanName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	return name
}

// OwnFrom is the name of the file the owner's recording was made from, when it was kept from the setup
// page and has not been replaced since; empty otherwise.
func (c *Clip) OwnFrom() string {
	info, err := os.Stat(c.ownPath())
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(c.fromPath())
	if err != nil {
		return ""
	}
	when, name, ok := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if !ok || when != strconv.FormatInt(info.ModTime().UnixNano(), 10) {
		return ""
	}
	return name
}

// OwnFile is the file the recording is kept in, by name only.
func (c *Clip) OwnFile() string { return c.file + ".wav" }

// RemoveOwn takes the owner's recording away, which puts the stock sound back. Having none is not
// a failure.
func (c *Clip) RemoveOwn() error {
	_ = os.Remove(c.fromPath())
	if err := os.Remove(c.ownPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Name is the clip's file name, without .wav.
func (c *Clip) Name() string { return c.file }

// HasStock reports whether there is a recording built in; the other sounds are tones until the owner
// supplies one.
func (c *Clip) HasStock() bool { return c.stockMs() > 0 }

// StockMs is how long the built-in sound plays; 0 where it is a tone.
func (c *Clip) StockMs() int { return c.stockMs() }
