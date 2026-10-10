package assistant

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/lib/hass"
	"github.com/HuskerMinion/techo5/echod/internal/lib/llm"
)

// Home Assistant devices, for a voice turn: ask how they are, and tell them what to do. Only what the
// user has exposed to Home Assistant's own voice assistant is visible, so exposing a device there is
// what lets this reach it and un-exposing it is what stops it. Locks and alarm panels can be read but
// never commanded from here: a mis-heard word must not open a door.

// homeTimeout bounds one tool call, which a spoken turn waits on.
const homeTimeout = 8 * time.Second

// scriptTimeout bounds a script, which can be asked to search a music library.
const scriptTimeout = 25 * time.Second

// maxLines bounds what a status answer lists, which the model has to read out.
const maxLines = 20

func homeTools() []tool {
	if !hass.Get().Ready() {
		return nil
	}
	return []tool{
		{llm.Tool{Name: "home_status", Description: "Look up devices in the house and say how they are: lights, switches, " +
			"blinds, fans, the thermostat, sensors, media players, the vacuum, the alarm. Matches words from the device's " +
			"name, its room or its kind, like 'kitchen lights', 'bedroom blind', 'temperature' or 'front door'. " +
			"With no query it counts what is available.",
			Parameters: object(map[string]any{"query": str("Words to look for in a device's name, room or kind; empty for an overview.")})},
			func(a map[string]any) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), homeTimeout)
				defer cancel()
				list, err := hass.Get().ExposedEntities(ctx)
				if err != nil {
					return "", err
				}
				return homeStatus(list, argString(a, "query")), nil
			}},

		{llm.Tool{Name: "home_run_script", Description: "Run one of the house's scripts, which do things the other tools cannot: " +
			"play music, change the home theater volume, start a computer. The list of scripts, what each one is for and the fields " +
			"it takes is in your instructions. Give the fields it asks for; leave out the ones it says to leave out. For music, " +
			"run a script only when the person names a speaker it knows; with none named, use home_play_music. The answer is " +
			"what the script said happened: say that, and do not claim more.",
			Parameters: object(map[string]any{
				"script": str("The script's name, like 'Play playlist'."),
				"fields": map[string]any{"type": "object", "description": "The script's fields, by name."},
			}, "script")},
			func(a map[string]any) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
				defer cancel()
				list, err := hass.Get().ExposedEntities(ctx)
				if err != nil {
					return "", err
				}
				fields, _ := a["fields"].(map[string]any)
				return runScript(list, argString(a, "script"), fields, hass.Get().RunScript)
			}},

		{llm.Tool{Name: "home_play_music", Description: "Play music on any speaker or computer in the house through Music Assistant, " +
			"by name: an artist, album, track or playlist. Use this when the person names a player the music scripts do not know (the " +
			"scripts know the Dot and the home theater); for those, use the scripts. player is words from the media player's name; " +
			"leave it out to play on the device that is being talked to.",
			Parameters: object(map[string]any{
				"name":    str("The artist, album, track or playlist to play."),
				"kind":    str("artist, album, track or playlist. Artist for 'play X' or 'songs by X'."),
				"player":  str("Words from the name of the media player to play it on, like 'office pc'."),
				"shuffle": map[string]any{"type": "boolean", "description": "Only if the person says shuffle."},
			}, "name")},
			func(a map[string]any) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
				defer cancel()
				list, err := hass.Get().ExposedEntities(ctx)
				if err != nil {
					return "", err
				}
				shuffle, _ := a["shuffle"].(bool)
				player := argString(a, "player")
				if player == "" {
					player = config.Get().Device.Name // the device that heard the request
				}
				out, err := playOnPlayer(list, argString(a, "name"), argString(a, "kind"), player, shuffle, hass.Get().Service)
				if err != nil && slowAnswer(err) {
					// Music Assistant searches its library before it plays, which can outlast the wait. That is not a
					// failure to report: the music may start in a moment.
					return "Music Assistant has not answered yet. It may still start playing, so do not say it failed: say it is taking a while.", nil
				}
				return out, err
			}},

		{llm.Tool{Name: "home_move_music", Description: "Move the music that is playing to another speaker or computer, " +
			"carrying on where it was: 'move the music to the home theater', 'bring it here', 'send this to the office PC'. " +
			"to is words from the player's name; leave it out to bring the music to the device being talked to. " +
			"from is the player it is on, only needed when more than one is playing.",
			Parameters: object(map[string]any{
				"to":   str("Words from the name of the player to move the music to; empty for this device."),
				"from": str("Words from the name of the player it is playing on; empty for whichever is playing."),
			})},
			func(a map[string]any) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
				defer cancel()
				list, err := hass.Get().ExposedEntities(ctx)
				if err != nil {
					return "", err
				}
				return moveMusic(list, argString(a, "to"), argString(a, "from"), config.Get().Device.Name, hass.Get().Service)
			}},

		{llm.Tool{Name: "home_weather", Description: "The weather at home from the house's own weather source: how it is now and " +
			"the forecast for the coming days. Use it for any question about the weather at home, today or on a later day.",
			Parameters: object(map[string]any{"days": num("How many days to give, 1 to 7; 3 if not said.")})},
			func(a map[string]any) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), homeTimeout)
				defer cancel()
				list, err := hass.Get().ExposedEntities(ctx)
				if err != nil {
					return "", err
				}
				n, ok := argNumber(a, "days")
				if !ok {
					n = 3
				}
				return homeWeather(list, int(n), time.Now(), hass.Get().Forecast)
			}},

		{llm.Tool{Name: "home_stop_music", Description: "Stop music. With no player named it stops every speaker and computer " +
			"that is playing or paused, wherever it is: use that for 'stop the music', 'stop all music' and 'be quiet'. " +
			"Name a player only when the person names one.",
			Parameters: object(map[string]any{
				"player": str("Words from the name of one media player; empty for all of them."),
			})},
			func(a map[string]any) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), homeTimeout)
				defer cancel()
				list, err := hass.Get().ExposedEntities(ctx)
				if err != nil {
					return "", err
				}
				out, err := stopMusic(list, argString(a, "player"), hass.Get().Service)
				if err == nil && stoppedHere(argString(a, "player"), config.Get().Device.Name) {
					// The stop went through Music Assistant, which this device cannot tell from a pause: the track
					// would stay on the screen with play on it. The room has asked for the music to stop, so it says so.
					media.Get().OnTransport.Emit(media.TransportStop)
				}
				return out, err
			}},

		{llm.Tool{Name: "home_control", Description: "Operate a device in the house. device is words from its name or room " +
			"(like 'kitchen island', 'bedroom blind'); action is one of: turn_on, turn_off, toggle, set_brightness (value 0 to 100), " +
			"set_color (color), open, close, stop, set_position (value 0 to 100), set_temperature (value), set_volume (value 0 to 100), " +
			"play, pause, next, previous, volume_up, volume_down, start, return_home, press, set_speed (value 0 to 100). " +
			"For the volume of any player, including this device, name the player and use set_volume, volume_up or volume_down. " +
			"A scene or script is run with turn_on. " +
			"If several devices match, the answer lists them: ask which one, or set all to true when they said all of them.",
			Parameters: object(map[string]any{
				"device": str("Words from the device's name or room."),
				"action": str("What to do, from the list."),
				"value":  num("A number for brightness, position, temperature, volume or speed."),
				"color":  str("A color name for set_color."),
				"all":    map[string]any{"type": "boolean", "description": "Do it to every device that matches."},
			}, "device", "action")},
			func(a map[string]any) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), homeTimeout)
				defer cancel()
				list, err := hass.Get().ExposedEntities(ctx)
				if err != nil {
					return "", err
				}
				all, _ := a["all"].(bool)
				value, hasValue := argNumber(a, "value")
				return homeControl(list, argString(a, "device"), argString(a, "action"), value, hasValue,
					argString(a, "color"), all, hass.Get().Service)
			}},
	}
}

