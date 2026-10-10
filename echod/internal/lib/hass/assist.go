package hass

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Exposed is an entity the user has exposed to Home Assistant's own voice assistant (Settings, Voice
// assistants, Expose). It is the list the assistant may read and act on here, and nothing outside it:
// exposing a device in Home Assistant is how the user grants access, and un-exposing it is how they
// take it away.
type Exposed struct {
	ID     string // light.kitchen_island
	Domain string // light
	Name   string // Kitchen Island
	Area   string // Kitchen, or empty
	// Aliases are the other names the user gave it for voice, in Home Assistant's entity settings.
	Aliases []string
	State   string
	Attrs   map[string]any
}

// exposedFor is how long a read of the list is reused: long enough that a turn's several tool calls
// share one, short enough that a device exposed a moment ago is found.
const exposedFor = 30 * time.Second

type exposedCache struct {
	mu   sync.Mutex
	at   time.Time
	list []Exposed
}

var exposure exposedCache

// ExposedEntities lists what is exposed to the "conversation" assistant, with names, rooms and states.
func (c *Client) ExposedEntities(ctx context.Context) ([]Exposed, error) {
	exposure.mu.Lock()
	defer exposure.mu.Unlock()
	if time.Since(exposure.at) < exposedFor && exposure.list != nil {
		return exposure.list, nil
	}

	raw, err := c.wsCall(ctx, map[string]any{"type": "homeassistant/expose_entity/list"})
	if err != nil {
		return nil, err
	}
	var ex struct {
		Entities map[string]map[string]bool `json:"exposed_entities"`
	}
	if err := json.Unmarshal(raw, &ex); err != nil {
		return nil, err
	}

	out, err := c.do("GET", "/api/states", nil)
	if err != nil {
		return nil, err
	}
	var states []struct {
		ID    string         `json:"entity_id"`
		State string         `json:"state"`
		Attrs map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(out, &states); err != nil {
		return nil, err
	}

	areas, aliases := c.registryNames(ctx)

	var list []Exposed
	for _, s := range states {
		if !ex.Entities[s.ID]["conversation"] {
			continue
		}
		name, _ := s.Attrs["friendly_name"].(string)
		if name == "" {
			name = strings.ReplaceAll(s.ID[strings.Index(s.ID, ".")+1:], "_", " ")
		}
		list = append(list, Exposed{
			ID: s.ID, Domain: s.ID[:strings.Index(s.ID, ".")], Name: name,
			Area: areas[s.ID], Aliases: aliases[s.ID], State: s.State, Attrs: s.Attrs,
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	exposure.at, exposure.list = time.Now(), list
	return list, nil
}

// registryNames maps entity ids to their room (the entity's own, else its device's) and to the aliases
// the user gave them for voice. A failure gives empty maps, so a device is still found by its name when
// the registries cannot be read.
func (c *Client) registryNames(ctx context.Context) (names map[string]string, aliases map[string][]string) {
	names = map[string]string{}
	aliases = map[string][]string{}
	var areas []struct {
		ID   string `json:"area_id"`
		Name string `json:"name"`
	}
	var devices []struct {
		ID     string `json:"id"`
		AreaID string `json:"area_id"`
	}
	var entities []struct {
		ID       string   `json:"entity_id"`
		DeviceID string   `json:"device_id"`
		AreaID   string   `json:"area_id"`
		Aliases  []string `json:"aliases"`
	}
	for _, q := range []struct {
		cmd string
		to  any
	}{{"config/area_registry/list", &areas}, {"config/device_registry/list", &devices}, {"config/entity_registry/list", &entities}} {
		raw, err := c.wsCall(ctx, map[string]any{"type": q.cmd})
		if err != nil || json.Unmarshal(raw, q.to) != nil {
			return names, aliases
		}
	}
	area := map[string]string{}
	for _, a := range areas {
		area[a.ID] = a.Name
	}
	deviceArea := map[string]string{}
	for _, d := range devices {
		deviceArea[d.ID] = area[d.AreaID]
	}
	for _, e := range entities {
		if len(e.Aliases) > 0 {
			aliases[e.ID] = e.Aliases
		}
		if n := area[e.AreaID]; n != "" {
			names[e.ID] = n
		} else if n := deviceArea[e.DeviceID]; n != "" {
			names[e.ID] = n
		}
	}
	return names, aliases
}

// Service calls a Home Assistant service on entities.
func (c *Client) Service(domain, service string, data map[string]any) error {
	_, err := c.do("POST", "/api/services/"+domain+"/"+service, data)
	return err
}

// ForgetExposed drops the cached list, for a test or after a change this device made itself.
func ForgetExposed() {
	exposure.mu.Lock()
	exposure.at, exposure.list = time.Time{}, nil
	exposure.mu.Unlock()
}

// String is a device as a line the model reads.
func (e Exposed) String() string {
	room := ""
	if e.Area != "" {
		room = ", " + e.Area
	}
	return fmt.Sprintf("%s (%s%s): %s", e.Name, e.Domain, room, e.State)
}

// ScriptField is one input a script takes, as the script describes it.
type ScriptField struct {
	Name        string
	Description string
	Required    bool
	Options     []string
}

// ScriptInfo is what a script says about itself, for a voice assistant to read before it runs one.
type ScriptInfo struct {
	Name        string
	Description string
	Fields      []ScriptField
}

// ScriptInfo reads a script's own description and fields from its configuration. id is the part of the
// entity id after "script.".
func (c *Client) ScriptInfo(id string) (ScriptInfo, error) {
	out, err := c.do("GET", "/api/config/script/config/"+id, nil)
	if err != nil {
		return ScriptInfo{}, err
	}
	var cfg struct {
		Alias       string `json:"alias"`
		Description string `json:"description"`
		Fields      map[string]struct {
			Description string `json:"description"`
			Required    bool   `json:"required"`
			Selector    struct {
				Select struct {
					Options []json.RawMessage `json:"options"`
				} `json:"select"`
			} `json:"selector"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return ScriptInfo{}, err
	}
	info := ScriptInfo{Name: cfg.Alias, Description: cfg.Description}
	names := make([]string, 0, len(cfg.Fields))
	for n := range cfg.Fields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := cfg.Fields[n]
		field := ScriptField{Name: n, Description: f.Description, Required: f.Required}
		for _, o := range f.Selector.Select.Options {
			var s string
			var v struct {
				Value string `json:"value"`
			}
			if json.Unmarshal(o, &s) == nil {
				field.Options = append(field.Options, s)
			} else if json.Unmarshal(o, &v) == nil && v.Value != "" {
				field.Options = append(field.Options, v.Value)
			}
		}
		info.Fields = append(info.Fields, field)
	}
	return info, nil
}

// RunScript runs a script with its fields and waits for it to finish, returning what it said back: the
// response a script gives with "stop", as Home Assistant's own voice assistant reads it, or "done".
// object is the part of the entity id after "script.".
func (c *Client) RunScript(object string, fields map[string]any) (string, error) {
	if fields == nil {
		fields = map[string]any{}
	}
	out, err := c.do("POST", "/api/services/script/"+object+"?return_response", fields)
	if err != nil {
		return "", err
	}
	var resp struct {
		ServiceResponse any `json:"service_response"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.ServiceResponse == nil {
		return "done", nil
	}
	b, _ := json.Marshal(resp.ServiceResponse)
	if s := string(b); s != "{}" && s != "null" && s != `""` {
		return s, nil
	}
	return "done", nil
}
