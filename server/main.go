// Standalone HTTPS server, meant to run directly on a rooted Echo Show.
// Impersonates d1cg7g7aedi1wy.cloudfront.net (the Amazon "Art" category
// image CDN) via a /etc/hosts redirect to 127.0.0.1 + a leaf cert signed
// by an already-installed, system-trusted CA. See ../SETUP.md.
//
// Image source: this example pulls from a public iCloud "Shared Album"
// (no login needed, just the share link). Swap fetchAlbumIndex() /
// downloadPhoto() for any other image source you like (a local folder, a
// different cloud photo service, etc.) - nothing else needs to change.
//
// Instead of syncing a whole photo library to disk, it keeps a small
// rolling cache:
//   - a "reserve pool" of at least MinReserve already-downloaded, not yet
//     assigned photos, refilled in the background so a request for a new
//     URL is never blocked on a live download
//   - a URL -> local file mapping, so repeat requests for the same
//     "artwork" path keep showing the same photo
//   - a hard cap on total cached files, evicting the oldest when exceeded
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

// Target size to downscale cached photos to. Match this to your device's
// actual screen resolution - Amazon's real thumbnail service does the same
// (returns images pre-sized to the requested viewBox), and decoding a
// full-resolution original as a Bitmap on old, memory-constrained devices
// can fail (returns a null Bitmap) instead of erroring loudly.
const (
	TargetWidth  = 960
	TargetHeight = 480
)

const (
	// Get this from an iCloud.com "Shared Album" link:
	// https://www.icloud.com/sharedalbum/#<TOKEN>  <- the part after the #
	AlbumToken = "REPLACE_WITH_YOUR_OWN_SHARED_ALBUM_TOKEN"
	CacheDir   = "/sdcard/Pictures/wallpaper_cache"
	MinReserve = 3  // always keep at least this many spare, unassigned photos ready
	MaxOnDisk  = 12 // hard cap on total cached files (reserve + assigned)
	ListenAddr = ":443"
	CertFile   = "leaf-cert.pem"
	KeyFile    = "leaf-key.pem"
)

// ---------- DNS workaround ----------
//
// Go's default DNS resolution doesn't work reliably on Android (no usable
// /etc/resolv.conf, netd isn't reachable the way Go's resolver expects).
// We use a custom resolver that queries a public DNS server directly, and
// wire it into a custom http.Client used for all outbound requests.

var dnsResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, "udp", "8.8.8.8:53")
	},
}

// Go's default TLS trust store lookup doesn't find Android's CA store
// (/system/etc/security/cacerts/*.0, individually hashed PEM files) since
// it isn't a layout Go recognizes on GOOS=linux. Load it explicitly.
func loadAndroidCAPool() *x509.CertPool {
	pool := x509.NewCertPool()
	const dir = "/system/etc/security/cacerts"
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Println("could not read Android CA dir:", err)
		return pool
	}
	loaded := 0
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if pool.AppendCertsFromPEM(data) {
			loaded++
		}
	}
	log.Println("loaded", loaded, "Android system CA certs")
	return pool
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := dnsResolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("resolve %s: %v", host, err)
			}
			d := net.Dialer{Timeout: 10 * time.Second}
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSClientConfig: &tls.Config{
			RootCAs: loadAndroidCAPool(),
		},
	},
	Timeout: 30 * time.Second,
}

// ---------- example image source: iCloud shared album ----------

type sourcePhoto struct {
	GUID string
	URL  string
}

func icloudPost(host, path string, body map[string]interface{}) (map[string]interface{}, string, error) {
	b, _ := json.Marshal(body)
	url := fmt.Sprintf("https://%s/%s/sharedstreams/%s", host, AlbumToken, path)
	req, err := http.NewRequest("POST", url, io.NopCloser(newReader(b)))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "", err
	}
	if next, ok := result["X-Apple-MMe-Host"].(string); ok {
		return icloudPost(next, path, body)
	}
	return result, host, nil
}

