# Troubleshooting: images stop showing after a while

## Symptom

The screensaver falls back to the device's plain default background (grey
gradient or the built-in wave/clock animation) instead of a photo, and stays
that way for hours until something intervenes.

## Root cause

The server side is generally not the problem. Confirmed by hitting the
server directly (see "Verifying it works" in [SETUP.md](SETUP.md)) during an
outage - it kept responding with a valid image the whole time.

Instead, the on-device client process that actually requests and renders the
images (`com.amazon.paladin` on Fire OS - the ambient/photo-clock renderer)
gets stuck. `server.log` shows the difference clearly:

- Healthy: lines like `served ... for /fit-in/960x480/...`
- Stuck: only connection-level errors, and no `served` lines at all -
  `http2: server: error reading preface from client ... connection reset by
  peer`, `http: TLS handshake error ... EOF`, `http: TLS handshake error ...
  remote error: tls: unknown certificate authority`

These are all failures *before* a request ever reaches our HTTP handler -
the TLS/HTTP2 handshake itself is failing, not our serving logic. Once the
client gets into this state it does not recover on its own; only killing
the process (Android immediately relaunches it) clears it.

In our case this repeatedly started around the same time window every night
(observed clustering around 18:00-20:00 and again around 01:00-02:00),
consistent with some nightly network hiccup (Wi-Fi roaming, DHCP renewal,
or similar) that the client doesn't handle gracefully.

## Fix: a watchdog that restarts the client periodically

[`server/paladin_watchdog_loop.sh`](server/paladin_watchdog_loop.sh) kills
the client process every 4 hours; Android relaunches it immediately, which
clears the stuck TLS state before it can persist for hours. This treats the
symptom, not the nightly root cause (which we never pinned down), but it's
been reliable in practice.

Install it the same way as the main server, as a Magisk boot service:

```bash
# push paladin_watchdog_loop.sh to e.g. /data/local/echo_photo_server/, then:
cat > /data/adb/service.d/99-paladin-watchdog.sh <<'EOF'
#!/system/bin/sh
export PATH=/system/bin:/system/xbin:/data/adb/magisk:$PATH
nohup sh /data/local/echo_photo_server/paladin_watchdog_loop.sh > /data/local/echo_photo_server/paladin_watchdog_stdout.log 2>&1 &
EOF
chmod 755 /data/adb/service.d/99-paladin-watchdog.sh
chmod 755 /data/local/echo_photo_server/paladin_watchdog_loop.sh

# and start it immediately for the current boot, without waiting for a reboot:
sh /data/adb/service.d/99-paladin-watchdog.sh
```

Restarts get logged to `/data/local/echo_photo_server/paladin_watchdog.log`.

If your ambient renderer process has a different package name (this is
FireOS/Echo Show specific), edit the `grep` target in
`paladin_watchdog_loop.sh` accordingly - find yours with
`ps | grep -i <launcher/home app>`.

## A trap worth knowing about: `awk` isn't on `PATH`

On this device's shell, plain `awk` doesn't resolve (`sh: awk: not found`) -
only `busybox awk` works, since BusyBox's applets aren't symlinked
individually into `/system/xbin`. Any script using bare `awk` (for example
to pull a PID out of `ps` output) silently fails at that one line: the
`$(...)` command substitution just returns empty output rather than raising
an obvious error, so a PID-lookup-and-kill script can look fine on paste,
run without complaint, and simply never do anything - which is exactly what
happened to an earlier version of this watchdog for 12 days before anyone
noticed. If a script here isn't doing what it should, check for this first:

```bash
which awk        # likely: not found
which busybox    # e.g. /data/adb/magisk/busybox
busybox awk '{print $1}' <<< "test"   # this works
```

Use `busybox awk` (or `busybox` for `sed`/`grep`/etc. as needed) explicitly
in scripts for this device rather than assuming coreutils-style tools are on
`PATH`.
