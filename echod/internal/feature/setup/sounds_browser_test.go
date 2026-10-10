package setup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// With TECHO5_BROWSER set to a Chrome or Chromium, this drives the real page: a stereo file at 44.1 kHz
// goes into a sound's file picker, the page converts and previews it, and "Use this sound" sends it.
// What the device then holds has to play as 48 kHz and last as long as the file did. Off by default,
// since it needs a browser.
func TestTheSoundsPageInARealBrowser(t *testing.T) {
	chrome := os.Getenv("TECHO5_BROWSER")
	if chrome == "" {
		t.Skip("set TECHO5_BROWSER to a Chrome or Chromium to run this")
	}
	f, c := letInBrowser(t)

	source := filepath.Join(t.TempDir(), "in.wav")
	if err := os.WriteFile(source, wav(44100, 2, 1200), 0o644); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/setup", f.serve)
	mux.HandleFunc("/setup/", f.serve)
	mux.HandleFunc("/in.wav", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, source) })
	mux.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		select {
		case result <- r.URL.Query().Get("r"):
		default:
		}
	})
	mux.HandleFunc("/driver", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: c.Name, Value: c.Value, Path: "/"})
		fmt.Fprint(w, `<!doctype html><iframe id="p" src="/setup?tab=sound" width="900" height="900"></iframe><script>
		const report = s => fetch('/result?r=' + encodeURIComponent(s));
		const wait = ms => new Promise(r => setTimeout(r, ms));
		document.getElementById('p').addEventListener('load', async () => {
		  try {
		    const d = document.getElementById('p').contentDocument;
		    const row = d.querySelector('.soundrow[data-name="timer_finished"]');
		    const blob = await (await fetch('/in.wav')).blob();
		    const dt = new DataTransfer(); dt.items.add(new File([blob], 'in.wav', {type: 'audio/wav'}));
		    const pick = row.querySelector('.soundfile'); pick.files = dt.files;
		    pick.dispatchEvent(new Event('change'));
		    const use = row.querySelector('.sounduse');
		    for (let i = 0; i < 50 && use.hidden; i++) await wait(100);
		    const said = row.querySelector('.soundsays').textContent;
		    if (use.hidden) return report('not ready: ' + said);
		    use.click();
		    for (let i = 0; i < 50 && row.querySelector('.soundsays').textContent !== 'Kept.'; i++) await wait(100);
		    report('done: ' + said + ' / ' + row.querySelector('.soundsays').textContent);
		  } catch (e) { report('error: ' + e); }
		});</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	startChrome(t, ctx, chrome, srv.URL+"/driver")

	select {
	case got := <-result:
		if !strings.HasPrefix(got, "done: 1.2 seconds") || !strings.HasSuffix(got, "Kept.") {
			t.Fatalf("the page said: %s", got)
		}
	case <-ctx.Done():
		t.Fatal("the page never finished")
	}
	if !speaker.ClipTimer.Own() {
		t.Fatal("the device holds no sound of its own after the upload")
	}
	if ms := speaker.ClipTimer.Ms(); ms < 1190 || ms > 1210 {
		t.Errorf("the kept sound lasts %d ms; the file was 1200", ms)
	}
}
