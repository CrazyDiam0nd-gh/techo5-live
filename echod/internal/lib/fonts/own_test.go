package fonts

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// ownDir points the fonts folder at a fresh one for a test, and back after.
func ownDir(t *testing.T) string {
	t.Helper()
	old := Dir
	Dir = filepath.Join(t.TempDir(), "fonts")
	Rescan()
	t.Cleanup(func() { Dir = old; Rescan() })
	return Dir
}

func bundled(t *testing.T, name string) []byte {
	t.Helper()
	b, err := files.ReadFile("files/" + name + "/Regular.ttf")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dotlessJ is a character Go has no glyph for and Open Sans and Comic Neue both do: it can only come
// from a font of one's own while Go is the screen's font.
const dotlessJ = 'ȷ'

// A missing folder is no fonts, and Go stays Go's face alone.
func TestNoFolderNoFonts(t *testing.T) {
	ownDir(t)
	if len(Own()) != 0 {
		t.Error("fonts listed with no folder")
	}
	if f, _ := Face(Go, false, 30); f == nil {
		t.Fatal("no face")
	} else if _, ok := f.(*opentype.Face); !ok {
		t.Errorf("Go with no fonts of one's own is a %T", f)
	}
}

// The folder's fonts are tried in file-name order, after the chosen font and Go, for what those lack;
// what is not a font file is passed over, and a file that is not a font is listed with why.
func TestOwnFontsInNameOrder(t *testing.T) {
	dir := ownDir(t)
	open, comic := bundled(t, "opensans"), bundled(t, "comicneue")
	write(t, dir, "b-comic.ttf", comic)
	write(t, dir, "a-open.ttf", open)
	write(t, dir, "notes.txt", []byte("not a font"))
	write(t, dir, "broken.otf", []byte("not a font either"))
	before := Generation()
	Rescan()
	if Generation() == before {
		t.Error("new fonts did not change the generation")
	}

	own := Own()
	if len(own) != 3 || own[0].File != "a-open.ttf" || own[1].File != "b-comic.ttf" || own[2].File != "broken.otf" {
		t.Fatalf("listed %+v", own)
	}
	if own[0].Name != "Open Sans Regular" || own[0].Problem != "" || own[2].Problem == "" {
		t.Errorf("listed %+v", own)
	}

	const size = 30
	advOf := func(b []byte) (w [2]int) {
		f, _ := opentype.Parse(b)
		fc, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		a, _ := fc.GlyphAdvance(dotlessJ)
		b2, _, _ := fc.GlyphBounds(dotlessJ)
		return [2]int{a.Round(), b2.Max.X.Round() - b2.Min.X.Round()}
	}
	if advOf(open) == advOf(comic) {
		t.Fatal("Open Sans and Comic Neue draw the test character alike; pick another")
	}
	// Go has no such character, so it comes from the folder. A font on offer keeps the folder's fonts
	// after itself and Go too.
	if f, _ := Face("inter", false, size); f == nil || len(f.(*Stack).layers) != 4 {
		t.Error("Inter's face does not have the folder's two fonts after Inter and Go")
	}
	chosen := []string{Go}
	check := func(want []byte, what string) {
		t.Helper()
		for _, v := range chosen {
			face, err := Face(v, false, size)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := face.GlyphAdvance(dotlessJ)
			b, _, _ := face.GlyphBounds(dotlessJ)
			if got := [2]int{a.Round(), b.Max.X.Round() - b.Min.X.Round()}; got != advOf(want) {
				t.Errorf("with %q chosen, %c was not drawn from %s", v, dotlessJ, what)
			}
			// The chosen font's own letters are still its own: Go's for Go.
			goFace, _ := Face(Go, false, size)
			if v == Go && adv(face, 'W') != adv(goFace, 'W') {
				t.Error("W was not Go's")
			}
		}
	}
	check(open, "the first file by name")

	// Renamed so that Comic Neue comes first, it is the one drawn from.
	if err := os.Rename(filepath.Join(dir, "b-comic.ttf"), filepath.Join(dir, "0-comic.ttf")); err != nil {
		t.Fatal(err)
	}
	Rescan()
	check(comic, "the new first file")
}

// The setup page's upload keeps a font under a safe name, replaces one of the same name, and refuses
// what is not a font, a name it cannot keep, and anything too big; nothing refused is left behind.
func TestAddAndRemoveOwn(t *testing.T) {
	dir := ownDir(t)
	if err := AddOwn("My Font.ttf", bytes.NewReader(bundled(t, "opensans"))); err != nil {
		t.Fatal(err)
	}
	if own := Own(); len(own) != 1 || own[0].File != "My Font.ttf" || own[0].Problem != "" {
		t.Fatalf("listed %+v", own)
	}
	gen := Generation()
	if err := AddOwn("My Font.ttf", bytes.NewReader(bundled(t, "comicneue"))); err != nil {
		t.Fatal(err)
	}
	if own := Own(); len(own) != 1 || own[0].Name != "Comic Neue Regular" || Generation() == gen {
		t.Errorf("replacing a font left %+v", own)
	}

	for _, bad := range []struct {
		name string
		body io.Reader
	}{
		{"junk.ttf", strings.NewReader("this is not a font")},
		{"font.exe", bytes.NewReader(bundled(t, "opensans"))},
		{"../escape.ttf", strings.NewReader("x")},
		{"héllo.ttf", bytes.NewReader(bundled(t, "opensans"))},
		{"huge.ttf", io.LimitReader(zeros{}, MaxOwnFile+10)},
	} {
		if err := AddOwn(bad.name, bad.body); err == nil {
			t.Errorf("%s was kept", bad.name)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the folder holds %d files after the refusals", len(entries))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.ttf")); err == nil {
		t.Error("a name wrote outside the folder")
	}

	if err := RemoveOwn("no such.ttf"); err == nil {
		t.Error("removed a font that is not there")
	}
	if err := RemoveOwn("../fonts/My Font.ttf"); err == nil {
		t.Error("removed by a path")
	}
	if err := RemoveOwn("My Font.ttf"); err != nil {
		t.Fatal(err)
	}
	if len(Own()) != 0 {
		t.Error("the font is still listed")
	}
}

// Over the number of files the folder takes, the rest are listed but not used.
func TestOwnFontsLimit(t *testing.T) {
	dir := ownDir(t)
	b := bundled(t, "comicneue")
	for i := range MaxOwnFiles + 1 {
		write(t, dir, string(rune('a'+i))+".ttf", b)
	}
	Rescan()
	own := Own()
	if len(own) != MaxOwnFiles+1 || own[MaxOwnFiles].Problem == "" || own[0].Problem != "" {
		t.Errorf("listed %+v", own)
	}
	if err := AddOwn("z.ttf", bytes.NewReader(b)); err == nil {
		t.Error("a font past the limit was taken")
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