// homeStatus answers a status question from the exposed list.
func homeStatus(list []hass.Exposed, query string) string {
	if strings.TrimSpace(query) == "" {
		counts := map[string]int{}
		for _, e := range list {
			counts[e.Domain]++
		}
		var parts []string
		for d, n := range counts {
			parts = append(parts, fmt.Sprintf("%d %s", n, d))
		}
		sort.Strings(parts)
		return fmt.Sprintf("%d devices are available: %s. Ask about one by name or room.", len(list), strings.Join(parts, ", "))
	}
	found := match(list, query)
	if len(found) == 0 {
		return "no device matches " + query
	}
	var lines []string
	for i, e := range found {
		if i == maxLines {
			lines = append(lines, fmt.Sprintf("and %d more", len(found)-maxLines))
			break
		}
		lines = append(lines, describe(e))
	}
	return strings.Join(lines, "\n")
}

// describe is a device and its state in words, with the attributes that answer a question about it.
func describe(e hass.Exposed) string {
	s := e.String()
	if unit, _ := e.Attrs["unit_of_measurement"].(string); unit != "" {
		s += " " + unit
	}
	var extra []string
	switch e.Domain {
	case "light":
		if b, ok := e.Attrs["brightness"].(float64); ok && e.State == "on" {
			extra = append(extra, fmt.Sprintf("brightness %d percent", int(b*100/255+0.5)))
		}
	case "cover":
		if p, ok := e.Attrs["current_position"].(float64); ok {
			extra = append(extra, fmt.Sprintf("%d percent open", int(p)))
		}
	case "climate":
		if t, ok := e.Attrs["current_temperature"].(float64); ok {
			extra = append(extra, fmt.Sprintf("now %g", t))
		}
		if t, ok := e.Attrs["temperature"].(float64); ok {
			extra = append(extra, fmt.Sprintf("set to %g", t))
		}
	case "media_player":
		if t, _ := e.Attrs["media_title"].(string); t != "" && e.State == "playing" {
			extra = append(extra, "playing "+t)
		}
		if v, ok := e.Attrs["volume_level"].(float64); ok {
			extra = append(extra, fmt.Sprintf("volume %d percent", int(v*100+0.5)))
		}
	}
	if len(extra) > 0 {
		s += " (" + strings.Join(extra, ", ") + ")"
	}
	return s
}

