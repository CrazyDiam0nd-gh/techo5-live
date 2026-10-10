package mic

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain lets this test binary stand in for the WebRTC helper: started with HELPER set, it answers each block
// of microphone and loopback with the microphone half, as a helper that is working and cancels nothing.
func TestMain(m *testing.M) {
	if os.Getenv("TECHO5_FAKE_AEC") == "1" {
		buf := make([]byte, 4*FrameSamples)
		for {
			if _, err := io.ReadFull(os.Stdin, buf); err != nil {
				os.Exit(0)
			}
			if _, err := os.Stdout.Write(buf[:2*FrameSamples]); err != nil {
				os.Exit(0)
			}
		}
	}
	os.Exit(m.Run())
}

// fakeHelper puts a "techo5-aec" on the path that runs this binary as the fake.
func fakeHelper(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nTECHO5_FAKE_AEC=1 exec " + exe + "\n"
	if err := os.WriteFile(filepath.Join(dir, WebRTCHelper), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func block() ([]int16, []int16) {
	return make([]int16, FrameSamples), make([]int16, FrameSamples)
}

// A helper that dies is replaced by the built-in filter at once, and started again after a pause, not left for good.
func TestAHelperThatDiesComesBack(t *testing.T) {
	fakeHelper(t)
	was := retryBase
	retryBase = 20 * time.Millisecond
	t.Cleanup(func() { retryBase = was })

	c := newCanceller()
	if got := c.setEngine("webrtc"); got != "webrtc" {
		t.Fatalf("the fake helper did not start: %s", got)
	}
	mic, ref := block()
	if _, _, err := c.process(mic, ref); err != nil {
		t.Fatalf("a block through the helper: %v", err)
	}

	_ = c.ext.cmd.Process.Kill() // the helper dies
	time.Sleep(50 * time.Millisecond)
	if _, _, err := c.process(mic, ref); err != nil {
		t.Fatalf("the built-in filter should carry on: %v", err)
	}
	if c.engine != "builtin" || c.ext != nil {
		t.Fatalf("after the helper died: engine %q", c.engine)
	}

	time.Sleep(60 * time.Millisecond) // the pause
	if _, _, err := c.process(mic, ref); err != nil {
		t.Fatal(err)
	}
	if c.engine != "webrtc" || c.ext == nil {
		t.Errorf("the helper was not started again: engine %q", c.engine)
	}
	c.ext.Close()
}

func TestTheRetriesBackOffAndGiveUp(t *testing.T) {
	c := &canceller{want: "webrtc"}
	now := time.Now()
	if c.retryDue(now) {
		t.Error("retrying with nothing having failed")
	}
	var gaps []time.Duration
	for i := 0; i < retryLimit+2; i++ {
		c.failed(now)
		if c.fails > retryLimit {
			if c.retryDue(now.Add(time.Hour)) {
				t.Error("still retrying after the limit")
			}
			break
		}
		if c.retryDue(now) {
			t.Error("retried the instant it failed")
		}
		wait := retryBase << (c.fails - 1)
		if c.retryDue(now.Add(wait - time.Millisecond)) {
			t.Errorf("failure %d retried before %v", c.fails, wait)
		}
		if !c.retryDue(now.Add(wait)) {
			t.Errorf("failure %d not retried at %v", c.fails, wait)
		}
		gaps = append(gaps, wait)
		now = now.Add(wait)
	}
	for i := 1; i < len(gaps); i++ {
		if gaps[i] <= gaps[i-1] {
			t.Errorf("the pause did not grow: %v", gaps)
		}
	}
	// One that ran well for a while is a fresh start, and one nobody asked for is left alone.
	c = &canceller{want: "webrtc", fails: 4, upSince: now.Add(-2 * time.Minute)}
	c.failed(now)
	if c.fails != 1 {
		t.Errorf("a long run then a failure counts as failure %d, want 1", c.fails)
	}
	c = &canceller{want: "builtin", fails: 1, failedAt: now}
	if c.retryDue(now.Add(time.Hour)) {
		t.Error("retried a helper nobody wanted")
	}
}
