#!/bin/sh
# autostart.sh on|off — installs or removes the upstart job that starts Amber
# once per boot. The job itself lives on the root filesystem, so installing it
# needs mntroot; everything else stays on the user store.
PATH=/usr/sbin:/sbin:/usr/bin:/bin
DIR=/mnt/us/extensions/amber
JOB=/etc/upstart/amber.conf

note() {
    echo "$1"
    eips 1 38 "Amber: $1                    " >/dev/null 2>&1
}

case "$1" in
on)
    mntroot rw
    cp "$DIR/bin/amber.conf" "$JOB" && chmod 644 "$JOB"
    mntroot ro
    note "autostart on"
    ;;
off)
    mntroot rw
    rm -f "$JOB"
    mntroot ro
    note "autostart off"
    ;;
*)
    echo "usage: $0 on|off" >&2
    exit 1
    ;;
esac
