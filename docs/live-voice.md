# Live voice: talk over your assistant (unofficial add-on)

This fork of [TECHO5](https://github.com/HuskerMinion/techo5) adds two ways to talk to an Echo Dot or Echo Show 5
that Home Assistant's Assist pipelines do not offer. It is **not part of TECHO5 and not affiliated with it**.
The project's maintainer chose to keep TECHO5 on Home Assistant's pipelines, which is a fair call; this fork is where
the other approach lives. The default behavior is unchanged: nothing here is used until you pick it on the setup page.

| Mode (setup page > Sound & Voice > Voice assistant > Answered by) | What it does |
|---|---|
| Home Assistant / direct | As in TECHO5. |
| **Live conversation (OpenAI Realtime)** | Speech goes to OpenAI Realtime, the answer comes back as text and is spoken sentence by sentence by **your own Wyoming TTS voice** (for example Piper). You can interrupt it mid-sentence. Half duplex. |
| **Live voice (OpenAI GPT-Live)** | Experimental. One continuous audio stream both ways, in the model's own voice, so you can talk over it like a phone call. Full duplex. |

Both can use your Home Assistant devices, scripts, music players and weather (exposed entities only), and the Show can
show a camera by voice. Live voice is the more fun and the less polished of the two.

## Warnings
- It is a modified daemon that runs as root on your device, and it is not signed by TECHO5. Install builds you trust.
- The firmware **Install** button on the device's Home Assistant card installs the official build over this one. Do not
  press it unless you want to go back. Run the installer again afterwards if you do.
- It uses your own OpenAI account and **costs money**. An open Live voice session costs roughly five cents a minute, and
  the session stays open for a short while after a conversation (setting: Stay ready after an answer). The key stays on the device.
- Your microphone mute stays your privacy switch. Audio is never recorded unless you turn that on.
- Written with an AI coding assistant and tested by hand on one Dot and one Show 5 (2nd gen). Expect rough edges.

## You need
- An Echo Dot 2nd gen or Echo Show 5 2nd gen already running TECHO5, with SSH as root (TECHO5's **SSH authorized keys** action).
- For Live conversation: a Wyoming text-to-speech server (the Piper add-on works), reachable as `host:port`.
- An OpenAI API key. Optional, for the house tools: a Home Assistant long-lived token made for the device.

## Install
Download the build for your device from this repository's [Releases](https://github.com/CrazyDiam0nd-gh/techo5-live/releases) (`echod-dot-arm` or `echod-show-arm`, with its `.sha256`),
then:
```sh
bash install.sh <the device's address> echod-show-arm echod-show-arm.sha256
```
(`tools/live-install/` has the scripts.) It finds out whether the address is a Dot or a Show.
- **Dot:** keeps the stock daemon, puts this one in the slot it runs from, on trial (back to the other slot by itself if it
  does not stay up). Reboot once afterwards so the slot is committed.
- **Show:** needs Python 3 on your computer. Saves the running slot as a stock image, builds a new image with this daemon and
  installs it into the other slot, on trial. Reboot the Show to start it.

Settings, keys and Home Assistant access live in `/data` and are never touched.

## Go back
```sh
bash restore.sh <the device's address>
```
On a Show, `SLOT=a` (or `b`) switches the boot slot back; `SLOT=image` installs the stock image that `install.sh` saved in `~/.techo5-live/` into the spare slot.

The stock daemon does not know the settings that only this fork has, and it blanks them when it rewrites its settings. Note them before you go back, and set them again if you reinstall. For example, the wake word's light effect **Steady** goes back to None (in Home Assistant: the Dot's *Assistant 1 wake word effect*), and the ring stays dark until you choose it again. Your key, voice server and mode are kept.

## Build it yourself
```sh
cd echod
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -tags dot -o echod-dot-arm ./cmd/echod     # Dot
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build          -o echod-show-arm ./cmd/echod     # Show
```
Tests: `go test ./...`, and again with `-tags dot` and `-tags spot`.

## Other changes in this fork
Sounds of your own uploaded from the setup page, the Dot's clock following the saved time zone, saving a setup form keeps
your scroll position, a WebRTC echo canceller that restarts after a stall.

## Credits
All the credit for the hardware work belongs to TECHO5 and the people it stands on: EchoLocal (Yuri Gelfand), amonet and
kaeru (R0rt1z2, k4y0z), and the others listed in the main README under "Made possible by". This fork keeps their licenses.
