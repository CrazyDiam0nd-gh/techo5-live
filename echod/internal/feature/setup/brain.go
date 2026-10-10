package setup

import (
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/wakeword"
	"github.com/HuskerMinion/techo5/echod/internal/lib/hass"
	"github.com/HuskerMinion/techo5/echod/internal/lib/live"
)

// listeningSection is how long the device listens again after an answer, and how many times in a row:
// Home Assistant's two settings for the first wake word, here for a device that has none.
func listeningSection(w http.ResponseWriter, token string) {
	fmt.Fprint(w, `<fieldset><legend>Listening after an answer</legend><form method="post" action="/setup/save">`)
	hidden(w, token, "listening", "sound")
	fmt.Fprintf(w, `<label for="fu">Keep listening for (seconds, 0 for only when asked a question)</label>
	 <input id="fu" name="followup" type="number" min="0" max="30" value="%d">
	 <label for="fus">Times in a row (0 for no limit)</label>
	 <input id="fus" name="followups" type="number" min="0" max="10" value="%d">
	 <p class="note">After an answer the device listens again without the wake word, for this long and this
	  many times. A question the assistant asks is always listened for.</p>
	 <p><button type="submit">Save</button></p></form></fieldset>`,
		int(wakeword.FollowUp(0).Seconds()), wakeword.FollowUps(0))
}

func saveListening(r *http.Request) string {
	fu, err1 := strconv.Atoi(strings.TrimSpace(r.PostFormValue("followup")))
	fus, err2 := strconv.Atoi(strings.TrimSpace(r.PostFormValue("followups")))
	if err1 != nil || err2 != nil || fu < 0 || fu > 30 || fus < 0 || fus > 10 {
		return "listening is 0 to 30 seconds, and 0 to 10 times in a row"
	}
	wakeword.Get().SetFollowUp(0, fu)
	wakeword.Get().SetFollowUps(0, fus)
	return ""
}

