//go:build live

package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/lib/hass"
)

// Reads a real Home Assistant, to see the status tool answer the way a person would ask. Read only:
// nothing is commanded.
//
//	HA_URL=http://host:8123 HA_TOKEN_FILE=path go test -tags live -run TestLiveHome -v ./internal/feature/assistant/
func TestLiveHome(t *testing.T) {
	url, tokenFile := os.Getenv("HA_URL"), os.Getenv("HA_TOKEN_FILE")
	if url == "" || tokenFile == "" {
		t.Skip("HA_URL and HA_TOKEN_FILE are not set")
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	hass.Path = filepath.Join(t.TempDir(), "hass.json")
	if err := hass.Get().Set(url, strings.TrimSpace(string(token))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	list, err := hass.Get().ExposedEntities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d exposed devices in %v", len(list), time.Since(start))
	for _, q := range []string{"", "kitchen lights", "thermostat", "bedroom blind", "front door", "home theater"} {
		t.Logf("--- %q\n%s", q, homeStatus(list, q))
	}
}
