package setup

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/buttons"
	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// letInBrowser is a feature with the page open and one browser let in, and the folder sounds are kept
// in swapped for a temporary one.
func letInBrowser(t *testing.T) (*Feature, *http.Cookie) {
	t.Helper()
	was := speaker.SoundsDir
	speaker.SoundsDir = t.TempDir()
	t.Cleanup(func() { speaker.SoundsDir = was })
	f := build()
	f.Open()
	c := ask(t, f)
	f.button(buttons.Event{Name: buttons.Action, Kind: buttons.Tap})
	return f, c
}

// wav is ms of a quiet constant as a 16-bit WAVE file.
func wav(rate, channels, ms int) []byte {
	frames := rate * ms / 1000
	var b bytes.Buffer
	le := func(n, size int) {
		for i := range size {
			b.WriteByte(byte(n >> (8 * i)))
		}
	}
	b.WriteString("RIFF")
	le(36+frames*channels*2, 4)
	b.WriteString("WAVEfmt ")
	le(16, 4)
	le(1, 2)
	le(channels, 2)
	le(rate, 4)
	le(rate*channels*2, 4)
	le(channels*2, 2)
	le(16, 2)
	b.WriteString("data")
	le(frames*channels*2, 4)
	for range frames * channels {
		le(3000, 2)
	}
	return b.Bytes()
}

