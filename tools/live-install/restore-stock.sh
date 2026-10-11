#!/usr/bin/env bash
# Put the stock TECHO5 daemon back on the Dot (undoes install-realtime.sh).
#   bash restore-stock.sh <the Dot's address>
set -euo pipefail
HOST=${1:?the address of the Dot}
ssh -o BatchMode=yes "root@$HOST" '
set -e
slot=$(cat /run/techo5/slot)
stock=/store/techo5/backup-stock/echod-stock
[ -f "$stock" ] || { echo "no stock daemon was kept on this Dot" >&2; exit 1; }
( cd /store/techo5/backup-stock && sha256sum -c echod-stock.sha256 )
bin=/store/techo5/slots/$slot/usr/local/bin/echod
touch /run/techo5/hold; killall echod || true; sleep 2; umount /usr/local/bin/echod 2>/dev/null || true
cp "$stock" "$bin.new" && mv -f "$bin.new" "$bin"
printf "trial 2\n" > /store/techo5/slots/$slot.state; sync
rm -f /run/techo5/hold
sleep 12; ps | grep "[e]chod run" || echo "NOT RUNNING - see the log"; slotctl status'
