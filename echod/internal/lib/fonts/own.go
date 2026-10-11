package fonts

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/HuskerMinion/techo5/echod/internal/layout"
)

// Fonts of the owner's own (#122): font files put in the device's fonts folder, by hand or from the
// setup page, used only for characters neither the chosen font nor Go has a glyph for - Korean,
// Chinese and Japanese, say, which none of the fonts on offer draw. Never the font the screen's words
// are drawn in. Tried in the order of their file names.
//
// A font of that kind is big (5 to 20 MB), and the device has little memory, so a file is never read
// into memory whole: it is kept open and its tables are read from it as glyphs are drawn, so what it
// costs is the page cache the kernel can drop again, not the daemon's heap.

// Dir is the folder they are kept in, beside the sounds folder.
var Dir = filepath.Join(layout.StateDir, "fonts")

// Limits on what the folder takes from the setup page; a file put there by hand over these is still
// skipped, so a full folder cannot take the device's memory or its storage either.
const (
	MaxOwnFile  = 32 << 20 // one file
	MaxOwnTotal = 96 << 20 // all of them
	MaxOwnFiles = 8
)

// OwnFont is one of them as the setup page lists it: its file, its size, and the name the font gives
// itself, or why it is not used.
type OwnFont struct {
	File    string
	Size    int64
	Name    string
	Problem string
}

// ownEntry is one in use: its file, as it was when opened, kept open, and the font read from it.
type ownEntry struct {
	file string
	size int64
	mod  time.Time
	fh   *os.File
	font *sfnt.Font
}

var own struct {
	mu      sync.Mutex
	listed  []OwnFont
	inUse   []ownEntry
	mtime   time.Time // the folder's, when last read
	checked time.Time // when the folder was last looked at
}

// gen counts the changes to the fonts in use, so a screen can tell when to make its faces again.
var gen atomic.Uint64

// Generation is a number that changes whenever the fonts of the owner's own do.
func Generation() uint64 { return gen.Load() }

// refreshEvery is how often Refresh looks at the folder for a change made by hand.
const refreshEvery = 10 * time.Second

// Refresh reads the folder again if it has changed since it was last read (a file added or removed
// over SSH), looking at most every refreshEvery. Cheap enough to call on every frame.
func Refresh() {
	own.mu.Lock()
	defer own.mu.Unlock()
	now := time.Now()
	if !own.checked.IsZero() && now.Sub(own.checked) < refreshEvery {
		return
	}
	own.checked = now
	var mtime time.Time
	if st, err := os.Stat(Dir); err == nil {
		mtime = st.ModTime()
	}
	if !mtime.Equal(own.mtime) || own.listed == nil {
		own.mtime = mtime
		load()
	}
}

// Rescan reads the folder again now.
func Rescan() {
	own.mu.Lock()
	defer own.mu.Unlock()
	own.checked = time.Now()
	if st, err := os.Stat(Dir); err == nil {
		own.mtime = st.ModTime()
	} else {
		own.mtime = time.Time{}
	}
	load()
}

// Own is the folder's font files, in the order they are tried, each with its name or what is wrong
// with it.
func Own() []OwnFont {
	Refresh()
	own.mu.Lock()
	defer own.mu.Unlock()
	return slices.Clone(own.listed)
}

// ownFonts is the fonts in use, in order. Called under no lock.
func ownFonts() []*sfnt.Font {
	own.mu.Lock()
	defer own.mu.Unlock()
	out := make([]*sfnt.Font, len(own.inUse))
	for i, e := range own.inUse {
		out[i] = e.font
	}
	return out
}

// fontFile is whether name is a font file by its name.
func fontFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ttf", ".otf", ".ttc":
		return !strings.HasPrefix(name, ".")
	}
	return false
}

// load reads the folder: each font file in name order, opened and checked, within the limits. The
// files of the fonts in use before are left to be closed when nothing draws with them any more (an
// os.File closes itself once unreachable), since a face made a moment ago may still be drawing a
// frame. Called under own.mu.
func load() {
	listed := []OwnFont{}
	var inUse []ownEntry
	entries, err := os.ReadDir(Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("fonts: the fonts folder could not be read", "dir", Dir, "err", err)
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() || !fontFile(e.Name()) {
			continue
		}
		o := OwnFont{File: e.Name()}
		var mod time.Time
		if info, err := e.Info(); err == nil {
			o.Size, mod = info.Size(), info.ModTime()
		}
		switch {
		case len(inUse) >= MaxOwnFiles:
			o.Problem = fmt.Sprintf("only the first %d fonts are used", MaxOwnFiles)
		case o.Size > MaxOwnFile:
			o.Problem = fmt.Sprintf("bigger than %d MB", MaxOwnFile>>20)
		case total+o.Size > MaxOwnTotal:
			o.Problem = fmt.Sprintf("the fonts together would be over %d MB", MaxOwnTotal>>20)
		default:
			fh, f, err := openFont(filepath.Join(Dir, e.Name()))
			if err != nil {
				o.Problem = err.Error()
				break
			}
			o.Name = fontName(f)
			total += o.Size
			inUse = append(inUse, ownEntry{file: e.Name(), size: o.Size, mod: mod, fh: fh, font: f})
		}
		if o.Problem != "" {
			slog.Warn("fonts: a font of your own is not used", "file", o.File, "why", o.Problem)
		}
		listed = append(listed, o)
	}
	changed := len(inUse) != len(own.inUse)
	for i := 0; !changed && i < len(inUse); i++ {
		a, b := inUse[i], own.inUse[i]
		changed = changed || a.file != b.file || a.size != b.size || !a.mod.Equal(b.mod)
	}
	if !changed {
		// The same files, unchanged: keep the ones open already, and close the new copies.
		for _, e := range inUse {
			e.fh.Close()
		}
		inUse = own.inUse
	}
	own.listed, own.inUse = listed, inUse
	if changed {
		gen.Add(1)
		slog.Info("fonts: fonts of your own", "in use", len(inUse))
	}
}

