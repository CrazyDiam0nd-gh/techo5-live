package assistant

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/feature/home"
	"github.com/HuskerMinion/techo5/echod/internal/feature/voice"
	"github.com/HuskerMinion/techo5/echod/internal/lib/llm"
)

// screen puts a page up on the device's screen - "weather", "radar" or "calendar" - and says whether it
// could. Set by the display on a device that has a screen; without one the tool is not offered.
var screen func(page string) bool

// SetScreen is how the display offers its pages.
func SetScreen(fn func(page string) bool) { screen = fn }

func screenTools() []tool {
	if screen == nil {
		return nil
	}
	return []tool{{llm.Tool{Name: "show_on_screen", Description: "Show a page on this device's screen: the weather forecast, the rain map (radar), or the calendar. The forecast and the radar are for this device's own location only.",
		Parameters: object(map[string]any{"page": map[string]any{"type": "string", "enum": []string{"weather", "radar", "calendar"}}}, "page")},
		func(a map[string]any) (string, error) {
			page := argString(a, "page")
			if !screen(page) {
				return "", errors.New("that page cannot be shown here")
			}
			voice.Get().LookHere()
			return "the " + page + " page is going up on the screen once you have answered", nil
		}}}
}

// showCamera puts a camera up on the screen, for as long as the display keeps one. Set by the display on
// a device that has a screen; without one the tool is not offered.
var showCamera func(entity string)

// SetCameraShower is how the display offers its camera view.
func SetCameraShower(fn func(entity string)) { showCamera = fn }

func cameraTools() []tool {
	if showCamera == nil {
		return nil
	}
	return []tool{{llm.Tool{Name: "show_camera", Description: "Show a camera's live picture on this device's screen for a little while: the front door, the doorbell, the garden, an alley, or this device's own camera. Say which by its name. If the name is not one the device knows it answers with the cameras it has.",
		Parameters: object(map[string]any{"camera": map[string]any{"type": "string", "description": "the camera's name, like front door or alley"}}, "camera")},
		func(a map[string]any) (string, error) {
			name, err := showCameraNamed(argString(a, "camera"), home.Get().Cameras(), showCamera)
			if err != nil {
				return "", err
			}
			voice.Get().LookHere()
			return "the " + name + " camera is going up on the screen once you have answered", nil
		}}}
}

// showCameraNamed shows the camera the person meant and says which it was, or says why it cannot.
func showCameraNamed(query string, cams []config.Camera, show func(entity string)) (string, error) {
	if len(cams) == 0 {
		return "", errors.New("there are no cameras: Home Assistant has none to show, or the device is not connected to it")
	}
	c, ok := pickCamera(query, cams)
	if !ok {
		names := make([]string, len(cams))
		for i, c := range cams {
			names[i] = c.Name
		}
		return "", fmt.Errorf("no camera is called %q; the cameras are: %s", query, strings.Join(names, ", "))
	}
	show(c.Entity)
	return c.Name, nil
}

// cameraWords are the words in what was said that name nothing.
var cameraWords = map[string]bool{"the": true, "a": true, "my": true, "camera": true, "cam": true, "view": true, "of": true, "on": true, "show": true, "me": true}

func nameWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// significant is the words of a name that tell one camera from another.
func significant(s string) []string {
	var out []string
	for _, w := range nameWords(s) {
		if !cameraWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// pickCamera is the camera a name asked for: the one called exactly that, or the only one whose name (or
// entity) holds every word that was said. Two that fit are not guessed between.
func pickCamera(query string, cams []config.Camera) (config.Camera, bool) {
	want := significant(query)
	if len(want) == 0 {
		return config.Camera{}, false
	}
	var fits []config.Camera
	for _, c := range cams {
		have := map[string]bool{}
		for _, w := range append(nameWords(c.Name), nameWords(c.Entity)...) {
			have[w] = true
		}
		all := true
		for _, w := range want {
			all = all && have[w]
		}
		if !all {
			continue
		}
		if strings.Join(significant(c.Name), " ") == strings.Join(want, " ") {
			return c, true // called exactly that, "Alley Camera" being the same as "alley"
		}
		fits = append(fits, c)
	}
	if len(fits) == 1 {
		return fits[0], true
	}
	return config.Camera{}, false
}