// match finds exposed devices whose name, room or kind holds every word asked for. A device named
// exactly what was asked for wins outright, so "kitchen island" is not also every kitchen island light.
func match(list []hass.Exposed, query string) []hass.Exposed {
	want := matchWords(query)
	if len(want) == 0 {
		return nil
	}
	norm := strings.Join(want, " ")
	var found []hass.Exposed
	for _, e := range list {
		for _, n := range append([]string{e.Name}, e.Aliases...) {
			if strings.Join(matchWords(n), " ") == norm {
				return []hass.Exposed{e}
			}
		}
		have := map[string]bool{}
		for _, w := range matchWords(e.Name + " " + e.Area + " " + e.Domain + " " + e.ID[len(e.Domain)+1:] + " " + strings.Join(e.Aliases, " ")) {
			have[w] = true
		}
		ok := true
		for _, w := range want {
			if !have[w] {
				ok = false
				break
			}
		}
		if ok {
			found = append(found, e)
		}
	}
	return found
}

// words lowercases, drops punctuation and filler, and makes plurals singular, so "the kitchen lights"
// meets "Kitchen Light 1".
func matchWords(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		switch w {
		case "the", "my", "a", "all", "in", "of", "please":
			continue
		}
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			w = strings.TrimSuffix(w, "s")
		}
		out = append(out, w)
	}
	return out
}

// service is one call to Home Assistant.
type service struct {
	domain  string
	name    string
	data    map[string]any
	summary string
}