// openFont opens a font file and checks it is one the screen can draw from: it reads, has glyphs, and
// a glyph's outline can be read. A collection (.ttc) is its first font. The file is kept open, as the
// font reads from it.
func openFont(path string) (*os.File, *sfnt.Font, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, nil, errors.New("it could not be opened")
	}
	f, err := parseFont(fh)
	if err == nil {
		err = checkFont(f)
	}
	if err != nil {
		fh.Close()
		return nil, nil, err
	}
	return fh, f, nil
}

func parseFont(r io.ReaderAt) (*sfnt.Font, error) {
	c, err := opentype.ParseCollectionReaderAt(r)
	if err != nil {
		return nil, errors.New("it is not a font this device can read")
	}
	if c.NumFonts() < 1 {
		return nil, errors.New("it has no font in it")
	}
	f, err := c.Font(0)
	if err != nil {
		return nil, errors.New("it is not a font this device can read")
	}
	return f, nil
}

// checkFont draws a few of a font's glyphs, to be sure its outlines are ones this device reads (some
// kinds of font read but cannot be drawn), before it is used.
func checkFont(f *sfnt.Font) error {
	n := f.NumGlyphs()
	if n < 2 {
		return errors.New("it has no glyphs")
	}
	var b sfnt.Buffer
	for _, g := range []int{1, n / 2, n - 1} {
		if _, err := f.LoadGlyph(&b, sfnt.GlyphIndex(g), fixed.I(32), nil); err != nil {
			return errors.New("its glyphs are not of a kind this device can draw")
		}
	}
	if _, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 24, DPI: 72, Hinting: font.HintingFull}); err != nil {
		return errors.New("it is not a font this device can draw")
	}
	return nil
}

// fontName is the name a font gives itself, its full name or else its family's.
func fontName(f *sfnt.Font) string {
	var b sfnt.Buffer
	for _, id := range []sfnt.NameID{sfnt.NameIDFull, sfnt.NameIDFamily} {
		if s, err := f.Name(&b, id); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// AddOwn keeps a font file sent from the setup page as name, if it is one: the name made safe, the
// file no bigger than the limits allow and readable as a font, else nothing is kept and the error
// says why. A file of the same name is replaced.
func AddOwn(name string, r io.Reader) error {
	name = filepath.Base(strings.TrimSpace(name))
	if !fontFile(name) {
		return errors.New("a font file's name ends in .ttf, .otf or .ttc")
	}
	if !safeName(name) {
		return errors.New("use letters, numbers, spaces, dots, dashes and underscores in the file's name")
	}
	if err := os.MkdirAll(Dir, 0o755); err != nil {
		return errors.New("the fonts folder could not be made")
	}
	var total int64
	files := 0
	for _, o := range Own() {
		if o.File != name && o.Problem == "" {
			total += o.Size
			files++
		}
	}
	if files >= MaxOwnFiles {
		return fmt.Errorf("there are %d fonts already; remove one first", files)
	}
	tmp, err := os.CreateTemp(Dir, ".upload-*")
	if err != nil {
		return errors.New("the font could not be saved")
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r, MaxOwnFile+1))
	if err != nil {
		tmp.Close()
		return errors.New("the font did not arrive whole")
	}
	if n > MaxOwnFile {
		tmp.Close()
		return fmt.Errorf("a font can be at most %d MB", MaxOwnFile>>20)
	}
	if total+n > MaxOwnTotal {
		tmp.Close()
		return fmt.Errorf("the fonts together can be at most %d MB; remove one first", MaxOwnTotal>>20)
	}
	f, err := parseFont(tmp)
	if err == nil {
		err = checkFont(f)
	}
	if cerr := tmp.Close(); err == nil && cerr != nil {
		err = errors.New("the font could not be saved")
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(Dir, name)); err != nil {
		return errors.New("the font could not be saved")
	}
	slog.Info("fonts: a font of your own was added", "file", name, "bytes", n)
	Rescan()
	return nil
}

// RemoveOwn deletes one of the folder's font files, named as Own lists it.
func RemoveOwn(file string) error {
	if !slices.ContainsFunc(Own(), func(o OwnFont) bool { return o.File == file }) {
		return errors.New("there is no such font")
	}
	if err := os.Remove(filepath.Join(Dir, file)); err != nil {
		return errors.New("the font could not be removed")
	}
	slog.Info("fonts: a font of your own was removed", "file", file)
	Rescan()
	return nil
}

// safeName is a file name of plain characters only, so it can be shown and named in a form as it is.
func safeName(name string) bool {
	if name == "" || len(name) > 100 || strings.HasPrefix(name, ".") {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.ContainsRune(" ._-", c):
		default:
			return false
		}
	}
	return true
}