// brainSection is where the voice answers come from: Home Assistant's Assist pipeline, or speech and a
// chat model reached directly (config.Brain). The key is never shown: it is written, or left alone.
//
// Each way of answering uses some of the fields and not others, so the form shows only the ones that
// matter to the way chosen (data-modes names the ways a part belongs to). The others are only hidden:
// their saved values go back with the form, so changing the way and back loses nothing.
func brainSection(w http.ResponseWriter, token string) {
	b := config.Get().Brain
	mode := modeName(b.Mode)
	// part opens an element that belongs to some ways of answering; hidden for the others from the start,
	// so the page is right before its script runs, or without it.
	part := func(tag, modes, extra string) string {
		hide := ""
		if !strings.Contains(" "+modes+" ", " "+mode+" ") {
			hide = " hidden"
		}
		return fmt.Sprintf(`<%s data-modes="%s"%s%s>`, tag, modes, hide, extra)
	}

	fmt.Fprint(w, `<fieldset><legend>Voice assistant</legend><form method="post" action="/setup/save">`)
	hidden(w, token, "brain", "sound")
	fmt.Fprintf(w, `<label for="mode">Answered by</label>
	 <select id="mode" name="mode">
	  <option value=""%s>Home Assistant (its Assist pipeline)</option>
	  <option value="direct"%s>Speech and a chat model, directly</option>
	  <option value="realtime"%s>Live conversation (OpenAI Realtime), which can be interrupted</option>
	  <option value="live"%s>Live voice (OpenAI GPT-Live), its own voice, both talking at once (experimental)</option>
	 </select>`,
		selected(b.Mode == config.BrainHomeAssistant), selected(b.Mode == config.BrainDirect),
		selected(b.Mode == config.BrainRealtime), selected(b.Mode == config.BrainLive))

	// What each way is, in a few lines.
	fmt.Fprint(w, part("p", "ha", ` class="note"`),
		`Home Assistant listens, thinks and answers; this device only carries the audio. Nothing here needs setting.</p>`)
	fmt.Fprint(w, part("p", "direct", ` class="note"`),
		`The device needs no Home Assistant: what is said goes to a speech-to-text server, the words to a chat model
	  that can set timers and alarms, play the radio and call other devices, and the answer to a text-to-speech
	  server. The servers are Wyoming ones, as Home Assistant uses (faster-whisper, Piper), and any chat model with
	  an OpenAI-style endpoint (llama.cpp, Ollama) will do.</p>`)
	fmt.Fprintf(w, `%sLive conversation sends what is said to OpenAI, which listens, decides when you have finished and
	  writes the answer as it goes; the text-to-speech server below speaks it, in the voice you choose, and talking
	  over it stops it. %s</p>`, part("p", "realtime", ` class="note"`), houseNote())
	fmt.Fprintf(w, `%sLive voice is OpenAI's full-duplex model: it listens while it talks, answers in one of its own
	  voices and can be talked over mid-sentence. It needs only an OpenAI key. It is billed for the time a
	  conversation is open, about five cents a minute, and the device closes it soon after the conversation ends.
	  Sound from the speaker can be mistaken for you, so it works best at a lower volume. %s</p>`,
		part("p", "live", ` class="note"`), houseNote())

	// Live voice.
	fmt.Fprintf(w, `%s<label for="livevoice">The model's voice</label>
	 <select id="livevoice" name="live_voice">%s</select>
	 <p><label><input type="checkbox" name="live_no_prompt" value="yes" style="width:auto"%s> Ignore &ldquo;Anything the
	  assistant should know&rdquo; below (to hear the model without its persona)</label></p>
	 <details><summary>Models</summary>
	  <label for="livemodel">Voice model (leave empty for %s)</label>
	  <input id="livemodel" name="live_model" value="%s" autocomplete="off">
	  <label for="livebackend">The model that does the thinking and the tools (leave empty for %s)</label>
	  <input id="livebackend" name="live_backend" value="%s" autocomplete="off">
	 </details></div>`,
		part("div", "live", ""), liveVoiceOptions(b.LiveVoice), checkedIf(b.LiveNoPrompt),
		live.DefaultModel, html.EscapeString(b.LiveModel), live.DefaultBackend, html.EscapeString(b.LiveBackend))

	// Servers and voices of the other ways.
	fmt.Fprintf(w, `%s<label for="stt">Speech to text (host:port)</label>
	 <input id="stt" name="stt" value="%s" placeholder="192.168.1.20:10300" autocomplete="off"></div>`,
		part("div", "direct", ""), html.EscapeString(b.STT))
	fmt.Fprintf(w, `%s<label for="tts">Text to speech (host:port)</label>
	 <input id="tts" name="tts" value="%s" placeholder="192.168.1.20:10200" autocomplete="off">
	 <label for="voice">Voice</label>
	 <input id="voice" name="voice" value="%s" placeholder="en_US-lessac-medium" autocomplete="off">
	 <label for="language">Language</label>
	 <input id="language" name="language" value="%s" placeholder="en" maxlength="8" autocomplete="off"></div>`,
		part("div", "direct realtime", ""), html.EscapeString(b.TTS), html.EscapeString(b.Voice), html.EscapeString(b.Language))
	fmt.Fprintf(w, `%s<label for="llm">Chat model endpoint</label>
	 <input id="llm" name="llm" value="%s" placeholder="http://192.168.1.20:8080/v1" autocomplete="off"></div>`,
		part("div", "direct", ""), html.EscapeString(b.LLM))
	fmt.Fprintf(w, `%s<label for="model"><span data-modes="direct"%s>Chat model (only if the server has several)</span><span
	  data-modes="realtime"%s>Realtime model (empty is %s)</span></label>
	 <input id="model" name="model" value="%s" autocomplete="off"></div>`,
		part("div", "direct realtime", ""), hideUnless(mode, "direct"), hideUnless(mode, "realtime"),
		config.DefaultRealtimeModel, html.EscapeString(b.Model))

	// The key, and what all three ways that have one share.
	fmt.Fprintf(w, `%s<label for="key"><span data-modes="direct"%s>Key for the chat endpoint, if it needs one</span><span
	  data-modes="realtime live"%s>Your OpenAI key</span></label>
	 <input id="key" name="key" type="password" value="" placeholder="%s" autocomplete="off">
	 <p><label><input type="checkbox" name="nokey" value="yes" style="width:auto"> Remove the key</label></p></div>`,
		part("div", "direct realtime live", ""), hideUnless(mode, "direct"), hideUnless(mode, "realtime live"), keyHint(b.Key != ""))
	fmt.Fprintf(w, `%s<label for="search">Web search (a SearXNG server)</label>
	 <input id="search" name="search" value="%s" placeholder="http://192.168.1.20:8888" autocomplete="off">
	 <p class="note">To look up what the model cannot know: games, news, opening hours. SearXNG needs its JSON
	  format turned on (search.formats in its settings.yml). Empty: no looking things up.</p></div>`,
		part("div", "direct realtime live", ""), html.EscapeString(b.Search))
	fmt.Fprintf(w, `%s<label for="prompt">Anything the assistant should know</label>
	 <textarea id="prompt" name="prompt" rows="3" maxlength="2000">%s</textarea></div>`,
		part("div", "direct realtime live", ""), html.EscapeString(b.Prompt))

	fmt.Fprint(w, `<p><button type="submit">Save</button></p></form>
	 <script>(function(){var sel=document.getElementById('mode');
	  function apply(){var m=sel.value||'ha';
	   document.querySelectorAll('[data-modes]').forEach(function(e){
	    e.hidden=(' '+e.getAttribute('data-modes')+' ').indexOf(' '+m+' ')<0;});}
	  sel.addEventListener('change',apply);apply();})();</script></fieldset>`)
}

// modeName is the name a way of answering goes by in the form's parts; Home Assistant's mode is empty.
func modeName(m config.BrainMode) string {
	if m == config.BrainHomeAssistant {
		return "ha"
	}
	return string(m)
}