// controllable says which services each kind of device may be given, from what the model asks for. A
// kind that is not here, like a lock or the alarm, can be read but not commanded.
func plan(e hass.Exposed, action string, value float64, hasValue bool, color string) (service, error) {
	action = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(action), " ", "_"))
	switch action {
	case "on":
		action = "turn_on"
	case "off":
		action = "turn_off"
	case "brightness", "dim":
		action = "set_brightness"
	case "colour", "set_colour":
		action = "set_color"
	case "resume":
		action = "play"
	}
	need := func() error {
		if !hasValue {
			return fmt.Errorf("%s needs a value", action)
		}
		return nil
	}
	pct := func() (int, error) {
		if err := need(); err != nil {
			return 0, err
		}
		if value < 0 || value > 100 {
			return 0, errors.New("the value must be 0 to 100")
		}
		return int(value + 0.5), nil
	}
	d := e.Domain
	svc := func(name string, data map[string]any, summary string) (service, error) {
		if data == nil {
			data = map[string]any{}
		}
		data["entity_id"] = e.ID
		return service{d, name, data, summary}, nil
	}

	switch d {
	case "light":
		switch action {
		case "turn_on", "turn_off", "toggle":
			return svc(action, nil, action)
		case "set_brightness":
			p, err := pct()
			if err != nil {
				return service{}, err
			}
			return svc("turn_on", map[string]any{"brightness_pct": p}, fmt.Sprintf("set to %d percent", p))
		case "set_color":
			if color == "" {
				return service{}, errors.New("set_color needs a color")
			}
			return svc("turn_on", map[string]any{"color_name": strings.ToLower(color)}, "set to "+color)
		}
	case "switch", "input_boolean":
		switch action {
		case "turn_on", "turn_off", "toggle":
			return svc(action, nil, action)
		}
	case "fan":
		switch action {
		case "turn_on", "turn_off", "toggle":
			return svc(action, nil, action)
		case "set_speed":
			p, err := pct()
			if err != nil {
				return service{}, err
			}
			return svc("set_percentage", map[string]any{"percentage": p}, fmt.Sprintf("speed %d percent", p))
		}
	case "cover":
		switch action {
		case "open", "close", "stop":
			return svc(map[string]string{"open": "open_cover", "close": "close_cover", "stop": "stop_cover"}[action], nil, action)
		case "set_position":
			p, err := pct()
			if err != nil {
				return service{}, err
			}
			return svc("set_cover_position", map[string]any{"position": p}, fmt.Sprintf("moved to %d percent", p))
		}
	case "scene", "script":
		if action == "turn_on" {
			return svc("turn_on", nil, "run")
		}
	case "button":
		if action == "press" || action == "turn_on" {
			return svc("press", nil, "pressed")
		}
	case "climate":
		switch action {
		case "set_temperature":
			if err := need(); err != nil {
				return service{}, err
			}
			if value < 5 || value > 35 {
				return service{}, errors.New("the temperature must be between 5 and 35")
			}
			return svc("set_temperature", map[string]any{"temperature": value}, fmt.Sprintf("set to %g", value))
		case "turn_on", "turn_off":
			return svc(action, nil, action)
		}
	case "media_player":
		switch action {
		case "turn_on", "turn_off":
			return svc(action, nil, action)
		case "play":
			return svc("media_play", nil, "playing")
		case "pause":
			return svc("media_pause", nil, "paused")
		case "stop":
			return svc("media_stop", nil, "stopped")
		case "next":
			return svc("media_next_track", nil, "next track")
		case "previous":
			return svc("media_previous_track", nil, "previous track")
		case "set_volume":
			p, err := pct()
			if err != nil {
				return service{}, err
			}
			return svc("volume_set", map[string]any{"volume_level": float64(p) / 100}, fmt.Sprintf("volume %d percent", p))
		case "volume_up":
			return svc("volume_up", nil, "volume up")
		case "volume_down":
			return svc("volume_down", nil, "volume down")
		}
	case "vacuum":
		switch action {
		case "start", "turn_on":
			return svc("start", nil, "started")
		case "stop", "turn_off":
			return svc("stop", nil, "stopped")
		case "pause":
			return svc("pause", nil, "paused")
		case "return_home":
			return svc("return_to_base", nil, "returning to its dock")
		}
	case "lock", "alarm_control_panel":
		return service{}, fmt.Errorf("%s cannot be commanded by voice, only checked", e.Name)
	default:
		return service{}, fmt.Errorf("%s (%s) can be checked but not controlled", e.Name, d)
	}
	return service{}, fmt.Errorf("%s (%s) cannot be asked to %s", e.Name, d, action)
}

