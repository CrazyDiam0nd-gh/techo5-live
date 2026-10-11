#!/usr/bin/env bash
# Go back to the stock TECHO5 daemon on a Dot or Show that install.sh was used on.
#   bash restore.sh <the device's address>      # on a Show, SLOT=a (or b) names the slot to go back to
# Dot: puts the saved stock daemon back in the running slot. Show: switches the boot slot back to the stock one.
set -euo pipefail
HOST=${1:?the address of the device}
SSH=(ssh -o BatchMode=yes -o ConnectTimeout=8 "root@$HOST")
if "${SSH[@]}" 'test -d /store/slots && test -x /usr/local/sbin/slotctl'; then
	echo "Show:"; "${SSH[@]}" '/usr/local/sbin/slotctl status'
	s=${SLOT:-}
	[ -n "$s" ] || read -r -p "Switch to which slot (a/b), or "image" to install the saved stock image into the other slot? (or run with SLOT=...) " s || s=n
	if [ "$s" = image ]; then
		img=${STATE_DIR:-$HOME/.techo5-live}/show-stock.tar.gz
		[ -s "$img" ] || { echo "no stock image at $img" >&2; exit 1; }
		want=$(sha256sum "$img" | cut -d' ' -f1)
		"${SSH[@]}" 'cat > /tmp/rootfs-stock.tar.gz' < "$img"
		[ "$want" = "$("${SSH[@]}" 'sha256sum /tmp/rootfs-stock.tar.gz' | cut -d' ' -f1)" ] || { echo "the copy does not match; nothing installed" >&2; exit 1; }
		"${SSH[@]}" '/usr/local/sbin/slotctl install /tmp/rootfs-stock.tar.gz; rm -f /tmp/rootfs-stock.tar.gz; /usr/local/sbin/slotctl status'
		echo "Stock firmware installed into the other slot, on trial. Reboot the Show to use it."
		exit 0
	fi
	case "$s" in a|b) ;; *) echo "nothing changed"; exit 0;; esac
	"${SSH[@]}" "/usr/local/sbin/slotctl switch $s; /usr/local/sbin/slotctl status"
	echo "Reboot the Show to use slot $s."
	echo "If the stock slot was overwritten, run again with SLOT=image to install the saved stock image."
else
	exec bash "$(dirname "$0")/restore-stock.sh" "$HOST"
fi
