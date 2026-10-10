package setup

import (
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"

	"github.com/HuskerMinion/techo5/echod/internal/hardware/speaker"
)

// Sounds of your own, kept on the device (hardware/speaker/ownsounds.go). The device plays 16-bit
// WAVE files at 48 kHz, which nobody has lying about, so the browser makes one from whatever audio
// file it can play and shows it before sending: the page decodes, mixes down to one channel, resamples,
// and writes the WAVE file. The device then applies the same rule to it that it applies to a file
// found in the folder, so a request that did not come from this page's script is held to it too.

// maxSound is one upload. A sound of the longest allowed, ten seconds of mono, is under a megabyte;
// this leaves room for the form around it and for a stereo file sent by hand.
const maxSound = 4 << 20

// longWake is how long a wake sound may be before the page says it will cost the first words: the
// microphone is held back while the sound plays.
const longWake = 1500

func soundsSection(w http.ResponseWriter, token string) {
	fmt.Fprint(w, `<fieldset><legend>Sounds of your own</legend>
	 <p style="margin:0">Pick any audio file for a sound and this page makes it fit: it is turned into
	  the device's format on this phone or computer, and you can hear it before it is kept. A sound is at
	  most 10 seconds. Removing yours puts the built-in sound back.</p>`)
	for _, s := range speaker.OwnSounds() {
		soundRow(w, token, s)
	}
	fmt.Fprintf(w, `<script>%s</script></fieldset>`, soundScript(token))
}

func soundRow(w http.ResponseWriter, token string, s speaker.OwnSound) {
	own := s.Clip.Own()
	ms := s.Clip.Ms()
	var state string
	switch {
	case own:
		state = fmt.Sprintf("your own, %.1f seconds", float64(ms)/1000)
	case s.Clip.HasStock():
		state = fmt.Sprintf("built in, %.1f seconds", float64(s.Clip.StockMs())/1000)
	default:
		state = "a built-in tone"
	}
	fmt.Fprintf(w, `<div class="soundrow" data-name="%s" style="margin-top:1.1em">
	 <strong>%s</strong> <span class="note">%s: %s</span>
	 <input type="file" accept="audio/*" class="soundfile" aria-label="%s: choose an audio file">`,
		html.EscapeString(s.Name), html.EscapeString(s.Label), html.EscapeString(s.When), html.EscapeString(state),
		html.EscapeString(s.Label))
	if s.Name == speaker.ClipWake.Name() && own && ms > longWake {
		fmt.Fprint(w, `<p class="note" style="margin:0">This one is long. The microphone is held back while it plays,
		 so the first words after the wake word are lost; say the request after the sound.</p>`)
	}
	fmt.Fprint(w, `<p class="soundsays note" style="margin:0"></p>
	 <audio class="soundheard" controls hidden></audio>
	 <button type="button" class="sounduse" hidden>Use this sound</button>`)
	if own || s.Clip.HasStock() {
		fmt.Fprint(w, `<form method="post" action="/setup/save" style="display:inline">`)
		hidden(w, token, "sound-play", "sound")
		fmt.Fprintf(w, `<input type="hidden" name="name" value="%s"><button type="submit">Play on the device</button></form>`,
			html.EscapeString(s.Name))
	}
	if own {
		fmt.Fprint(w, `<form method="post" action="/setup/save" style="display:inline">`)
		hidden(w, token, "sound-remove", "sound")
		fmt.Fprintf(w, `<input type="hidden" name="name" value="%s"><button type="submit">Remove mine</button></form>`,
			html.EscapeString(s.Name))
	}
	fmt.Fprint(w, `</div>`)
}

