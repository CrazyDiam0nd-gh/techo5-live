package setup

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

func bridgeForm(extra url.Values) url.Values {
	v := url.Values{"mode": {"live"}, "key": {"sk-openai"}}
	for k, vs := range extra {
		v[k] = vs
	}
	return v
}

// The bridge is an address and a key, saved with the rest of the form.
func TestBridgeIsSaved(t *testing.T) {
	freshConfig(t)
	if problem := saveBrain(formPost(bridgeForm(url.Values{"bridge": {" http://192.168.1.20:8765/ "}, "bridge_key": {"bridge-secret"}}))); problem != "" {
		t.Fatalf("refused: %s", problem)
	}
	got := config.Get().Brain
	if got.Bridge != "http://192.168.1.20:8765" {
		t.Errorf("the address = %q, want it trimmed and without the closing slash", got.Bridge)
	}
	if got.BridgeKey != "bridge-secret" || got.Key != "sk-openai" {
		t.Errorf("the keys = %q and %q", got.BridgeKey, got.Key)
	}
}

func TestBridgeAddressMustBeAnAddress(t *testing.T) {
	for _, bad := range []string{"192.168.1.20:8765", "ftp://host/", "http://", "not an address", "http://host\nmore"} {
		freshConfig(t)
		problem := saveBrain(formPost(bridgeForm(url.Values{"bridge": {bad}, "bridge_key": {"k"}})))
		if problem == "" || !strings.Contains(problem, "bridge") {
			t.Errorf("%q: problem = %q, want a refusal that mentions the bridge", bad, problem)
		}
		if config.Get().Brain.Bridge != "" {
			t.Errorf("%q: it was saved", bad)
		}
	}
}

// An address with no key could never work (the bridge refuses every request without one), so it is
// refused here rather than failing at the next question.
func TestBridgeNeedsItsKey(t *testing.T) {
	freshConfig(t)
	problem := saveBrain(formPost(bridgeForm(url.Values{"bridge": {"http://192.168.1.20:8765"}})))
	if problem == "" || !strings.Contains(problem, "bridge") || !strings.Contains(problem, "key") {
		t.Errorf("problem = %q", problem)
	}
	// A key saved earlier counts, unless it is being removed.
	freshConfig(t)
	_ = config.Set().Brain().SetBridgeKey("saved-earlier")
	if problem := saveBrain(formPost(bridgeForm(url.Values{"bridge": {"http://192.168.1.20:8765"}}))); problem != "" {
		t.Errorf("a kept key was not enough: %s", problem)
	}
	if problem := saveBrain(formPost(bridgeForm(url.Values{"bridge": {"http://192.168.1.20:8765"}, "nobridgekey": {"yes"}}))); problem == "" {
		t.Errorf("removing the key and keeping the address was accepted")
	}
}

// Saving the form again without typing the key keeps it; the box to remove it removes it; a new one replaces it.
func TestBridgeKeyIsKeptReplacedOrRemoved(t *testing.T) {
	freshConfig(t)
	address := url.Values{"bridge": {"http://192.168.1.20:8765"}}
	_ = saveBrain(formPost(bridgeForm(url.Values{"bridge": address["bridge"], "bridge_key": {"first"}})))
	if p := saveBrain(formPost(bridgeForm(address))); p != "" || config.Get().Brain.BridgeKey != "first" {
		t.Errorf("saving without the key: problem %q, key %q", p, config.Get().Brain.BridgeKey)
	}
	_ = saveBrain(formPost(bridgeForm(url.Values{"bridge": address["bridge"], "bridge_key": {"second"}})))
	if config.Get().Brain.BridgeKey != "second" {
		t.Errorf("a new key was not saved: %q", config.Get().Brain.BridgeKey)
	}
	if p := saveBrain(formPost(bridgeForm(url.Values{"nobridgekey": {"yes"}}))); p != "" {
		t.Fatalf("removing: %s", p)
	}
	if got := config.Get().Brain; got.BridgeKey != "" || got.Bridge != "" {
		t.Errorf("after removing: %+v", got)
	}
	if config.Get().Brain.Key != "sk-openai" {
		t.Errorf("the OpenAI key changed: %q", config.Get().Brain.Key)
	}
	if p := saveBrain(formPost(bridgeForm(url.Values{"bridge_key": {"line\nbreak"}, "bridge": address["bridge"]}))); p == "" {
		t.Errorf("a key with a line break was accepted")
	}
}

// The key is never put back in the page: only whether there is one.
func TestBridgeKeyIsNeverShown(t *testing.T) {
	freshConfig(t)
	_ = saveBrain(formPost(bridgeForm(url.Values{"bridge": {"http://192.168.1.20:8765"}, "bridge_key": {"super-secret-bridge-key"}})))
	w := httptest.NewRecorder()
	brainSection(w, "tok")
	page := w.Body.String()
	if strings.Contains(page, "super-secret-bridge-key") {
		t.Fatal("the bridge key is in the page")
	}
	if !strings.Contains(page, `value="http://192.168.1.20:8765"`) {
		t.Errorf("the address is not shown")
	}
	at := strings.Index(page, `id="bridgekey"`)
	if at < 0 {
		t.Fatal("no key field")
	}
	field := page[at : at+strings.Index(page[at:], ">")]
	if !strings.Contains(field, `type="password"`) || !strings.Contains(field, `value=""`) || !strings.Contains(field, "set; leave empty to keep it") {
		t.Errorf("the key field = %s", field)
	}
	if !strings.Contains(page, `name="nobridgekey"`) {
		t.Errorf("no way to remove the key")
	}
}

// The page explains what the bridge is for, and keeps the person's text safe in the page.
func TestBridgeFieldIsExplainedAndEscaped(t *testing.T) {
	freshConfig(t)
	_ = config.Set().Brain().Set(config.Brain{Mode: config.BrainLive, Bridge: `http://h/"><script>x</script>`})
	w := httptest.NewRecorder()
	brainSection(w, "tok")
	page := w.Body.String()
	if strings.Contains(page, "<script>x</script>") {
		t.Errorf("the address is not escaped")
	}
	for _, want := range []string{"Bridge", "Empty: the assistant does not use one"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not say %q", want)
		}
	}
}
