package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

func useBridge(t *testing.T, address, key string) {
	t.Helper()
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	if err := config.Set().Brain().Set(config.Brain{Bridge: address}); err != nil {
		t.Fatal(err)
	}
	if err := config.Set().Brain().SetBridgeKey(key); err != nil {
		t.Fatal(err)
	}
}

func toolNamed(name string) *tool {
	for _, t := range tools() {
		if t.Name == name {
			t := t
			return &t
		}
	}
	return nil
}

// Without an address and a key there is no bridge tool, and the model is not told of one.
func TestBridgeToolIsOfferedOnlyWhenSetUp(t *testing.T) {
	for _, c := range []struct{ address, key string }{{"", ""}, {"http://b:8765", ""}, {"", "k"}} {
		useBridge(t, c.address, c.key)
		if toolNamed("ask_bridge") != nil {
			t.Errorf("address %q key %q: the tool is offered", c.address, c.key)
		}
	}
	useBridge(t, "http://b:8765", "k")
	tl := toolNamed("ask_bridge")
	if tl == nil {
		t.Fatal("not offered when set up")
	}
	params, _ := json.Marshal(tl.Parameters)
	if !strings.Contains(string(params), `"question"`) || !strings.Contains(string(params), `"required":["question"]`) {
		t.Errorf("the question is not a required parameter: %s", params)
	}
	if !strings.Contains(tl.Description, "Working.") {
		t.Errorf("the description does not tell the model to say Working. first: %q", tl.Description)
	}
}

// What the bridge is sent is what Home Assistant would send it: a POST of JSON to /api/chat with the
// bridge's key as a bearer token, and the person's words as text.
func TestAskBridgeSendsWhatTheBridgeExpects(t *testing.T) {
	var got struct {
		method, path, auth, ctype string
		body                      map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.auth, got.ctype = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		_, _ = w.Write([]byte(`{"reply": "It is 2:49 AM on Monday in Tokyo.", "continue_conversation": false}`))
	}))
	defer srv.Close()
	for _, address := range []string{srv.URL, srv.URL + "/", srv.URL + "//"} {
		useBridge(t, address, "secret-key")
		out, err := toolNamed("ask_bridge").Run(map[string]any{"question": "  What time is it in Tokyo?  "})
		if err != nil {
			t.Fatalf("%q: %v", address, err)
		}
		if out != "It is 2:49 AM on Monday in Tokyo." {
			t.Errorf("%q: the reply = %q", address, out)
		}
		if got.method != "POST" || got.path != "/api/chat" || got.auth != "Bearer secret-key" || !strings.HasPrefix(got.ctype, "application/json") {
			t.Errorf("%q: sent %+v", address, got)
		}
		if got.body["text"] != "What time is it in Tokyo?" || got.body["language"] != "en" {
			t.Errorf("%q: body %v", address, got.body)
		}
		for _, k := range []string{"conversation_id", "satellite_id"} {
			if s, _ := got.body[k].(string); s == "" {
				t.Errorf("%q: no %s in %v", address, k, got.body)
			}
		}
	}
}

func TestAskBridgeSaysWhatWentWrong(t *testing.T) {
	for _, c := range []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"a refused key", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte("Unauthorized"))
		}, "refused the key"},
		{"its agent not answering", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(502)
			_, _ = w.Write([]byte("the agent is unavailable"))
		}, "its agent"},
		{"a server error", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, "500"},
		{"not json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }, "not understand"},
		{"no reply", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"continue_conversation": false}`))
		}, "no reply"},
		{"an empty reply", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"reply": "   "}`)) }, "no reply"},
	} {
		srv := httptest.NewServer(c.handler)
		useBridge(t, srv.URL, "k")
		out, err := toolNamed("ask_bridge").Run(map[string]any{"question": "anything"})
		srv.Close()
		if err == nil {
			t.Errorf("%s: no error, got %q", c.name, out)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: the error %q does not mention %q", c.name, err, c.want)
		}
		if strings.Contains(err.Error(), "k\"") && strings.Contains(err.Error(), "Bearer") {
			t.Errorf("%s: the error shows the key: %q", c.name, err)
		}
	}
}

func TestAskBridgeWhenNothingIsListening(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	address := srv.URL
	srv.Close()
	useBridge(t, address, "k")
	_, err := toolNamed("ask_bridge").Run(map[string]any{"question": "anything"})
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("error = %v, want one saying the bridge did not answer", err)
	}
}

func TestAskBridgeGivesUpAfterTheWait(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	useBridge(t, srv.URL, "k")
	saved := bridgeWait
	bridgeWait = 150 * time.Millisecond
	defer func() { bridgeWait = saved }()
	start := time.Now()
	_, err := toolNamed("ask_bridge").Run(map[string]any{"question": "anything"})
	if err == nil || !strings.Contains(err.Error(), "did not answer in time") {
		t.Errorf("error = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v to give up", time.Since(start))
	}
}

func TestAskBridgeNeedsAQuestion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("the bridge was asked") }))
	defer srv.Close()
	useBridge(t, srv.URL, "k")
	for _, args := range []map[string]any{{}, {"question": ""}, {"question": "   "}, {"question": 5.0}} {
		if _, err := toolNamed("ask_bridge").Run(args); err == nil || !strings.Contains(err.Error(), "nothing to ask") {
			t.Errorf("%v: error = %v", args, err)
		}
	}
}

// A runaway reply is cut off, not read for ever, and the model is not given more than it can say.
func TestAskBridgeCutsAHugeReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"reply": "` + strings.Repeat("word ", 200000) + `"}`))
	}))
	defer srv.Close()
	useBridge(t, srv.URL, "k")
	out, err := toolNamed("ask_bridge").Run(map[string]any{"question": "anything"})
	if err == nil && len(out) > 8000 {
		t.Errorf("a reply of %d characters went back to the model", len(out))
	}
}

// The model is told it can use the bridge when one is set, and the old "you cannot look anything up"
// is not said then; with no bridge the instructions are exactly what they were.
func TestInstructionsMentionTheBridgeOnlyWhenThereIsOne(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	plain := instructions(config.Brain{}, now)
	if strings.Contains(plain, "ask_bridge") || !strings.Contains(plain, "Beyond the tools you cannot look anything up") {
		t.Errorf("without a bridge the instructions changed:\n%s", plain)
	}
	withBridge := instructions(config.Brain{Bridge: "http://b:8765", BridgeKey: "k"}, now)
	if !strings.Contains(withBridge, "ask_bridge") || !strings.Contains(withBridge, "Working.") {
		t.Errorf("the instructions do not tell the model about the bridge:\n%s", withBridge)
	}
	if strings.Contains(withBridge, "Beyond the tools you cannot look anything up") {
		t.Errorf("the model is told it cannot look anything up, with a bridge that can")
	}
	if half := instructions(config.Brain{Bridge: "http://b:8765"}, now); strings.Contains(half, "ask_bridge") {
		t.Errorf("a bridge without its key is mentioned")
	}
	both := instructions(config.Brain{Bridge: "http://b:8765", BridgeKey: "k", Search: "http://s:8888"}, now)
	if !strings.Contains(both, "ask_bridge") || !strings.Contains(both, "web_search") {
		t.Errorf("with a search server and a bridge the model should hear of both")
	}
}
