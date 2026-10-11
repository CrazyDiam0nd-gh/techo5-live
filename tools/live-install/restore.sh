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
	[ -n "$s" ] || read -r -p "Switch to which slot (a/b)? (or run with SLOT=a) " s || s=n
	case "$s" in a|b) ;; *) echo "nothing changed"; exit 0;; esac
	"${SSH[@]}" "/usr/local/sbin/slotctl switch $s; /usr/local/sbin/slotctl status"
	echo "Reboot the Show to use slot $s."
	echo "If the stock slot was overwritten, install the stock image install.sh saved: ~/.techo5-live/show-stock-slot-*.tar.gz, with  slotctl install <file>  on the Show."
else
	exec bash "$(dirname "$0")/restore-stock.sh" "$HOST"
fi
