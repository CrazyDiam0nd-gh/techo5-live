package streaming

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/HuskerMinion/techo5/echod/internal/feature/home"
	"github.com/HuskerMinion/techo5/echod/internal/feature/media"
	"github.com/HuskerMinion/techo5/echod/internal/layout"
	"github.com/HuskerMinion/techo5/echod/internal/lib/safe"
)

// spotifyEvent is the program librespot runs on each player event: it writes one line, tab-separated,
// to a pipe the daemon reads: the event, the song, its artists and album, its cover (the first librespot
// lists, which is the largest) and the volume the app set. Tabs and newlines in what it writes are made
// spaces, so a song's name cannot end the line early, and each field is cut short: short enough that a
// whole line stays under the pipe's atomic write, so two events running at once cannot interleave their
// lines, even in text whose characters take four bytes each. The pipe is opened
// for reading as well as writing (1<>), which never waits: a pipe nobody reads any more, as when the
// receivers are stopped, cannot leave the program stuck.
const spotifyEvent = `#!/bin/sh
case "$PLAYER_EVENT" in
track_changed|stopped|session_connected|session_disconnected|volume_changed) ;;
*) exit 0 ;;
esac
clean() { printf '%s' "$1" | tr '\t\n' '  ' | cut -c1-200; }
artists() { printf '%s' "$ARTISTS" | awk 'NR > 1 { printf ", " } { printf "%s", $0 }' | tr '\t' ' ' | cut -c1-200; }
cover() { printf '%s' "$COVERS" | awk 'NR == 1' | tr -d '\t\r' | cut -c1-200; }
printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$PLAYER_EVENT" "$(clean "$NAME")" "$(artists)" "$(clean "$ALBUM")" "$(cover)" "$(clean "$VOLUME")" 1<>"$TECHO5_SPOTIFY_EVENTS"
`

// The Spotify app's volume slider and the device's volume are one. The librespot build-librespot.sh
// makes takes volumes from outside (--volume-fifo, librespot-volume-fifo.patch) and passes the audio on
// unscaled: the device's volume is handed to it as a phone connects and whenever it changes, by the
// buttons, Home Assistant or night volume alike, so the app's slider shows it, and a move of the slider
// sets the device's volume to where the slider is (spotifySync).
//
// An older librespot, from an image made before that, takes no volume from outside and scales what it
// sends by the slider whatever it is told (its "fixed" control in 0.8 still scales). It is told to scale
// linearly, which the pump undoes exactly (spotifyLevel), and each move of the slider moves the device
// by as many steps as it moved (slider). By as many, not to the same place: its slider does not follow
// the device's buttons, and a slider that says 100% over a device at 8 would otherwise send it to
// nearly full at the first nudge down.
//
// librespot also says its volume as a phone connects, which is only where it was left, and may say it
// more than once: everything it says in the moments after a connect only marks where the slider is.

// spotifyFull is librespot's top volume.
const spotifyFull = 65535

// spotifyLevel undoes librespot's scaling on what it sends. librespot says its volume as a phone
// connects, before any audio, so the unity it starts at is never what a song is undone by.
var spotifyLevel = newLevel()

// spotifyPipe is the read end of librespot's audio while it runs, for spotifyQueued.
var spotifyPipe atomic.Pointer[os.File]

// spotifyGain is the gain that undoes librespot's scaling at the app's volume v, so what reaches the
// speaker is the song at the level the device's volume sets. Silence is left alone: there is nothing
// to undo.
func spotifyGain(v int) float64 {
	if v <= 0 || v >= spotifyFull {
		return 1
	}
	return spotifyFull / float64(v)
}

// spotifyQueued is how much audio is waiting in librespot's pipe, scaled at whatever volume it was
// scaled at when it was written.
func spotifyQueued() int {
	f := spotifyPipe.Load()
	if f == nil {
		return 0
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return 0
	}
	n := 0
	_ = rc.Control(func(fd uintptr) { n, _ = unix.IoctlGetInt(int(fd), unix.TIOCINQ) })
	return n
}

// spotifyStep is the device's volume step for the app's level.
func spotifyStep(v int) int { return (v*media.VolumeSteps + spotifyFull/2) / spotifyFull }