// upload posts a sound as the page's script does.
func upload(f *Feature, c *http.Cookie, token, name string, body []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if token != "" {
		_ = mw.WriteField("token", token)
	}
	_ = mw.WriteField("name", name)
	if body != nil {
		fw, _ := mw.CreateFormFile("sound", name+".wav")
		_, _ = fw.Write(body)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/setup/sound", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.serve(w, r)
	return w
}

func TestASoundIsKeptAndRemoved(t *testing.T) {
	f, c := letInBrowser(t)
	if w := upload(f, c, c.Value, "canceled", wav(48000, 1, 400)); w.Code != http.StatusNoContent {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(speaker.SoundsDir, "canceled.wav")); err != nil {
		t.Fatal(err)
	}
	if !speaker.ClipCanceled.Own() {
		t.Error("the device does not play the kept sound")
	}
	page := get(f, "/setup?tab=sound", c).Body.String()
	for _, want := range []string{"Sounds of your own", "In use now:", "Your own sound", "canceled.wav", "0.4 seconds", "Remove mine", `data-name="canceled"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if problem := removeSound("canceled"); problem != "" {
		t.Fatal(problem)
	}
	if speaker.ClipCanceled.Own() {
		t.Error("removed, but still the owner's")
	}
	if problem := removeSound("canceled"); problem != "" {
		t.Errorf("removing it twice: %s", problem)
	}
}

func TestASoundUploadIsHeldToTheSameRules(t *testing.T) {
	f, c := letInBrowser(t)
	cases := []struct {
		what   string
		token  string
		name   string
		body   []byte
		cookie *http.Cookie
		want   int
	}{
		{"no session", c.Value, "canceled", wav(48000, 1, 100), nil, http.StatusForbidden},
		{"no token", "", "canceled", wav(48000, 1, 100), c, http.StatusForbidden},
		{"someone else's token", "other", "canceled", wav(48000, 1, 100), c, http.StatusForbidden},
		{"unknown sound", c.Value, "../../etc/passwd", wav(48000, 1, 100), c, http.StatusBadRequest},
		{"a sound that is not listed", c.Value, "alarm", wav(48000, 1, 100), c, http.StatusBadRequest},
		{"no file", c.Value, "canceled", nil, c, http.StatusBadRequest},
		{"wrong rate", c.Value, "canceled", wav(44100, 1, 100), c, http.StatusBadRequest},
		{"too long", c.Value, "canceled", wav(48000, 1, 11000), c, http.StatusBadRequest},
		{"not audio", c.Value, "canceled", []byte("ID3 not a wave file at all"), c, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if w := upload(f, tc.cookie, tc.token, tc.name, tc.body); w.Code != tc.want {
			t.Errorf("%s: got %d (%s), want %d", tc.what, w.Code, strings.TrimSpace(w.Body.String()), tc.want)
		}
	}
	if entries, _ := os.ReadDir(speaker.SoundsDir); len(entries) != 0 {
		t.Errorf("refused uploads left %d files behind", len(entries))
	}
	r := httptest.NewRequest(http.MethodGet, "/setup/sound", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	f.serve(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET got %d", w.Code)
	}
}

func TestAnUploadTooBigIsRefused(t *testing.T) {
	f, c := letInBrowser(t)
	if w := upload(f, c, c.Value, "canceled", make([]byte, maxSound+(256<<10))); w.Code != http.StatusBadRequest {
		t.Errorf("an oversized upload got %d", w.Code)
	}
}

func TestSoundsAreSavedThroughTheSaveRoute(t *testing.T) {
	f, c := letInBrowser(t)
	if w := upload(f, c, c.Value, "timer_finished", wav(48000, 1, 300)); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	post := func(what, name string) string {
		v := url.Values{"token": {c.Value}, "what": {what}, "name": {name}, "tab": {"sound"}}
		r := httptest.NewRequest(http.MethodPost, "/setup/save", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(c)
		w := httptest.NewRecorder()
		f.serve(w, r)
		return w.Header().Get("Location")
	}
	if loc := post("sound-play", "timer_finished"); strings.Contains(loc, "problem") {
		t.Errorf("play was refused: %s", loc)
	}
	if loc := post("sound-play", "bogus"); !strings.Contains(loc, "problem") {
		t.Errorf("playing a sound that does not exist went to %s", loc)
	}
	if loc := post("sound-play", "failure"); !strings.Contains(loc, "problem") {
		t.Errorf("playing a tone-only sound with no recording went to %s", loc)
	}
	if loc := post("sound-remove", "timer_finished"); strings.Contains(loc, "problem") || speaker.ClipTimer.Own() {
		t.Errorf("remove went to %s, own %v", loc, speaker.ClipTimer.Own())
	}
	if loc := post("sound-remove", "bogus"); !strings.Contains(loc, "problem") {
		t.Errorf("removing a sound that does not exist went to %s", loc)
	}
}

// A long wake sound costs the first words, and the page says so beside it.
func TestALongWakeSoundIsWarnedAbout(t *testing.T) {
	f, c := letInBrowser(t)
	upload(f, c, c.Value, "wake_word_triggered", wav(48000, 1, 3000))
	page := get(f, "/setup?tab=sound", c).Body.String()
	if !strings.Contains(page, "This one is long") {
		t.Error("no warning beside a three-second wake sound")
	}
	upload(f, c, c.Value, "wake_word_triggered", wav(48000, 1, 800))
	if page := get(f, "/setup?tab=sound", c).Body.String(); strings.Contains(page, "This one is long") {
		t.Error("a short wake sound was called long")
	}
}

// The page's WAVE writer, run by Node, makes a file the device accepts and reads back at the length
// and level it was given.
func TestTheBrowsersWaveWriterMakesAFileTheDeviceAccepts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out := filepath.Join(t.TempDir(), "x.wav")
	script := encodeWavJS + `
	const n = 24000, s = new Float32Array(n);
	for (let i = 0; i < n; i++) s[i] = Math.sin(i / 10) * 0.5;
	s[0] = 2; s[1] = -2; // past full scale must clamp, not wrap
	encodeWav(s, 48000).arrayBuffer().then(b => require('fs').writeFileSync(process.argv[1], Buffer.from(b)));`
	if b, err := exec.Command(node, "-e", script, out).CombinedOutput(); err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := speaker.CheckOwn(b)
	if err != nil || ms != 500 {
		t.Fatalf("the device reads %d ms, %v", ms, err)
	}
	// samples 0 and 1 are the clamped ones: +full scale then -full scale
	if b[44] != 0xff || b[45] != 0x7f || b[46] != 0x00 || b[47] != 0x80 {
		t.Errorf("clamping: % x", b[44:48])
	}
}

// The page's script as a whole has to be valid JavaScript: a typo in it would only show as a button
// that does nothing.
func TestThePageScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	p := filepath.Join(t.TempDir(), "sounds.js")
	if err := os.WriteFile(p, []byte(soundScript("tok\"en")), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(node, "--check", p).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
}

// The page names the file in use: the one it was made from when it came through the page, and the
// stored file when it was copied there by hand, which carries no earlier name. A name with markup in it
// is shown as text.
func TestThePageNamesTheFileInUse(t *testing.T) {
	f, c := letInBrowser(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("token", c.Value)
	_ = mw.WriteField("name", "failure")
	_ = mw.WriteField("from", `<b onclick=x>oops.mp3`)
	fw, _ := mw.CreateFormFile("sound", "failure.wav")
	_, _ = fw.Write(wav(48000, 1, 300))
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/setup/sound", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(c)
	w := httptest.NewRecorder()
	f.serve(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	page := get(f, "/setup?tab=sound", c).Body.String()
	if !strings.Contains(page, "&lt;b onclick=x&gt;oops.mp3") || strings.Contains(page, "<b onclick=x>") {
		t.Error("the source file's name is missing or not escaped")
	}
	if !strings.Contains(page, "kept as failure.wav") {
		t.Error("the stored file is not named")
	}

	if err := os.WriteFile(filepath.Join(speaker.SoundsDir, "mute_switch_on.wav"), wav(48000, 1, 250), 0o644); err != nil {
		t.Fatal(err)
	}
	page = get(f, "/setup?tab=sound", c).Body.String()
	if !strings.Contains(page, "mute_switch_on.wav, 0.2 seconds") && !strings.Contains(page, "mute_switch_on.wav, 0.3 seconds") {
		t.Errorf("a file put there by hand is not named plainly")
	}
	if !strings.Contains(page, "Built-in tone (there is no recording)") {
		t.Error("a sound with no recording is not said to be a tone")
	}
}
