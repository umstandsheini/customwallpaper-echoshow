# customwallpaper-echoshow

How to replace the photos shown on an Amazon Echo Show's ambient/idle
screen — both the "Personal Photos" (Amazon Photos) mode and the built-in
"Art" category — with your own images, entirely standalone on a **rooted**
Echo Show. No cloud service, no companion app, no PC needed once it's set
up.

## How it works, in one sentence

The Echo Show's own photo-display mechanism is left completely alone —
we just make it fetch its images from a small HTTPS server running on the
device itself instead of from Amazon's real CDN, by combining a
`/etc/hosts` redirect with a certificate issued by a CA we install into
the system trust store.

See [`PROTOCOL.md`](PROTOCOL.md) for the full technical write-up (how the
two endpoints were found and what they look like) and
[`SETUP.md`](SETUP.md) for step-by-step instructions.

## Prerequisites

- A **rooted** Echo Show with Magisk (bootloader unlock is device/model
  specific — search XDA for your exact Echo Show model + "unlock")
- Root shell access (Magisk + a lightweight SSH daemon like `dropbear`
  works well, or `adb shell` as root if you prefer)
- [Go](https://go.dev/) on your PC, for cross-compiling the server binary
  (nothing needs to be installed on the Echo Show itself beyond the one
  binary)

## Status / disclaimer

This impersonates real Amazon domain names for images your own device
requests, using a certificate authority you install into your own,
already-rooted device's trust store. It only works because you have full
control of the device already — this is not a way to intercept or
redirect anyone else's traffic. Everything here was derived from
observing traffic on hardware the author owns.

Installing a custom CA weakens TLS trust validation for **the whole
device**, not just the two hijacked domains — every app's HTTPS
connections are, in principle, interceptable by anything that can use
that CA's private key. Keep the private key safe and understand this
trade-off before proceeding.

## Contents

- [`PROTOCOL.md`](PROTOCOL.md) — how the two photo endpoints were found,
  and their exact request/response format
- [`SETUP.md`](SETUP.md) — step-by-step setup instructions
- [`server/`](server) — the Go source for the on-device HTTPS server
- [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) — images stop showing after a
  while? Read this before debugging the server - it's almost certainly the
  client-side renderer getting stuck, not the server