// spotifyValue is the app's level for the device's volume step: the one spotifyStep takes back to it.
func spotifyValue(step int) int { return (step*spotifyFull + media.VolumeSteps/2) / media.VolumeSteps }

// connectQuiet is how long after a phone connects a volume is only librespot saying where it was.
const connectQuiet = 3 * time.Second

// slider follows the app's volume slider. last is where it was, -1 before anything was heard.
type slider struct{ last int }

// moved takes the slider's new level v and says how many steps it moved, 0 for a level that only says
// where it is: one heard as a phone connects, or the first ever heard, which has nothing to move from.
func (s *slider) moved(v int, connecting bool) int {
	prev := s.last
	s.last = v
	if connecting || prev < 0 {
		return 0
	}
	return spotifyStep(v) - spotifyStep(prev)
}

// echoWait is how long a level handed to librespot may take to come back as the app's.
const echoWait = 3 * time.Second

// spotifySync keeps the app's slider and the device's volume as one, with a librespot that takes
// volumes from outside.
//
// librespot says every volume it is given back as the app's, so a level heard that was handed to it
// moments ago is its own coming back, and is let by: taken as the app's it could pull the device back
// to where it was a moment ago, since the programs that say the levels finish in no set order. A level
// the device took from the slider is not handed back, which would fight a slider still being dragged,
// unless the device ended somewhere else (night volume stopped a raise), so the slider shows that.
type spotifySync struct {
	write  func(v int) bool // hands librespot a level; false when it was not taken
	device func() int       // the device's volume step
	set    func(step int)   // sets the device's volume step
	most   func(to int) int // the most a raise may take the device to (media.CapRaise)
	now    func() time.Time

	mu     sync.Mutex
	active bool              // a phone is connected: librespot takes a volume only then
	shown  int               // the step the app's slider is at, as far as is known; -1 for not known
	sent   map[int]time.Time // levels handed to librespot, and when
}

func newSpotifySync() *spotifySync {
	return &spotifySync{shown: -1, sent: map[int]time.Time{}, now: time.Now}
}

// session is a phone connecting, or going: one that connects is told the device's volume.
func (s *spotifySync) session(connected bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active, s.shown = connected, -1
	clear(s.sent)
	s.tellLocked(s.device())
}

// heard takes a level librespot said, 0..spotifyFull; connecting when it came in the moments after a
// phone connected.
func (s *spotifySync) heard(v int, connecting bool) {
	s.mu.Lock()
	if at, ok := s.sent[v]; ok && s.now().Sub(at) <= echoWait {
		s.mu.Unlock()
		return
	}
	// librespot says a volume only while a phone is connected: one heard before the connect's own line
	// (the programs that say them finish in no set order) is the connect's too, and like the connect's
	// is only where librespot was left. The device's level stands, and the app is told it.
	if connecting || !s.active {
		s.active = true
		s.tellLocked(s.device())
		s.mu.Unlock()
		return
	}
	step := spotifyStep(v)
	s.shown = step
	from, to := s.device(), step
	if to > from {
		to = s.most(to)
	}
	s.mu.Unlock()
	switch {
	case to != from:
		s.set(to) // levelChanged tells the app if it ended somewhere other than the slider
	case to != step:
		s.tell(to)
	}
}

// levelChanged is the device's volume changing, for whatever reason: the app's slider is moved to it
// unless it is there already.
func (s *spotifySync) levelChanged(step int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if step != s.shown {
		s.tellLocked(step)
	}
}

func (s *spotifySync) tell(step int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tellLocked(step)
}

// tellLocked hands librespot the device's step while a phone is connected. Called with mu held: write
// never waits.
func (s *spotifySync) tellLocked(step int) {
	if !s.active {
		return
	}
	now := s.now()
	for v, at := range s.sent {
		if now.Sub(at) > echoWait {
			delete(s.sent, v)
		}
	}
	v := spotifyValue(step)
	if !s.write(v) {
		return
	}
	s.sent[v], s.shown = now, step
}

// writeSpotifyVolume hands librespot a level through its volume pipe, and says whether it took it.
// Nothing waits: with librespot not running there is nobody to open the pipe to and the level is
// dropped, as it is when the pipe is full, which a librespot that stopped reading would leave it.
// A line is far shorter than the pipe's atomic write, so it goes whole or not at all.
func writeSpotifyVolume(path string, v int) bool {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	line := []byte(strconv.Itoa(v) + "\n")
	n, err := unix.Write(fd, line)
	return err == nil && n == len(line)
}

