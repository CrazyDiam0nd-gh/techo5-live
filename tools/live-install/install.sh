#!/usr/bin/env bash
# Install the live-conversation build of the TECHO5 daemon on a jailbroken Echo Dot or Echo Show 5.
#
#   bash install.sh <the device's address> [<binary>] [<its .sha256 file>]
#
# Read README.md first. The script finds out which device it is talking to. With no binary named it
# looks next to itself for echod-dot-arm or echod-show-arm (and a matching .sha256 file).
#
#   Dot:  replaces the daemon in the slot the Dot runs from and puts that slot on trial (back to the other slot by
#         itself if it does not stay up). The stock daemon is kept on the Dot.
#   Show: builds a root filesystem image here (the Show's own, with the daemon swapped), installs it into the slot the
#         Show is NOT running from (slotctl install: on trial, made the slot to boot next). The running slot is untouched
#         and is your way back. The stock image is also saved on this computer.
# Settings, keys and Home Assistant access live in /data and are never touched.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
HOST=${1:?the address of the device, e.g. 192.168.1.50}
BIN=${2:-}
SUMS=${3:-}
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new "root@$HOST")

"${SSH[@]}" 'test -r /run/techo5/slot' \
	|| { echo "$HOST does not look like a TECHO5 device (no /run/techo5/slot), or SSH is not set up" >&2; exit 1; }
if "${SSH[@]}" 'test -d /store/slots && test -x /usr/local/sbin/slotctl'; then KIND=show
elif "${SSH[@]}" 'test -d /store/techo5/slots && command -v slotctl >/dev/null'; then KIND=dot
else echo "$HOST has a TECHO5 slot file but a layout this script does not know; not touching it" >&2; exit 1; fi
echo "$HOST is an Echo ${KIND^}"

[ -n "$BIN" ] || BIN=$HERE/echod-$KIND-arm
[ -f "$BIN" ] || { echo "no binary at $BIN (give it as the second argument)" >&2; exit 1; }
[ -n "$SUMS" ] || { [ -f "$BIN.sha256" ] && SUMS=$BIN.sha256; } || true
want=$(sha256sum "$BIN" | cut -d' ' -f1)
if [ -n "$SUMS" ]; then
	grep -q "^$want" "$SUMS" || { echo "the checksum of $BIN is not the one in $SUMS; not installing" >&2; exit 1; }
	echo "checksum matches $SUMS"
else
	echo "WARNING: no checksum file given; only install a build you trust"
fi
file "$BIN" 2>/dev/null | grep -q "ARM" || echo "WARNING: $BIN does not look like a 32-bit ARM program"

echo "Currently:"; "${SSH[@]}" 'slotctl status 2>/dev/null || /usr/local/sbin/slotctl status'
read -r -p "Install $BIN on this $KIND? [y/N] " yes
[ "$yes" = y ] || { echo "nothing changed"; exit 0; }

copy_checked() { # local file, remote path
	"${SSH[@]}" "cat > $2" < "$1"
	local got; got=$("${SSH[@]}" "sha256sum $2" | cut -d' ' -f1)
	[ "$(sha256sum "$1" | cut -d' ' -f1)" = "$got" ] || { echo "the copy on the device does not match; nothing was replaced" >&2; "${SSH[@]}" "rm -f $2"; exit 1; }
}

if [ "$KIND" = dot ]; then
	echo "copying..."
	"${SSH[@]}" 'mkdir -p /store/techo5'
	copy_checked "$BIN" /store/techo5/echod-realtime.new
	"${SSH[@]}" 'chmod 755 /store/techo5/echod-realtime.new'
	"${SSH[@]}" '
set -e
slot=$(cat /run/techo5/slot)
state=/store/techo5/slots/$slot.state
bin=/store/techo5/slots/$slot/usr/local/bin/echod
mkdir -p /store/techo5/backup-stock
touch /run/techo5/hold
killall echod || true
sleep 2
umount /usr/local/bin/echod 2>/dev/null || true
if [ ! -f /store/techo5/backup-stock/echod-stock ]; then
	cp "$bin" /store/techo5/backup-stock/echod-stock
	sha256sum /store/techo5/backup-stock/echod-stock > /store/techo5/backup-stock/echod-stock.sha256
	echo "stock daemon kept in /store/techo5/backup-stock/"
fi
cp /store/techo5/echod-realtime.new "$bin.new"
mv -f "$bin.new" "$bin"
rm -f /store/techo5/echod-realtime.new
printf "trial 2\n" > "$state.tmp" && mv -f "$state.tmp" "$state"
sync
rm -f /run/techo5/hold
echo "slot $slot now holds the live-conversation build, on trial"
sleep 12
ps | grep "[e]chod run" || echo "NOT RUNNING - see the log"
slotctl status'
	echo "Done. Reboot the Dot once so the slot is committed as good."
else
	command -v python3 >/dev/null || { echo "python3 is needed on this computer to build the Show image" >&2; exit 1; }
	slot=$("${SSH[@]}" 'cat /run/techo5/slot')
	STATE=${STATE_DIR:-$HOME/.techo5-live}
	mkdir -p "$STATE"
	stockimg=$STATE/show-stock-slot-$slot.tar.gz
	if [ ! -s "$stockimg" ]; then
		echo "saving the running slot ($slot) as a stock image on this computer: $stockimg"
		"${SSH[@]}" "tar -C /store/slots/$slot -cf - ." | gzip > "$stockimg.part"
		gzip -t "$stockimg.part" && mv -f "$stockimg.part" "$stockimg"
	fi
	img=$STATE/show-rootfs-${want:0:8}.tar.gz
	if [ ! -s "$img" ] || ! gzip -t "$img" 2>/dev/null; then
		echo "making the image (the running slot with the daemon swapped)..."
		"${SSH[@]}" "tar -C /store/slots/$slot -cf - ." | python3 -I "$HERE/swap-binary-in-rootfs.py" usr/local/bin/techo5 "$BIN" > "$img.part"
		gzip -t "$img.part" && mv -f "$img.part" "$img"
	fi
	echo "copying the image to the Show..."
	copy_checked "$img" /tmp/rootfs-live.tar.gz
	"${SSH[@]}" '/usr/local/sbin/slotctl install /tmp/rootfs-live.tar.gz; rm -f /tmp/rootfs-live.tar.gz; /usr/local/sbin/slotctl status'
	echo "Done. Reboot the Show to start it (it falls back to slot $slot if the new one fails)."
	echo "Stock image kept at $stockimg; restore.sh uses it."
fi
echo "Then: setup page > Sound & Voice > Voice > Voice assistant > Live conversation / Live voice."
