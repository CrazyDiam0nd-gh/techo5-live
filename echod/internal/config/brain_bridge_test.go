package config

import (
	"path/filepath"
	"testing"
)

// The bridge's key is a secret like the OpenAI key: saving the rest of the form must not clear it, only
// SetBridgeKey changes it, and the two keys never touch each other.
func TestBridgeKeyIsKeptWhenTheRestIsSaved(t *testing.T) {
	st, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set().Brain().SetKey("openai-key"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set().Brain().SetBridgeKey("bridge-key"); err != nil {
		t.Fatal(err)
	}
	// A form save replaces everything but the keys.
	if err := st.Set().Brain().Set(Brain{Mode: BrainLive, Bridge: "http://192.168.1.20:8765"}); err != nil {
		t.Fatal(err)
	}
	got := st.Get().Brain
	if got.Key != "openai-key" || got.BridgeKey != "bridge-key" {
		t.Errorf("the keys after a save = %q and %q, want both kept", got.Key, got.BridgeKey)
	}
	if got.Bridge != "http://192.168.1.20:8765" || got.Mode != BrainLive {
		t.Errorf("the rest was not saved: %+v", got)
	}
	// Changing one key leaves the other.
	if err := st.Set().Brain().SetBridgeKey(""); err != nil {
		t.Fatal(err)
	}
	if got := st.Get().Brain; got.BridgeKey != "" || got.Key != "openai-key" {
		t.Errorf("after removing the bridge key: %q and %q", got.Key, got.BridgeKey)
	}
	if err := st.Set().Brain().SetKey(""); err != nil {
		t.Fatal(err)
	}
	if err := st.Set().Brain().SetBridgeKey("again"); err != nil {
		t.Fatal(err)
	}
	if got := st.Get().Brain; got.Key != "" || got.BridgeKey != "again" {
		t.Errorf("after removing the OpenAI key: %q and %q", got.Key, got.BridgeKey)
	}
}

// The settings survive a restart, under the names the file uses.
func TestBridgeSettingsAreWrittenAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Set().Brain().Set(Brain{Bridge: "http://b:8765"})
	_ = st.Set().Brain().SetBridgeKey("k")
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Get().Brain; got.Bridge != "http://b:8765" || got.BridgeKey != "k" {
		t.Errorf("read back %+v", got)
	}
}

func TestBridgeReadyNeedsTheAddressAndTheKey(t *testing.T) {
	for _, c := range []struct {
		b    Brain
		want bool
	}{
		{Brain{}, false},
		{Brain{Bridge: "http://b:8765"}, false},
		{Brain{BridgeKey: "k"}, false},
		{Brain{Bridge: "http://b:8765", BridgeKey: "k"}, true},
	} {
		if got := c.b.BridgeReady(); got != c.want {
			t.Errorf("%+v: BridgeReady = %v, want %v", c.b, got, c.want)
		}
	}
}
