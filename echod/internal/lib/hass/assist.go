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
