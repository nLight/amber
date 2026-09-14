#!/bin/sh
# The wrapper in start.sh brings the reader UI back once Amber is gone.
PIDFILE=/var/run/amber.pid
[ -f "$PIDFILE" ] && kill "$(cat "$PIDFILE")" 2>/dev/null
exit 0
