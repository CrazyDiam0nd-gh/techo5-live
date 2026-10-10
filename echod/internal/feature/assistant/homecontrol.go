package assistant

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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
			"it takes is in your instructions. Give the fields it asks for; leave out the ones it says to leave out. The answer is " +
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

		{llm.Tool{Name: "home_control", Description: "Operate a device in the house. device is words from its name or room " +
			"(like 'kitchen island', 'bedroom blind'); action is one of: turn_on, turn_off, toggle, set_brightness (value 0 to 100), " +
			"set_color (color), open, close, stop, set_position (value 0 to 100), set_temperature (value), set_volume (value 0 to 100), " +
			"play, pause, next, previous, start, return_home, press, set_speed (value 0 to 100). A scene or script is run with turn_on. " +
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
func scriptCatalog() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := hass.Get().ExposedEntities(ctx)
	if err != nil {
		return ""
	}
	var lines []string
	size := 0
	for _, e := range list {
		if e.Domain != "script" {
			continue
		}
		info, err := hass.Get().ScriptInfo(strings.TrimPrefix(e.ID, "script."))
		if err != nil {
			info = hass.ScriptInfo{Name: e.Name}
		}
		line := scriptLine(e.Name, info)
		if size += len(line); size > 6000 {
			break
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return "Scripts you can run with home_run_script (name: what it is for. Fields):\n" + strings.Join(lines, "\n")
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
