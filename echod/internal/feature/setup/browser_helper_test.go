package setup

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startChrome opens url in a headless Chrome of its own and returns what stops it. Chrome starts helper
// processes that go on writing to its profile after the main one is killed, so the whole group is
// stopped and the profile is removed without complaint if something still holds a file in it.
func startChrome(t *testing.T, ctx context.Context, chrome, url string) {
	t.Helper()
	profile, err := os.MkdirTemp("", "techo5-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu",
		"--autoplay-policy=no-user-gesture-required", "--user-data-dir="+profile, url)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(profile)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		time.Sleep(200 * time.Millisecond)
		_ = os.RemoveAll(profile) // best effort: a file still held is not the test's failure
	})
}