// homeControl does what was asked to the devices it names, through call.
func homeControl(list []hass.Exposed, device, action string, value float64, hasValue bool, color string, all bool,
	call func(domain, service string, data map[string]any) error) (string, error) {
	found := match(list, device)
	switch {
	case len(found) == 0:
		return "", fmt.Errorf("no device matches %q", device)
	case len(found) > 1 && !all:
		var names []string
		for i, e := range found {
			if i == 8 {
				names = append(names, fmt.Sprintf("and %d more", len(found)-8))
				break
			}
			names = append(names, e.Name)
		}
		return "", fmt.Errorf("several devices match: %s. Ask which one, or repeat with all set to true", strings.Join(names, "; "))
	}
	var done, failed []string
	for _, e := range found {
		p, err := plan(e, action, value, hasValue, color)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		if err := call(p.domain, p.name, p.data); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", e.Name, err))
			continue
		}
		done = append(done, fmt.Sprintf("%s %s", e.Name, p.summary))
	}
	hass.ForgetExposed()
	switch {
	case len(done) == 0:
		return "", errors.New(strings.Join(failed, "; "))
	case len(failed) > 0:
		return "done: " + strings.Join(done, "; ") + ". Could not: " + strings.Join(failed, "; "), nil
	}
	return "done: " + strings.Join(done, "; "), nil
}

// runScript runs the exposed script that was named, if exactly one was.
func runScript(list []hass.Exposed, name string, fields map[string]any, run func(object string, fields map[string]any) (string, error)) (string, error) {
	var scripts []hass.Exposed
	for _, e := range list {
		if e.Domain == "script" {
			scripts = append(scripts, e)
		}
	}
	found := match(scripts, name)
	switch {
	case len(found) == 0:
		return "", fmt.Errorf("no script matches %q", name)
	case len(found) > 1:
		var names []string
		for _, e := range found {
			names = append(names, e.Name)
		}
		return "", fmt.Errorf("several scripts match: %s", strings.Join(names, "; "))
	}
	out, err := run(strings.TrimPrefix(found[0].ID, "script."), fields)
	if err != nil {
		return "", fmt.Errorf("%s: %w", found[0].Name, err)
	}
	hass.ForgetExposed()
	return found[0].Name + ": " + out, nil
}