// fetchSourceIndex returns the list of available photos. Replace this
// with a call to whatever image source you want to use instead.
func fetchSourceIndex() ([]sourcePhoto, error) {
	webstream, host, err := icloudPost("p01-sharedstreams.icloud.com", "webstream", map[string]interface{}{"streamCtag": nil})
	if err != nil {
		return nil, err
	}
	photosRaw, _ := webstream["photos"].([]interface{})

	checksumByGUID := map[string]string{}
	var guids []string
	for _, p := range photosRaw {
		photo := p.(map[string]interface{})
		if photo["mediaAssetType"] == "video" {
			continue
		}
		derivs, _ := photo["derivatives"].(map[string]interface{})
		if len(derivs) == 0 {
			continue
		}
		var bestChecksum string
		var bestSize int64
		for _, d := range derivs {
			deriv := d.(map[string]interface{})
			var size int64
			fmt.Sscanf(fmt.Sprint(deriv["fileSize"]), "%d", &size)
			if size > bestSize {
				bestSize = size
				bestChecksum, _ = deriv["checksum"].(string)
			}
		}
		guid, _ := photo["photoGuid"].(string)
		checksumByGUID[guid] = bestChecksum
		guids = append(guids, guid)
	}

	urlsResp, _, err := icloudPost(host, "webasseturls", map[string]interface{}{"photoGuids": guids})
	if err != nil {
		return nil, err
	}
	items, _ := urlsResp["items"].(map[string]interface{})

	var result []sourcePhoto
	for guid, checksum := range checksumByGUID {
		item, ok := items[checksum].(map[string]interface{})
		if !ok {
			continue
		}
		loc, _ := item["url_location"].(string)
		path, _ := item["url_path"].(string)
		result = append(result, sourcePhoto{GUID: guid, URL: "https://" + loc + path})
	}
	return result, nil
}

// resizeCover scales img so it fully covers a targetW x targetH box
// (like CSS object-fit: cover), then center-crops to exactly that size.
func resizeCover(img image.Image, targetW, targetH int) image.Image {
	b := img.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW == 0 || srcH == 0 {
		return img
	}
	scale := float64(targetW) / float64(srcW)
	if s2 := float64(targetH) / float64(srcH); s2 > scale {
		scale = s2
	}
	scaledW := int(float64(srcW)*scale + 0.5)
	scaledH := int(float64(srcH)*scale + 0.5)
	if scaledW < targetW {
		scaledW = targetW
	}
	if scaledH < targetH {
		scaledH = targetH
	}

	scaled := image.NewRGBA(image.Rect(0, 0, scaledW, scaledH))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), img, b, draw.Over, nil)

	left := (scaledW - targetW) / 2
	top := (scaledH - targetH) / 2
	cropped := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	draw.Draw(cropped, cropped.Bounds(), scaled, image.Point{X: left, Y: top}, draw.Src)
	return cropped
}

// downloadPhoto fetches one photo's bytes to disk. Replace alongside
// fetchSourceIndex() if you swap the image source.
func downloadPhoto(p sourcePhoto, destDir string) (string, error) {
	resp, err := httpClient.Get(p.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Some source photos (notably iPhone Portrait-mode shots) are MPO
	// files - a JPEG container with extra embedded frames for depth data.
	// Android's Bitmap decoder on old FireOS builds sometimes returns a
	// null bitmap for those and gets stuck showing nothing at all. Go's
	// jpeg decoder reads just the primary frame and ignores the extra MPO
	// markers, so decoding here normalizes every photo before it ever
	// reaches the device.
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", p.GUID, err)
	}
	resized := resizeCover(img, TargetWidth, TargetHeight)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 90}); err != nil {
		return "", fmt.Errorf("re-encode %s: %w", p.GUID, err)
	}

	dest := filepath.Join(destDir, p.GUID+".jpg")
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(buf.Bytes()); err != nil {
		return "", err
	}
	return dest, nil
}

// ---------- rolling cache ----------

type cache struct {
	mu          sync.Mutex
	reserve     []string          // paths of downloaded, not-yet-assigned photos
	assigned    map[string]string // request path -> local file path
	assignedLRU []string          // request paths in assignment order, oldest first
	usedGUIDs   map[string]bool   // GUIDs already downloaded (avoid repeats while the source is small)
	sourceIndex []sourcePhoto
}

func newCache() *cache {
	os.MkdirAll(CacheDir, 0755)
	return &cache{
		assigned:  map[string]string{},
		usedGUIDs: map[string]bool{},
	}
}

func (c *cache) refreshSourceIndex() {
	idx, err := fetchSourceIndex()
	if err != nil {
		log.Println("source index fetch failed:", err)
		return
	}
	c.mu.Lock()
	c.sourceIndex = idx
	c.mu.Unlock()
}

