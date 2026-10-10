package hass

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{acc: access{URL: srv.URL, Token: "t"}, http: &http.Client{Timeout: 5 * time.Second}}
}

func TestScriptInfo(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config/script/config/play_music" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"alias":"Play music","description":"Play an artist.",
			"fields":{"name":{"description":"The artist","required":true,"selector":{"text":{}}},
			"device":{"description":"Speaker","selector":{"select":{"options":["dot",{"value":"denon","label":"Denon"}]}}}}}`))
	})
	info, err := c.ScriptInfo("play_music")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Play music" || len(info.Fields) != 2 {
		t.Fatalf("%+v", info)
	}
	// Sorted by name: device, then name.
	if info.Fields[0].Name != "device" || strings.Join(info.Fields[0].Options, ",") != "dot,denon" || !info.Fields[1].Required {
		t.Errorf("%+v", info.Fields)
	}
}

func TestRunScriptReturnsItsAnswer(t *testing.T) {
	var path, query string
	var body map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"changed_states":[],"service_response":{"message":"Playlist not found"}}`))
	})
	out, err := c.RunScript("play_playlist", map[string]any{"playlist": "Metal"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/services/script/play_playlist" || query != "return_response" || body["playlist"] != "Metal" {
		t.Errorf("path=%s query=%s body=%v", path, query, body)
	}
	if !strings.Contains(out, "Playlist not found") {
		t.Errorf("answer %q", out)
	}
}

func TestRunScriptWithNoAnswer(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"changed_states":[],"service_response":{}}`))
	})
	out, err := c.RunScript("stop_music", nil)
	if err != nil || out != "done" {
		t.Errorf("%q %v", out, err)
	}
}