// scriptCatalog tells the model which scripts there are, what each is for and what it takes, read from
// the scripts themselves. A script the user exposes to Home Assistant's voice assistant appears here
// without anything else being set up.
func scriptCatalog() (string, []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := hass.Get().ExposedEntities(ctx)
	if err != nil {
		return "", nil
	}
	var lines, devices []string
	size := 0
	for _, e := range list {
		if e.Domain != "script" {
			continue
		}
		info, err := hass.Get().ScriptInfo(strings.TrimPrefix(e.ID, "script."))
		if err != nil {
			info = hass.ScriptInfo{Name: e.Name}
		}
		for _, f := range info.Fields {
			if strings.EqualFold(f.Name, "device") {
				devices = append(devices, f.Options...)
			}
		}
		line := scriptLine(e.Name, info)
		if size += len(line); size > 6000 {
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "Scripts you can run with home_run_script (name: what it is for. Fields):\n" + strings.Join(lines, "\n"), devices
}

// scriptLine is one script as the model reads it.
func scriptLine(name string, info hass.ScriptInfo) string {
	desc := strings.Join(strings.Fields(info.Description), " ")
	if len(desc) > 300 {
		desc = desc[:300] + "..."
	}
	var fields []string
	for _, f := range info.Fields {
		s := f.Name
		if f.Required {
			s += " (required)"
		}
		if len(f.Options) > 0 {
			s += " [" + strings.Join(f.Options, "|") + "]"
		}
		if d := strings.Join(strings.Fields(f.Description), " "); d != "" {
			s += ": " + d
		}
		fields = append(fields, s)
	}
	line := "- " + name
	if desc != "" {
		line += ": " + desc
	}
	if len(fields) > 0 {
		line += " Fields: " + strings.Join(fields, "; ") + "."
	}
	return line
}

// playOnPlayer starts music on an exposed media player through Music Assistant, which looks the name up in
// its library. It is for the players the house's music scripts do not know.
func playOnPlayer(list []hass.Exposed, name, kind, player string, shuffle bool,
	call func(domain, service string, data map[string]any) error) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("say what to play")
	}
	var players []hass.Exposed
	for _, e := range list {
		if e.Domain == "media_player" {
			players = append(players, e)
		}
	}
	found := match(players, player)
	switch {
	case len(found) == 0:
		return "", fmt.Errorf("no media player matches %q", player)
	case len(found) > 1:
		var names []string
		for _, e := range found {
			names = append(names, e.Name)
		}
		return "", fmt.Errorf("several players match: %s. Ask which", strings.Join(names, "; "))
	}
	switch kind = strings.ToLower(strings.TrimSpace(kind)); kind {
	case "artist", "album", "track", "playlist":
	default:
		kind = "artist"
	}
	target := found[0]
	if err := call("media_player", "shuffle_set", map[string]any{"entity_id": target.ID, "shuffle": shuffle}); err != nil {
		return "", fmt.Errorf("%s: %w", target.Name, err)
	}
	if err := call("music_assistant", "play_media", map[string]any{
		"entity_id": target.ID, "media_id": name, "media_type": kind, "enqueue": "replace"}); err != nil {
		return "", fmt.Errorf("%s: %w", target.Name, err)
	}
	hass.ForgetExposed()
	return fmt.Sprintf("started %s %q on %s", kind, name, target.Name), nil
}

// stopMusic stops the media players that are playing or paused, or the one that was named. Asked to stop
// the music, nobody means one speaker of several.
func stopMusic(list []hass.Exposed, player string, call func(domain, service string, data map[string]any) error) (string, error) {
	var players []hass.Exposed
	for _, e := range list {
		if e.Domain == "media_player" {
			players = append(players, e)
		}
	}
	var targets []hass.Exposed
	if strings.TrimSpace(player) != "" {
		targets = match(players, player)
		if len(targets) == 0 {
			return "", fmt.Errorf("no media player matches %q", player)
		}
	} else {
		for _, e := range players {
			switch e.State {
			case "playing", "paused", "buffering":
				targets = append(targets, e)
			}
		}
		if len(targets) == 0 {
			return "nothing is playing", nil
		}
	}
	var stopped, failed []string
	for _, e := range targets {
		if err := call("media_player", "media_stop", map[string]any{"entity_id": e.ID}); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", e.Name, err))
			continue
		}
		stopped = append(stopped, e.Name)
	}
	hass.ForgetExposed()
	if len(stopped) == 0 {
		return "", errors.New(strings.Join(failed, "; "))
	}
	out := "stopped: " + strings.Join(stopped, ", ")
	if len(failed) > 0 {
		out += ". Could not stop: " + strings.Join(failed, "; ")
	}
	return out, nil
}

