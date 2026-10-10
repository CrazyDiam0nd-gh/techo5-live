package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// With TECHO5_BROWSER set to a Chrome or Chromium, this opens the real page and changes the "Answered
// by" list through each way of answering, reporting which fields can be seen. Off by default.
func TestTheVoiceFormFollowsTheWayChosenInARealBrowser(t *testing.T) {
	chrome := os.Getenv("TECHO5_BROWSER")
	if chrome == "" {
		t.Skip("set TECHO5_BROWSER to a Chrome or Chromium to run this")
	}
	f, c := letInBrowser(t)
	result := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/setup", f.serve)
	mux.HandleFunc("/setup/", f.serve)
	mux.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		select {
		case result <- r.URL.Query().Get("r"):
		default:
		}
	})
	mux.HandleFunc("/driver", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: c.Name, Value: c.Value, Path: "/"})
		fmt.Fprint(w, `<!doctype html><iframe id="p" src="/setup?tab=sound" width="900" height="1400"></iframe><script>
		const ids = ['stt','tts','voice','language','llm','model','key','search','prompt','livevoice','livemodel','livebackend'];
		document.getElementById('p').addEventListener('load', () => {
		  const d = document.getElementById('p').contentDocument, sel = d.getElementById('mode'), out = {};
		  for (const m of ['', 'direct', 'realtime', 'live']) {
		    sel.value = m; sel.dispatchEvent(new Event('change'));
		    out[m || 'ha'] = ids.filter(id => { const e = d.getElementById(id); return e && e.offsetParent !== null; }).join(',');
		  }
		  fetch('/result?r=' + encodeURIComponent(JSON.stringify(out)));
		});</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	startChrome(t, ctx, chrome, srv.URL+"/driver")

	var got string
	select {
	case got = <-result:
	case <-ctx.Done():
		t.Fatal("the page never reported")
	}
	for mode, want := range map[string]string{
		"ha":       "",
		"direct":   "stt,tts,voice,language,llm,model,key,search,prompt",
		"realtime": "tts,voice,language,model,key,search,prompt",
		"live":     "key,search,prompt,livevoice,livemodel,livebackend",
	} {
		if !strings.Contains(got, `"`+mode+`":"`) {
			t.Fatalf("no answer for %s in %s", mode, got)
		}
		field := got[strings.Index(got, `"`+mode+`":"`)+len(mode)+4:]
		field = field[:strings.Index(field, `"`)]
		if field != want {
			t.Errorf("%s shows %q, want %q", mode, field, want)
		}
	}
}

// In a real browser, a save the device would refuse is stopped on the page, with the reason beside the button
// and what was typed left where it is; one it would take goes through.
func TestASaveTheDeviceWouldRefuseIsStoppedOnThePage(t *testing.T) {
	chrome := os.Getenv("TECHO5_BROWSER")
	if chrome == "" {
		t.Skip("set TECHO5_BROWSER to a Chrome or Chromium to run this")
	}
	f, c := letInBrowser(t)
	result := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/setup", f.serve)
	mux.HandleFunc("/setup/", f.serve)
	mux.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		select {
		case result <- r.URL.Query().Get("r"):
		default:
		}
	})
	mux.HandleFunc("/driver", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: c.Name, Value: c.Value, Path: "/"})
		fmt.Fprint(w, `<!doctype html><iframe id="p" src="/setup?tab=sound" width="900" height="1400"></iframe><script>
		document.getElementById('p').addEventListener('load', () => {
		  const d = document.getElementById('p').contentDocument, form = d.getElementById('brainform'), sel = d.getElementById('mode'),
		        why = d.getElementById('brainwhy'), set = (id, v) => { d.getElementById(id).value = v; };
		  const attempt = (mode, fields) => {
		    for (const id of ['stt','tts','llm','key']) set(id, '');
		    set('prompt', 'keep me');
		    sel.value = mode; sel.dispatchEvent(new Event('change'));
		    for (const [id, v] of Object.entries(fields)) set(id, v);
		    const ev = new Event('submit', {cancelable: true, bubbles: true});
		    form.dispatchEvent(ev);
		    return {stopped: ev.defaultPrevented, why: why.hidden ? '' : why.textContent, kept: d.getElementById('prompt').value};
		  };
		  const out = {
		    realtimeNoServer: attempt('realtime', {key: 'k'}),
		    realtimeNoKey: attempt('realtime', {tts: 'h:10200'}),
		    realtimeBoth: attempt('realtime', {tts: 'h:10200', key: 'k'}),
		    liveNoKey: attempt('live', {}),
		    liveKey: attempt('live', {key: 'k'}),
		    directPart: attempt('direct', {stt: 'h:1'}),
		    ha: attempt('', {}),
		  };
		  fetch('/result?r=' + encodeURIComponent(JSON.stringify(out)));
		});</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	startChrome(t, ctx, chrome, srv.URL+"/driver")

	var got string
	select {
	case got = <-result:
	case <-ctx.Done():
		t.Fatal("the page never reported")
	}
	var out map[string]struct {
		Stopped bool   `json:"stopped"`
		Why     string `json:"why"`
		Kept    string `json:"kept"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatal(err, got)
	}
	for name, want := range map[string]struct {
		stopped bool
		says    string
	}{
		"realtimeNoServer": {true, "text-to-speech server"},
		"realtimeNoKey":    {true, "OpenAI key"},
		"realtimeBoth":     {false, ""},
		"liveNoKey":        {true, "OpenAI key"},
		"liveKey":          {false, ""},
		"directPart":       {true, "all three"},
		"ha":               {false, ""},
	} {
		o := out[name]
		if o.Stopped != want.stopped || (want.says != "" && !strings.Contains(o.Why, want.says)) || (!want.stopped && o.Why != "") {
			t.Errorf("%s: stopped %v, says %q; want stopped %v, saying %q", name, o.Stopped, o.Why, want.stopped, want.says)
		}
		if o.Kept != "keep me" {
			t.Errorf("%s: what was typed became %q", name, o.Kept)
		}
	}
}