// librespotTakesVolume is whether the image's librespot takes volumes from outside (--volume-fifo):
// one from an image made before build-librespot.sh patched it in does not, and would refuse to start
// if it were given one. A variable for the tests.
var librespotTakesVolume = func() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, librespotPath, "--help").Output()
	return bytes.Contains(out, []byte("--volume-fifo"))
}

// spotifyPort is where librespot listens for the Spotify app handing it a login: fixed, so a firewall
// can let it through (the Dot's does).
const spotifyPort = "4070"

// spotifyCache is where the login a phone hands over is kept, so the device is still the account's
// after a restart: on userdata beside the device's own state, not in it (that is root's alone, and
// librespot runs as the receivers' user), readable by that user alone. Turning Spotify Connect off
// forgets it (forgetSpotify).
func spotifyCache() string { return filepath.Join(filepath.Dir(layout.StateDir), "techo5-spotify") }

// forgetSpotify removes the kept login: a device switched off from Spotify Connect, or given away, is
// no longer the account's.
func forgetSpotify() {
	if _, err := os.Stat(spotifyCache()); err != nil {
		return
	}
	if err := os.RemoveAll(spotifyCache()); err != nil {
		slog.Warn("streaming: forgetting the Spotify login failed", "err", err)
		return
	}
	slog.Info("streaming: Spotify login forgotten")
}

// runSpotifyReceiver runs librespot under the device's name until ctx ends, playing what it sends and
// showing what it says is playing.
func runSpotifyReceiver(ctx context.Context, name string) {
	r, w, err := os.Pipe()
	if err != nil {
		slog.Error("streaming: Spotify's audio pipe failed", "err", err)
		return
	}
	defer r.Close()
	defer w.Close()
	spotifyPipe.Store(r)
	defer spotifyPipe.Store(nil)
	cred := receiverCred()
	// The device's volume goes to librespot through a pipe, when it can take it.
	volume := ""
	if librespotTakesVolume() {
		path := filepath.Join(runDir, "spotify-volume")
		_ = os.Remove(path)
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			slog.Warn("streaming: Spotify's volume pipe failed; its slider moves the device by steps", "err", err)
		} else {
			handTo(cred, path)
			volume = path
		}
	}
	events := filepath.Join(runDir, "spotify-events")
	script := filepath.Join(runDir, "spotify-event")
	_ = os.Remove(events)
	if err := syscall.Mkfifo(events, 0o600); err != nil {
		slog.Warn("streaming: Spotify's event pipe failed; no song names", "err", err)
	} else if err := os.WriteFile(script, []byte(spotifyEvent), 0o755); err != nil {
		slog.Warn("streaming: writing Spotify's event program failed; no song names", "err", err)
	} else {
		handTo(cred, events)
		safe.Go("spotify events", func() { readSpotifyEvents(ctx, events, volume) })
	}
	cache := spotifyCache()
	_ = os.MkdirAll(cache, 0o700)
	_ = os.Chmod(cache, 0o700)
	handTo(cred, cache)
	args := []string{
		"--name", name,
		"--backend", "pipe",
		"--format", "S16",
		"--bitrate", "160",
		"--device-type", "speaker",
		"--disable-audio-cache",
		"--cache", cache,
		"--zeroconf-port", spotifyPort,
		"--onevent", script,
	}
	level := spotifyLevel
	if volume != "" {
		// The audio comes unscaled: there is nothing to undo.
		args, level = append(args, "--volume-fifo", volume), nil
	} else {
		args = append(args, "--initial-volume", "100", "--volume-ctrl", "linear")
	}
	safe.Go("spotify audio", func() { pump(ctx, home.SpotifyName, r, level) })
	supervise(ctx, program{
		name:   "librespot",
		path:   librespotPath,
		args:   args,
		stdout: w,
		env:    []string{"TECHO5_SPOTIFY_EVENTS=" + events},
		cred:   cred,
		keep:   librespotWorthLogging,
	})
}