// fetchOneNewPhoto downloads a single photo not already used, cycling back
// to the start of the source once everything has been shown at least once.
func (c *cache) fetchOneNewPhoto() (string, error) {
	c.mu.Lock()
	if len(c.sourceIndex) == 0 {
		c.mu.Unlock()
		return "", fmt.Errorf("source index empty")
	}
	var candidates []sourcePhoto
	for _, p := range c.sourceIndex {
		if !c.usedGUIDs[p.GUID] {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		// everything shown at least once - start reusing from the top
		c.usedGUIDs = map[string]bool{}
		candidates = c.sourceIndex
	}
	pick := candidates[rand.Intn(len(candidates))]
	c.usedGUIDs[pick.GUID] = true
	c.mu.Unlock()

	return downloadPhoto(pick, CacheDir)
}

// maintainReserve runs forever in the background, keeping at least
// MinReserve spare downloaded photos ready to hand out instantly.
func (c *cache) maintainReserve() {
	for {
		c.mu.Lock()
		need := MinReserve - len(c.reserve)
		c.mu.Unlock()

		if need > 0 {
			path, err := c.fetchOneNewPhoto()
			if err != nil {
				log.Println("reserve refill failed:", err)
				time.Sleep(10 * time.Second)
				continue
			}
			c.mu.Lock()
			c.reserve = append(c.reserve, path)
			c.mu.Unlock()
			c.enforceDiskCap()
			log.Println("reserve topped up:", path, "reserve size now", len(c.reserve))
			continue
		}
		time.Sleep(2 * time.Second)
	}
}

// enforceDiskCap deletes the oldest assigned photo if we're over MaxOnDisk.
func (c *cache) enforceDiskCap() {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := len(c.reserve) + len(c.assigned)
	for total > MaxOnDisk && len(c.assignedLRU) > 0 {
		oldestPath := c.assignedLRU[0]
		c.assignedLRU = c.assignedLRU[1:]
		oldFile := c.assigned[oldestPath]
		delete(c.assigned, oldestPath)
		os.Remove(oldFile)
		log.Println("evicted:", oldFile, "(was serving", oldestPath, ")")
		total--
	}
}

// servePath returns the local file to serve for a given request path,
// assigning a fresh reserve photo if this path hasn't been seen before.
func (c *cache) servePath(requestPath string) (string, error) {
	c.mu.Lock()
	if existing, ok := c.assigned[requestPath]; ok {
		c.mu.Unlock()
		return existing, nil
	}
	if len(c.reserve) == 0 {
		c.mu.Unlock()
		// extremely unlikely (reserve maintainer keeps it topped up) - fall
		// back to a synchronous fetch rather than fail the request
		path, err := c.fetchOneNewPhoto()
		if err != nil {
			return "", err
		}
		c.mu.Lock()
		c.assigned[requestPath] = path
		c.assignedLRU = append(c.assignedLRU, requestPath)
		c.mu.Unlock()
		return path, nil
	}
	picked := c.reserve[0]
	c.reserve = c.reserve[1:]
	c.assigned[requestPath] = picked
	c.assignedLRU = append(c.assignedLRU, requestPath)
	c.mu.Unlock()
	return picked, nil
}

func newReader(b []byte) *byteReader { return &byteReader{b: b} }

type byteReader struct {
	b   []byte
	pos int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

// ---------- HTTPS server ----------

func main() {
	rand.Seed(time.Now().UnixNano())
	c := newCache()

	log.Println("fetching initial source index...")
	c.refreshSourceIndex()
	go func() {
		for {
			time.Sleep(15 * time.Minute)
			c.refreshSourceIndex()
		}
	}()

	go c.maintainReserve()

	// enforceDiskCap normally only runs right after a reserve top-up, so
	// if the client stops requesting images for a while (e.g. it's showing
	// something else), stale cached files never get cleaned up. Run it
	// independently on a timer too.
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			c.enforceDiskCap()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path, err := c.servePath(r.URL.Path)
		if err != nil {
			http.Error(w, "no photo available", 503)
			log.Println("serve error:", err)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeFile(w, r, path)
		log.Println("served", path, "for", r.URL.Path)
	})

	server := &http.Server{
		Addr:    ListenAddr,
		Handler: mux,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	log.Println("listening on", ListenAddr)
	log.Fatal(server.ListenAndServeTLS(CertFile, KeyFile))
}