// moveMusic carries the queue and its place to another player through Music Assistant. here is the
// player of the device that heard the request, for "bring it here".
func moveMusic(list []hass.Exposed, to, from, here string,
	call func(domain, service string, data map[string]any) error) (string, error) {
	var players []hass.Exposed
	for _, e := range list {
		if e.Domain == "media_player" {
			players = append(players, e)
		}
	}
	if strings.TrimSpace(to) == "" {
		to = here
	}
	dest := match(players, to)
	switch {
	case len(dest) == 0:
		return "", fmt.Errorf("no media player matches %q", to)
	case len(dest) > 1:
		var names []string
		for _, e := range dest {
			names = append(names, e.Name)
		}
		return "", fmt.Errorf("several players match: %s. Ask which", strings.Join(names, "; "))
	}

	var src []hass.Exposed
	if strings.TrimSpace(from) != "" {
		src = match(players, from)
	} else {
		for _, e := range players {
			if e.ID != dest[0].ID && (e.State == "playing" || e.State == "paused" || e.State == "buffering") {
				src = append(src, e)
			}
		}
	}
	switch {
	case len(src) == 0:
		return "", errors.New("nothing is playing to move")
	case len(src) > 1:
		var names []string
		for _, e := range src {
			names = append(names, e.Name)
		}
		return "", fmt.Errorf("music is playing on several players: %s. Ask which to move", strings.Join(names, "; "))
	case src[0].ID == dest[0].ID:
		return "", fmt.Errorf("the music is already on %s", dest[0].Name)
	}

	if err := call("music_assistant", "transfer_queue", map[string]any{
		"entity_id": dest[0].ID, "source_player": src[0].ID, "auto_play": true}); err != nil {
		return "", fmt.Errorf("%s to %s: %w", src[0].Name, dest[0].Name, err)
	}
	hass.ForgetExposed()
	return fmt.Sprintf("moved the music from %s to %s", src[0].Name, dest[0].Name), nil
}

// homeWeather reads the weather the house has: its exposed weather entity, now and the days ahead.
func homeWeather(list []hass.Exposed, days int, now time.Time, forecast func(entity string) ([]hass.Day, error)) (string, error) {
	var w *hass.Exposed
	for i := range list {
		if list[i].Domain == "weather" {
			w = &list[i]
			break
		}
	}
	if w == nil {
		return "", errors.New("the house has no weather source exposed to the voice assistant")
	}
	days = min(max(days, 1), 7)
	unit, _ := w.Attrs["temperature_unit"].(string)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Now: %s", strings.ReplaceAll(w.State, "-", " "))
	if t, ok := w.Attrs["temperature"].(float64); ok {
		fmt.Fprintf(&sb, ", %g%s", t, unit)
	}
	if h, ok := w.Attrs["humidity"].(float64); ok {
		fmt.Fprintf(&sb, ", humidity %g percent", h)
	}
	sb.WriteString(".")
	fc, err := forecast(w.ID)
	if err != nil || len(fc) == 0 {
		return sb.String(), nil
	}
	today := now.Format("2006-01-02")
	shown := 0
	for _, d := range fc {
		if shown == days {
			break
		}
		name := d.When.Format("Monday")
		switch day := d.When.Format("2006-01-02"); {
		case day < today:
			continue
		case day == today:
			name = "Today"
		case day == now.AddDate(0, 0, 1).Format("2006-01-02"):
			name = "Tomorrow"
		}
		fmt.Fprintf(&sb, "\n%s: %s, high %g%s, low %g%s", name, strings.ReplaceAll(d.Condition, "-", " "), d.High, unit, d.Low, unit)
		if d.Rain >= 0 {
			fmt.Fprintf(&sb, ", %d percent chance of rain", d.Rain)
		}
		shown++
	}
	return sb.String(), nil
}

// stoppedHere is whether a stop of the player named (all of them, when none is) includes this device's own.
func stoppedHere(player, device string) bool {
	player, device = strings.ToLower(strings.TrimSpace(player)), strings.ToLower(strings.TrimSpace(device))
	if player == "" {
		return true
	}
	return device != "" && (strings.Contains(device, player) || strings.Contains(player, device))
}

// slowAnswer is whether an error is the wait running out rather than the request being refused.
func slowAnswer(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "deadline exceeded") || strings.Contains(m, "client.timeout") || strings.Contains(m, "timeout")
}
