#!/bin/sh
# Stops the reader UI and runs Amber. Whenever Amber exits — a long press, stop.sh,
# a crash, a broken config — the reader UI is started again.
PATH=/usr/sbin:/sbin:/usr/bin:/bin
DIR=/mnt/us/extensions/amber
PIDFILE=/var/run/amber.pid
# On the user store, so it can be read over USB storage too.
LOG=$DIR/amber.log

if [ -f "$PIDFILE" ] && [ -d "/proc/$(cat "$PIDFILE")" ]; then
    echo 'amber is already running' >&2
    exit 0
fi

[ -f "$LOG" ] && [ "$(wc -c < "$LOG")" -gt 500000 ] && mv -f "$LOG" "$LOG.old"

(
    echo "--- start $(date)"
    # The reader UI is what brings Wi-Fi up after a boot, so wait for a connection
    # (up to two minutes) before stopping it. Amber re-enables Wi-Fi itself later.
    lipc-set-prop com.lab126.cmd wirelessEnable 1 2>/dev/null
    i=0
    while [ "$(lipc-get-prop com.lab126.wifid cmState 2>/dev/null)" != "CONNECTED" ] && [ $i -lt 60 ]; do
        sleep 2
        i=$((i + 1))
    done
    echo "wifi: $(lipc-get-prop com.lab126.wifid cmState 2>/dev/null) after $((i * 2))s"

    OLD_SAVER=$(lipc-get-prop com.lab126.powerd preventScreenSaver 2>/dev/null)
    lipc-set-prop com.lab126.powerd preventScreenSaver 1
    stop lab126_gui
    sleep 4
    "$DIR/bin/amber" -config "$DIR/amber.json" "$@" &
    echo $! > "$PIDFILE"
    wait $!
    echo "--- amber exited with $?"
    rm -f "$PIDFILE"
    lipc-set-prop com.lab126.powerd preventScreenSaver "${OLD_SAVER:-0}"
    start lab126_gui
) </dev/null >>"$LOG" 2>&1 &
