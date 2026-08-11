# Setup

All commands assume a root shell on the Echo Show (e.g. via `adb shell`,
or SSH to a `dropbear` daemon installed through Magisk) and [Go](https://go.dev/)
installed on your PC for cross-compiling.

## 1. Create your own CA

Any CA works — openssl, mitmproxy's auto-generated one, whatever you're
comfortable with. With openssl:

```bash
openssl req -x509 -newkey rsa:2048 -days 3650 -nodes \
  -keyout ca-key.pem -out ca-cert.pem \
  -subj "/CN=my-echo-show-ca"
cat ca-key.pem ca-cert.pem > ca.pem
```

`ca.pem` (key + cert concatenated) is what [`server/gencert`](server/gencert)
expects.

**Keep `ca-key.pem` private** — anyone with it could impersonate any
HTTPS site to a device that trusts this CA.

## 2. Install the CA into the device's system trust store

Requires root. Android identifies system CAs by a hash of the subject
name, not just any filename:

```bash
# on your PC:
openssl x509 -inform PEM -subject_hash_old -in ca-cert.pem -noout
# -> prints something like c8750f0d

# push ca-cert.pem to the device, then on the device:
mount -o rw,remount /system
cp ca-cert.pem /system/etc/security/cacerts/c8750f0d.0   # use YOUR hash
chmod 644 /system/etc/security/cacerts/c8750f0d.0
chown root:root /system/etc/security/cacerts/c8750f0d.0
mount -o ro,remount /system
```

## 3. Generate the leaf certificate

```bash
cd server/gencert
cp ../../ca.pem .        # the combined key+cert from step 1
go run gencert.go
```

This writes `leaf-cert.pem` and `leaf-key.pem`. Edit the `hosts` list at
the top of `gencert.go` first if you want to target different/additional
domains than the two documented in [`PROTOCOL.md`](PROTOCOL.md).

## 4. Redirect the target domain(s) via `/etc/hosts`

```bash
mount -o rw,remount /system
echo '127.0.0.1       d1cg7g7aedi1wy.cloudfront.net' >> /system/etc/hosts
echo '127.0.0.1       thumbnails-photos.amazon.de' >> /system/etc/hosts
mount -o ro,remount /system
```

Optional: also block the sponsored-content domains noted in
[`PROTOCOL.md`](PROTOCOL.md) the same way (nothing needs to listen on
127.0.0.1 for those — the app just gets a failed connection, which it
handles fine).

## 5. Configure and build the server

- Edit `server/main.go`: set `AlbumToken` to your own iCloud shared
  album token (from `https://www.icloud.com/sharedalbum/#<TOKEN>`), or
  replace `fetchSourceIndex`/`downloadPhoto` entirely with your own image
  source. Also set `TargetWidth`/`TargetHeight` to your device's actual
  screen resolution (check with `wm size` on the device).
- Cross-compile for the Echo Show's CPU (ARMv7 on the models this was
  tested against — check `cat /proc/cpuinfo` on your device if unsure).
  This needs network access once to fetch the `golang.org/x/image`
  dependency (used to resize/re-encode photos so old, memory-constrained
  devices don't choke decoding full-resolution originals):

```bash
cd server
GOOS=linux GOARCH=arm GOARM=7 go build -o echo_photo_server .
```

## 6. Deploy

```bash
# push echo_photo_server, leaf-cert.pem, leaf-key.pem to e.g.
# /data/local/echo_photo_server/ on the device, then:
chmod 755 /data/local/echo_photo_server/echo_photo_server
cd /data/local/echo_photo_server
nohup ./echo_photo_server > server.log 2>&1 &
```

Binding port 443 requires root. Check `server.log` — you should see the
Android CA count, the source index being fetched, and `listening on :443`.

## 7. Survive reboots (Magisk boot service)

```bash
cat > /data/adb/service.d/99-echo-photo-server.sh <<'EOF'
#!/system/bin/sh
export PATH=/system/bin:/system/xbin:/data/adb/magisk:$PATH
for i in $(seq 1 30); do
  ping -c1 -W2 8.8.8.8 >/dev/null 2>&1 && break
  sleep 2
done
cd /data/local/echo_photo_server
nohup ./echo_photo_server > server.log 2>&1 &
EOF
chmod 755 /data/adb/service.d/99-echo-photo-server.sh
```

Magisk runs everything in `/data/adb/service.d/` late in boot,
automatically, on every subsequent reboot.

## Verifying it works

From your PC (which isn't affected by the device's `/etc/hosts`), tunnel
into the device and hit it directly:

```bash
ssh -L 8443:127.0.0.1:443 root@<device-ip> -N -f
curl -sk --resolve d1cg7g7aedi1wy.cloudfront.net:8443:127.0.0.1 \
  "https://d1cg7g7aedi1wy.cloudfront.net:8443/fit-in/960x480/anything.jpg" \
  -o test.jpg
```

If `test.jpg` opens as a real image, the server side works — any
remaining issue is in the hosts-file redirect or CA trust on the device
itself.

## Images stopped showing after it worked fine for a while

Don't assume the server crashed - check `server.log` first; it's very
likely still running and responding fine, and the on-device renderer is the
one stuck. See [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) for the diagnosis
and a watchdog script that fixes it automatically.

Also note: this device's shell doesn't have plain `awk` on `PATH` (only
`busybox awk` works) - if you write any helper scripts that parse `ps`
output, see the "trap worth knowing about" section in
[`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) before you lose a day to a
silently-empty `$(...)` substitution.