// hideUnless is the attribute that hides a part from every way of answering but the ones named.
func hideUnless(mode, modes string) string {
	if strings.Contains(" "+modes+" ", " "+mode+" ") {
		return ""
	}
	return " hidden"
}

// houseNote says whether live conversation can reach the house. Its tools (lights, music, scripts) use the
// address and token Home Assistant gave the device when it was paired, and the devices the owner exposed
// there to Home Assistant's own voice assistant.
func houseNote() string {
	if hass.Get().Ready() {
		return "The house is connected: it can look at and operate whatever is exposed to Home Assistant's voice assistant."
	}
	return "The house is not connected, so it cannot look at or operate devices; Home Assistant gives the device its address and token when it is paired."
}

// liveVoiceOptions are the choices for the model's voice, with the one in force selected. A name that is
// not in the list (typed in an earlier version, or one the server has since allowed) stays a choice, so
// opening the page and saving it does not change the voice.
func liveVoiceOptions(cur string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<option value=""%s>Default (%s)</option>`, selected(cur == ""), live.DefaultVoice)
	known := cur == ""
	for _, v := range live.Voices {
		known = known || v == cur
		fmt.Fprintf(&sb, `<option value="%s"%s>%s</option>`, v, selected(v == cur), v)
	}
	if !known {
		fmt.Fprintf(&sb, `<option value="%s" selected>%s (not in the list)</option>`,
			html.EscapeString(cur), html.EscapeString(cur))
	}
	return sb.String()
}

func keyHint(set bool) string {
	if set {
		return "set; leave empty to keep it"
	}
	return "none"
}

// saveBrain keeps the form, refusing what could not work rather than keeping it to fail at the next
// wake word.
func saveBrain(r *http.Request) string {
	v := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	b := config.Brain{
		Mode:     config.BrainMode(v("mode")),
		STT:      v("stt"),
		TTS:      v("tts"),
		Voice:    v("voice"),
		Language: v("language"),
		LLM:      strings.TrimRight(v("llm"), "/"),
		Model:    v("model"),
		Search:   strings.TrimRight(v("search"), "/"),
		Prompt:   strings.TrimSpace(r.PostFormValue("prompt")),

		LiveVoice:    v("live_voice"),
		LiveModel:    v("live_model"),
		LiveBackend:  v("live_backend"),
		LiveNoPrompt: r.PostFormValue("live_no_prompt") == "yes",
	}
	if b.Mode != config.BrainHomeAssistant && b.Mode != config.BrainDirect && b.Mode != config.BrainRealtime && b.Mode != config.BrainLive {
		return "that is not a way of answering this device knows"
	}
	for _, hp := range []struct{ what, v string }{{"speech to text", b.STT}, {"text to speech", b.TTS}} {
		if hp.v == "" {
			continue
		}
		if _, port, err := net.SplitHostPort(hp.v); err != nil || port == "" {
			return hp.what + " should be host:port, like 192.168.1.20:10300"
		}
	}
	if b.LLM != "" {
		u, err := url.Parse(b.LLM)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "the chat model endpoint should be an address like http://192.168.1.20:8080/v1"
		}
	}
	if b.Search != "" {
		u, err := url.Parse(b.Search)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "the search server should be an address like http://192.168.1.20:8888"
		}
	}
	if b.Mode == config.BrainDirect && (b.STT == "" || b.TTS == "" || b.LLM == "") {
		return "answering directly needs all three: speech to text, text to speech and the chat model"
	}
	if b.Mode == config.BrainRealtime {
		// A key typed now, or one already kept and not being removed.
		haveKey := strings.TrimSpace(r.PostFormValue("key")) != "" ||
			(config.Get().Brain.Key != "" && r.PostFormValue("nokey") != "yes")
		if b.TTS == "" || !haveKey {
			return "live conversation needs a text-to-speech server and a key"
		}
	}
	if b.Mode == config.BrainLive {
		haveKey := strings.TrimSpace(r.PostFormValue("key")) != "" ||
			(config.Get().Brain.Key != "" && r.PostFormValue("nokey") != "yes")
		if !haveKey {
			return "live voice needs an OpenAI key"
		}
	}
	if strings.ContainsAny(b.Voice+b.Language+b.Model+b.LiveVoice+b.LiveModel+b.LiveBackend, "\r\n") {
		return "the voice, language and model are one line each"
	}
	if err := config.Set().Brain().Set(b); err != nil {
		return "could not save it: " + err.Error()
	}
	switch key := strings.TrimSpace(r.PostFormValue("key")); {
	case r.PostFormValue("nokey") == "yes":
		if err := config.Set().Brain().SetKey(""); err != nil {
			return "could not remove the key: " + err.Error()
		}
	case key != "":
		if strings.ContainsAny(key, "\r\n") {
			return "a key is one line"
		}
		if err := config.Set().Brain().SetKey(key); err != nil {
			return "could not save the key: " + err.Error()
		}
	}
	slog.Info("setup page: the voice assistant was set", "mode", b.Mode, "stt", b.STT, "tts", b.TTS, "llm", b.LLM)
	return ""
}
