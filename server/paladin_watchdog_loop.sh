#!/system/bin/sh
# Periodically restarts com.amazon.paladin (Fire OS's ambient/photo-clock
# renderer). Workaround for a recurring TLS/network hiccup that leaves it
# stuck failing every image request until the process is killed and
# auto-respawns. See ../TROUBLESHOOTING.md.
export PATH=/system/bin:/system/xbin:/data/adb/magisk:$PATH
LOG=/data/local/echo_photo_server/paladin_watchdog.log

while true; do
  sleep 14400
  PID=$(ps | grep com.amazon.paladin | grep -v grep | busybox awk '{print $2}')
  if [ -n "$PID" ]; then
    kill $PID
    echo "$(date) restarted paladin (was pid $PID)" >> "$LOG"
  else
    echo "$(date) paladin not found, nothing to restart" >> "$LOG"
  fi
done
