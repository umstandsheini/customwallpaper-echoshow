# Protocol notes: how the Echo Show fetches ambient-screen photos

## How this was found

Traffic was captured by running a local MITM proxy (e.g.
[mitmproxy](https://mitmproxy.org/)) on a PC, pointing the Echo Show's
global HTTP proxy setting at it (`settings put global http_proxy
<pc-ip>:<port>`), and installing the proxy's CA certificate into the
device's system trust store (`/system/etc/security/cacerts/`, filename =
`openssl x509 -subject_hash_old -in ca.pem -noout` + `.0`, after
`mount -o rw,remount /system`). This only works because the device is
rooted — installing a *user* CA (no root needed) is not trusted by
the Alexa app's HTTPS connections on modern Android (apps must opt in to
trusting user CAs, and Amazon's don't).

With that in place, everything the ambient screen loads shows up in the
proxy in plaintext.

## Endpoint 1: "Art" / curated categories (easy target)

```
GET https://d1cg7g7aedi1wy.cloudfront.net/fit-in/{width}x{height}/filters:quality(95):sharpen(.5,0.10,true)/ATVPDKIKX0DER/Art/{filename}.jpg
```

- No meaningful personalization. Response is a plain JPEG.
- Filenames observed match real museum inventory numbers (e.g.
  `SK-A-2579.jpg` is a Rijksmuseum object ID) — this category is served
  from a public-domain art collection via a
  [Thumbor](https://github.com/thumbor/thumbor)-style resize proxy on a
  generic AWS CloudFront distribution.
- An `x-amz-access-token` header is sent by the app on every request to
  Amazon-owned domains, including this one, but the response looks like a
  standard cacheable CloudFront/S3 asset (`Cache-Control`, `X-Cache: Hit
  from cloudfront`, no personalization headers) — nothing suggests this
  particular endpoint validates it.
- Because there's no meaningful per-request validation, **the exact path
  or filename requested can be ignored entirely** — serve any image you
  want for any request to this host and the app will display it.

## Endpoint 2: "Personal Photos" (Amazon Photos)

```
GET https://thumbnails-photos.amazon.de/v1/thumbnail/{photoId}?cropOffset=0,0&cropSize={w},{h}&viewBox={w},{h}&ownerId={ownerId}
Header: x-amz-access-token: Atna|...   (account bearer token)
```

- `{photoId}` is an opaque per-account Amazon Photos asset ID,
  `{ownerId}` identifies the Amazon account.
- Response: plain JPEG, `Cache-Control: private, max-age=31536000`.
- The `viewBox` query parameter matches the device's screen resolution
  exactly (e.g. `960,480` on an Echo Show 5) — the server returns an
  image already sized for display.
- As with endpoint 1: since we're replacing the server entirely, the
  specific `{photoId}` requested doesn't need to be understood or
  replicated — the app just wants "some image" for whatever ID it has
  cached client-side from a separate (never-observed-in-plaintext-here)
  listing call. Serving a different image than what that ID "should" be
  goes unnoticed by the app.

## Bonus finding: sponsored-content domains

While investigating, two unrelated ad/sponsored-content domains were
identified and can be blocked (return nothing, e.g. via a `/etc/hosts`
entry to `127.0.0.1` with nothing listening there, or via the server
below returning a 404) without affecting the photo endpoints:

- `cdn.prod.adskit.juno.alexa.amazon.dev` (ad creative/layout delivery)
- `*.turntable.sonic.advertising.amazon.dev` (ad impression/viewability
  tracking, `/ads/v1/reportEvent`)

## Architecture used to serve fake content

1. `/etc/hosts` (on `/system`, needs `mount -o rw,remount /system` first)
   gets one line per hijacked hostname, pointing it at `127.0.0.1`.
2. A CA certificate is installed into the system trust store (see
   above).
3. A leaf TLS certificate is generated for the hijacked hostname(s),
   signed by that CA (see [`server/gencert`](server/gencert)).
4. A small HTTPS server ([`server/main.go`](server/main.go)) runs
   directly on the device (as root, to bind port 443), presents that leaf
   certificate, and serves your own images for any request to the
   hijacked hostnames — the requested path is ignored.

See [`SETUP.md`](SETUP.md) for the full step-by-step.
