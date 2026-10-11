#!/usr/bin/env python3
"""Copy a root filesystem tar from stdin to a .tar.gz on stdout, with one file replaced by another.

    ssh root@show 'tar -C /store/slots/a -cf - .' | python3 -I swap-binary-in-rootfs.py usr/local/bin/techo5 build.bin > rootfs.tar.gz

Every other member is copied with its owner, mode, links and device numbers exactly as the archive has them,
which is why this works on a tar stream and not on an unpacked tree (a user cannot unpack one as root).
Fails unless exactly one member was replaced.
"""
import sys, tarfile, os, io

target, replacement = sys.argv[1].lstrip("./"), sys.argv[2]
data = open(replacement, "rb").read()
src = tarfile.open(fileobj=sys.stdin.buffer, mode="r|")
dst = tarfile.open(fileobj=sys.stdout.buffer, mode="w|gz", format=tarfile.GNU_FORMAT)
replaced = copied = 0
for m in src:
    if m.name.lstrip("./") == target:
        if not m.isfile():
            sys.exit("%s is not a regular file in the archive" % m.name)
        m.size = len(data)
        dst.addfile(m, io.BytesIO(data))
        replaced += 1
    elif m.isfile():
        dst.addfile(m, src.extractfile(m))
        copied += 1
    else:
        dst.addfile(m)
        copied += 1
dst.close()
sys.stderr.write("swap: %d members copied, %d replaced (%s, %d bytes)\n" % (copied, replaced, target, len(data)))
if replaced != 1:
    sys.exit("expected to replace exactly one member, replaced %d" % replaced)
