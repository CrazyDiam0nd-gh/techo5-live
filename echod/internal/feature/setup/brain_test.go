package setup

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/live"
)

func formPost(v url.Values) *http.Request {
	r := httptest.NewRequest("POST", "/setup/save", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func freshConfig(t *testing.T) {
	t.Helper()
	config.Use(filepath.Join(t.TempDir(), "state.json"))
}

func TestLiveConversationIsSaved(t *testing.T) {
	freshConfig(t)
	problem := saveBrain(formPost(url.Values{
		"mode": {"realtime"}, "tts": {"192.168.1.20:10200"}, "voice": {"en_US-lessac-medium"},
		"model": {""}, "key": {"sk-test-key"}, "prompt": {"Be brief."}}))
	if problem != "" {
		t.Fatalf("refused: %s", problem)
	}
	b := config.Get().Brain
	if b.Mode != config.BrainRealtime || !b.Realtime() || b.Key != "sk-test-key" || b.TTS != "192.168.1.20:10200" || b.Prompt != "Be brief." {
		t.Errorf("kept %+v", b)
	}
}

// Live conversation cannot work without a voice and a key, so the form says so rather than keeping a
// setting that fails at the next wake word.
func TestLiveConversationNeedsAVoiceAndAKey(t *testing.T) {
	freshConfig(t)
	if problem := saveBrain(formPost(url.Values{"mode": {"realtime"}, "key": {"k"}})); problem == "" {
		t.Error("took live conversation with no text-to-speech server")
	}
	if problem := saveBrain(formPost(url.Values{"mode": {"realtime"}, "tts": {"h:10200"}})); problem == "" {
		t.Error("took live conversation with no key")
	}
	if config.Get().Brain.Mode == config.BrainRealtime {
		t.Error("kept a setting it refused")
	}
}

// The key is written once and kept: saving the form again with the box empty does not lose it, and
// removing it from live conversation is refused.
func TestTheKeyIsKept(t *testing.T) {
	freshConfig(t)
	form := url.Values{"mode": {"realtime"}, "tts": {"h:10200"}, "key": {"sk-one"}}
	if p := saveBrain(formPost(form)); p != "" {
		t.Fatal(p)
	}
	form.Set("key", "")
	form.Set("voice", "other")
	if p := saveBrain(formPost(form)); p != "" {
		t.Fatalf("saving again with the key box empty: %s", p)
	}
	if got := config.Get().Brain.Key; got != "sk-one" {
		t.Errorf("key is %q", got)
	}
	form.Set("nokey", "yes")
	if p := saveBrain(formPost(form)); p == "" {
		t.Error("removed the key from live conversation")
	}
	if config.Get().Brain.Key != "sk-one" {
		t.Error("the key went anyway")
	}
}

func TestBackToHomeAssistant(t *testing.T) {
	freshConfig(t)
	if p := saveBrain(formPost(url.Values{"mode": {"realtime"}, "tts": {"h:10200"}, "key": {"k"}})); p != "" {
		t.Fatal(p)
	}
	if p := saveBrain(formPost(url.Values{"mode": {""}, "tts": {"h:10200"}})); p != "" {
		t.Fatal(p)
	}
	if b := config.Get().Brain; b.Realtime() || b.Direct() || b.Mode != config.BrainHomeAssistant {
		t.Errorf("still on %+v", b)
	}
}

func TestAnUnknownWayOfAnsweringIsRefused(t *testing.T) {
	freshConfig(t)
	if p := saveBrain(formPost(url.Values{"mode": {"telepathy"}})); p == "" {
		t.Error("took a mode it does not know")
	}
}

// The form offers live conversation, says what it needs, and never prints the key it holds.
func TestTheFormOffersLiveConversationAndHidesTheKey(t *testing.T) {
	freshConfig(t)
	if p := saveBrain(formPost(url.Values{"mode": {"realtime"}, "tts": {"h:10200"}, "key": {"sk-very-secret"}})); p != "" {
		t.Fatal(p)
	}
	w := httptest.NewRecorder()
	brainSection(w, "tok")
	body := w.Body.String()
	for _, want := range []string{`value="realtime" selected`, "Live conversation", config.DefaultRealtimeModel, "not connected"} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	if strings.Contains(body, "sk-very-secret") {
		t.Error("the form prints the key")
	}
}

func TestLiveVoiceIsSavedWithOnlyAKey(t *testing.T) {
	freshConfig(t)
	problem := saveBrain(formPost(url.Values{"mode": {"live"}, "key": {"sk-test-key"}, "live_voice": {"cedar"},
		"live_model": {""}, "live_backend": {"gpt-5.6-luna"}}))
	if problem != "" {
		t.Fatalf("refused: %s", problem)
	}
	b := config.Get().Brain
	if !b.Live() || b.Realtime() || b.Direct() || b.LiveVoice != "cedar" || b.LiveBackend != "gpt-5.6-luna" || b.LiveModel != "" {
		t.Errorf("kept %+v", b)
	}
}

func TestLiveVoiceNeedsAKeyAndKeepsTheOne(t *testing.T) {
	freshConfig(t)
	if p := saveBrain(formPost(url.Values{"mode": {"live"}})); p == "" {
		t.Error("took live voice with no key")
	}
	if config.Get().Brain.Mode == config.BrainLive {
		t.Error("kept a setting it refused")
	}
	if p := saveBrain(formPost(url.Values{"mode": {"live"}, "key": {"sk-one"}})); p != "" {
		t.Fatal(p)
	}
	// Saving again with the key box empty keeps it; asking to remove it is refused in this mode.
	if p := saveBrain(formPost(url.Values{"mode": {"live"}, "live_voice": {"marin"}})); p != "" {
		t.Fatalf("saving with the key box empty: %s", p)
	}
	if config.Get().Brain.Key != "sk-one" {
		t.Errorf("key is %q", config.Get().Brain.Key)
	}
	if p := saveBrain(formPost(url.Values{"mode": {"live"}, "nokey": {"yes"}})); p == "" {
		t.Error("removed the key from live voice")
	}
}

func TestLiveVoiceFieldsAreOneLineAndTheFormOffersIt(t *testing.T) {
	freshConfig(t)
	if p := saveBrain(formPost(url.Values{"mode": {"live"}, "key": {"k"}, "live_voice": {"a\nb"}})); p == "" {
		t.Error("took a voice of two lines")
	}
	if p := saveBrain(formPost(url.Values{"mode": {"live"}, "key": {"k"}, "live_voice": {"ember"}, "live_model": {"gpt-live-1"}})); p != "" {
		t.Fatal(p)
	}
	w := httptest.NewRecorder()
	brainSection(w, "tok")
	page := w.Body.String()
	for _, want := range []string{`value="live" selected`, `<select id="livevoice" name="live_voice">`,
		`<option value="marin">marin</option>`, `<option value="cedar">cedar</option>`, `<option value="coral">coral</option>`,
		`<option value="ember" selected>ember (not in the list)</option>`, `Default (marin)`,
		`name="live_model" value="gpt-live-1"`, "gpt-5.6-luna", "about five cents a minute"} {
		if !strings.Contains(page, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	if strings.Contains(page, "sk-") || strings.Contains(page, `value="k"`) {
		t.Error("the key is shown")
	}
}

// Going back to another mode keeps what was chosen for live voice, and the live fields do not leak
// into the others.
func TestLiveVoiceFieldsSurviveAModeChange(t *testing.T) {
	freshConfig(t)
	_ = saveBrain(formPost(url.Values{"mode": {"live"}, "key": {"k"}, "live_voice": {"verse"}}))
	if p := saveBrain(formPost(url.Values{"mode": {""}, "live_voice": {"verse"}})); p != "" {
		t.Fatal(p)
	}
	b := config.Get().Brain
	if b.Live() || b.LiveVoice != "verse" || b.Key != "k" {
		t.Errorf("kept %+v", b)
	}
}

// Every voice is a choice whatever is selected (the list does not shrink to the one in force, as a
// suggestion list does), and the one in force is the one marked.
func TestTheVoiceListShowsEveryVoiceWhateverIsChosen(t *testing.T) {
	for _, cur := range []string{"", "coral", "marin", "verse", "<b>x"} {
		opts := liveVoiceOptions(cur)
		for _, v := range live.Voices {
			if !strings.Contains(opts, `value="`+v+`"`) {
				t.Errorf("with %q chosen, %s is missing", cur, v)
			}
		}
		if got := strings.Count(opts, " selected"); got != 1 {
			t.Errorf("with %q chosen, %d options are selected", cur, got)
		}
		if strings.Contains(opts, "<b>x") {
			t.Errorf("a custom name was not escaped: %s", opts)
		}
	}
	if !strings.Contains(liveVoiceOptions("coral"), `<option value="coral" selected>coral</option>`) {
		t.Error("coral is not the selected one")
	}
}