// soundScript is the page's side: choose a file, make the WAVE file, show it, send it. The WAVE
// writer is its own function so a test can run it.
func soundScript(token string) string {
	return fmt.Sprintf(`
	const TOKEN = %q, RATE = 48000, LONGEST = 10, LONG_WAKE = %d / 1000;
	%s
	async function toWav(file) {
	  const ctx = new (window.AudioContext || window.webkitAudioContext)();
	  let decoded;
	  try { decoded = await ctx.decodeAudioData(await file.arrayBuffer()); }
	  finally { ctx.close(); }
	  if (decoded.duration > LONGEST) throw new Error('That is ' + decoded.duration.toFixed(1) + ' seconds; a sound is at most ' + LONGEST + '.');
	  if (decoded.duration <= 0) throw new Error('That file holds no sound.');
	  const frames = Math.ceil(decoded.duration * RATE);
	  const off = new OfflineAudioContext(1, frames, RATE);
	  const src = off.createBufferSource();
	  src.buffer = decoded; src.connect(off.destination); src.start();
	  const mono = (await off.startRendering()).getChannelData(0);
	  return encodeWav(mono, RATE);
	}
	document.querySelectorAll('.soundrow').forEach(row => {
	  const name = row.dataset.name, say = row.querySelector('.soundsays'), audio = row.querySelector('.soundheard'),
	        use = row.querySelector('.sounduse'), pick = row.querySelector('.soundfile');
	  let wav = null;
	  pick.addEventListener('change', async () => {
	    wav = null; use.hidden = true; audio.hidden = true;
	    if (!pick.files.length) { say.textContent = ''; return; }
	    say.textContent = 'Getting it ready...';
	    try {
	      wav = await toWav(pick.files[0]);
	      const secs = (wav.size - 44) / (RATE * 2);
	      audio.src = URL.createObjectURL(wav); audio.hidden = false; use.hidden = false;
	      say.textContent = secs.toFixed(1) + ' seconds.' + (name === 'wake_word_triggered' && secs > LONG_WAKE
	        ? ' That is long for a wake sound: the microphone is held back while it plays, so say the request after it.' : '');
	    } catch (e) {
	      say.textContent = 'This browser could not use that file: ' + (e && e.message ? e.message : 'it is not audio it can play.');
	    }
	  });
	  use.addEventListener('click', async () => {
	    if (!wav) return;
	    use.disabled = true; say.textContent = 'Sending...';
	    try {
	      const fd = new FormData(); fd.append('token', TOKEN); fd.append('name', name); fd.append('sound', wav, name + '.wav');
	      const r = await fetch('/setup/sound', {method: 'POST', body: fd});
	      if (r.ok) { say.textContent = 'Kept.'; setTimeout(() => location.reload(), 800); }
	      else { say.textContent = await r.text(); use.disabled = false; }
	    } catch (e) { say.textContent = 'Could not send it.'; use.disabled = false; }
	  });
	});`, token, longWake, encodeWavJS)
}

// encodeWavJS writes float samples in -1..1 as a 16-bit mono WAVE file.
const encodeWavJS = `function encodeWav(samples, rate) {
	  const buf = new ArrayBuffer(44 + samples.length * 2), v = new DataView(buf);
	  const str = (at, s) => { for (let i = 0; i < s.length; i++) v.setUint8(at + i, s.charCodeAt(i)); };
	  str(0, 'RIFF'); v.setUint32(4, 36 + samples.length * 2, true); str(8, 'WAVE'); str(12, 'fmt ');
	  v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true);
	  v.setUint32(24, rate, true); v.setUint32(28, rate * 2, true); v.setUint16(32, 2, true); v.setUint16(34, 16, true);
	  str(36, 'data'); v.setUint32(40, samples.length * 2, true);
	  for (let i = 0; i < samples.length; i++) {
	    const s = Math.max(-1, Math.min(1, samples[i]));
	    v.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
	  }
	  return new Blob([buf], {type: 'audio/wav'});
	}`

// sound takes one recording from the page's script, as photo takes a picture.
func (f *Feature) sound(w http.ResponseWriter, r *http.Request) {
	token, in := f.session(r)
	if !in {
		http.Error(w, "not let in", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "post a sound", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSound+64<<10)
	if err := r.ParseMultipartForm(maxSound); err != nil {
		http.Error(w, "that sound was too big", http.StatusBadRequest)
		return
	}
	if r.FormValue("token") != token {
		http.Error(w, "that did not come from this page", http.StatusForbidden)
		return
	}
	s, ok := speaker.OwnSoundNamed(r.FormValue("name"))
	if !ok {
		http.Error(w, "that is not a sound this device has", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("sound")
	if err != nil {
		http.Error(w, "no sound was sent", http.StatusBadRequest)
		return
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, maxSound+1))
	if err != nil || len(b) > maxSound {
		http.Error(w, "the sound did not arrive whole", http.StatusBadRequest)
		return
	}
	if err := s.Clip.SaveOwn(b); err != nil {
		http.Error(w, "the device cannot play that: "+err.Error(), http.StatusBadRequest)
		return
	}
	slog.Info("setup page: a sound of your own was kept", "sound", s.Name, "bytes", len(b))
	f.used()
	w.WriteHeader(http.StatusNoContent)
}

func removeSound(name string) string {
	s, ok := speaker.OwnSoundNamed(name)
	if !ok {
		return "that is not a sound this device has"
	}
	if err := s.Clip.RemoveOwn(); err != nil {
		return "could not remove it: " + err.Error()
	}
	slog.Info("setup page: a sound of your own was removed", "sound", s.Name)
	return ""
}

// playSound plays what plays now for the sound: the owner's, or the built-in one.
func playSound(name string) string {
	s, ok := speaker.OwnSoundNamed(name)
	if !ok {
		return "that is not a sound this device has"
	}
	if !s.Clip.Own() && !s.Clip.HasStock() {
		return "that one is a tone; there is no recording to play"
	}
	speaker.Sound().Chime([]speaker.Note{s.Clip.Note()})
	return ""
}
