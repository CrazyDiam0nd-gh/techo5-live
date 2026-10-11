package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
	"github.com/HuskerMinion/techo5/echod/internal/lib/llm"
)

// Asking a bridge: a service on the household's network (config.Brain.Bridge) that does what the model
// cannot - arithmetic, conversions, dates, the time or weather elsewhere, and anything that needs an AI
// agent with the internet behind it. The device sends it what Home Assistant's Assist sends a
// conversation agent: a POST of JSON to /api/chat with the bridge's key as a bearer token, and gets
// {"reply": "..."} back. Without an address and a key the tool is not offered, and the model is not told
// of it.

const (
	// bridgeBodyMax bounds what is read of an answer: a spoken reply is a few sentences.
	bridgeBodyMax = 256 << 10

	// bridgeReplyMax is the longest reply that goes back to the model, in bytes.
	bridgeReplyMax = 8000

	// bridgeID names this device to the bridge, which remembers the last answer for "say that again" per name.
	bridgeID = "techo5-live"
)

// bridgeWait is how long the bridge may take: it can ask an AI agent that takes half a minute, and
// gives up on that itself after two minutes.
var bridgeWait = 100 * time.Second

func bridgeTools() []tool {
	if !config.Get().Brain.BridgeReady() {
		return nil
	}
	return []tool{
		{llm.Tool{Name: "ask_bridge", Description: "Ask the household's own assistant service. It does arithmetic, unit and currency conversions, dates and the calendar, " +
			"sunrise and sunset, the weather, the time anywhere, and anything else that needs looking up or working out. Use it for any question or request you cannot do " +
			"with your other tools or are not sure of. It can take 10 to 30 seconds when it has to look something up: say Working. before you call it. " +
			"Pass the person's own words. Its answer is already a short spoken sentence: say it as it is.",
			Parameters: object(map[string]any{"question": str("What the person asked, in their own words.")}, "question")},
			func(a map[string]any) (string, error) { return askBridge(argString(a, "question")) }},
	}
}

func askBridge(question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", errors.New("there was nothing to ask")
	}
	b := config.Get().Brain
	if !b.BridgeReady() {
		return "", errors.New("no bridge is set up")
	}
	body, err := json.Marshal(map[string]any{
		"text":            question,
		"language":        "en",
		"conversation_id": bridgeID,
		"satellite_id":    bridgeID,
	})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), bridgeWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.Bridge, "/")+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("the bridge address is not usable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.BridgeKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("the bridge did not answer in time")
		}
		return "", errors.New("the bridge did not answer")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", errors.New("the bridge refused the key")
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "", errors.New("the bridge could not reach Hermes, the agent behind it")
	default:
		return "", fmt.Errorf("the bridge answered %s", resp.Status)
	}
	var out struct {
		Reply string `json:"reply"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, bridgeBodyMax)).Decode(&out); err != nil {
		return "", errors.New("the device did not understand the bridge's answer")
	}
	reply := strings.TrimSpace(out.Reply)
	if reply == "" {
		return "", errors.New("the bridge had no reply")
	}
	if len(reply) > bridgeReplyMax {
		reply = reply[:bridgeReplyMax]
	}
	return reply, nil
}