// librespotWorthLogging keeps librespot's warnings and errors, and what it says as it fails to start or
// crashes, and drops the rest, which names the account that logged in.
func librespotWorthLogging(line string) bool {
	for _, s := range []string{" WARN ", " ERROR ", "panicked", "error:", "Usage:"} {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

// readSpotifyEvents follows the event pipe, telling the media player the song and the page its cover,
// and taking the app's volume as the device's: kept as one through the volume pipe at volume, or, with
// none, moved by steps. Opened for writing as well, so it neither waits for librespot nor sees an end
// between events.
func readSpotifyEvents(ctx context.Context, path, volume string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		slog.Warn("streaming: opening Spotify's event pipe failed", "err", err)
		return
	}
	stop := context.AfterFunc(ctx, func() { f.Close() })
	defer stop()
	defer f.Close()
	track := func(title, artist, album, cover string) {
		media.Get().SetReceivedTrack(home.SpotifyName, title, artist, album)
		home.ReceivedArt(home.SpotifyName, cover)
	}
	if volume != "" {
		one := newSpotifySync()
		one.write = func(v int) bool { return writeSpotifyVolume(volume, v) }
		one.device = media.Get().Volume
		one.set = media.Get().Set
		one.most = media.Get().CapRaise
		defer media.Get().OnLevel.Listen(one.levelChanged)()
		followSpotifyEvents(f, spotifyEvents{
			track: track,
			volume: func(v int, connecting bool) {
				if connecting {
					slog.Info("streaming: Spotify connected", "volume", media.Get().Volume())
				}
				one.heard(v, connecting)
			},
			session: one.session,
			now:     time.Now,
		})
		return
	}
	app := slider{last: -1}
	followSpotifyEvents(f, spotifyEvents{
		track: track,
		volume: func(v int, connecting bool) {
			spotifyLevel.set(spotifyGain(v), spotifyQueued())
			by := app.moved(v, connecting)
			if connecting {
				slog.Info("streaming: Spotify connected", "volume", spotifyStep(v))
			}
			// The app says each level more than once as the slider moves; those move nothing.
			if by != 0 {
				media.Get().Set(media.Get().Volume() + by)
			}
		},
		now: time.Now,
	})
}

// spotifyEvents is what the event lines are turned into.
type spotifyEvents struct {
	// track is a new song, or nothing when playing stops.
	track func(title, artist, album, cover string)
	// volume is the app's volume, 0..spotifyFull; connecting when it came in the moments after a phone
	// connected, and so only says where librespot was left.
	volume func(v int, connecting bool)
	// session, when there is one, is a phone connecting, or going.
	session func(connected bool)
	now     func() time.Time
}

// followSpotifyEvents reads event lines from r until it ends. A line too long to be one of the
// program's is skipped, not taken as the end.
func followSpotifyEvents(r io.Reader, on spotifyEvents) {
	br := bufio.NewReaderSize(r, 4096)
	var connected time.Time
	for {
		line, err := readLine(br, 16<<10)
		if err != nil {
			return
		}
		f := strings.Split(line, "\t")
		if len(f) < 6 {
			continue
		}
		// The program cuts each field at a byte count, which can fall inside a character.
		for i := range f {
			f[i] = strings.ToValidUTF8(f[i], "")
		}
		switch f[0] {
		case "track_changed":
			on.track(f[1], f[2], f[3], f[4])
		case "stopped":
			on.track("", "", "", "")
		case "session_disconnected":
			on.track("", "", "", "")
			if on.session != nil {
				on.session(false)
			}
		case "session_connected":
			connected = on.now()
			if on.session != nil {
				on.session(true)
			}
		case "volume_changed":
			v, err := strconv.Atoi(strings.TrimSpace(f[5]))
			if err != nil || v < 0 || v > spotifyFull {
				continue
			}
			// Not only the first after a connect: librespot can say its level more than once as it
			// starts, and in whatever order the event programs finish.
			connecting := !connected.IsZero() && on.now().Sub(connected) < connectQuiet
			on.volume(v, connecting)
		}
	}
}

// readLine is the next line from br without its newline, or "" for one longer than most, which is read
// through and dropped.
func readLine(br *bufio.Reader, most int) (string, error) {
	var line []byte
	over := false
	for {
		part, isPrefix, err := br.ReadLine()
		if err != nil {
			return "", err
		}
		if !over {
			line = append(line, part...)
			over = len(line) > most
		}
		if !isPrefix {
			break
		}
	}
	if over {
		return "", nil
	}
	return string(line), nil
}
