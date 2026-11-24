package fwupd

import (
	"context"
	"crypto/sha1" //nolint:gosec // the LVFS lists SHA-1 next to SHA-256; SHA-256 is preferred
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// MaxSize is the largest cabinet Download accepts. The biggest LVFS
// payloads (system firmware, docks) are tens of MiB.
const MaxSize = 512 << 20

// Download fetches the release's cabinet from its first HTTPS location into
// an unlinked file in dir ("" is the system's temporary directory), checks
// its size and checksum, and returns it rewound. The caller closes it.
// fwupd checks the signature itself; the checksum here catches a corrupt or
// a wrong file before it reaches the daemon.
func Download(ctx context.Context, client *http.Client, r Release, dir string) (*os.File, error) {
	loc := ""
	for _, l := range r.Locations {
		if u, err := url.Parse(l); err == nil && u.Scheme == "https" && u.Host != "" {
			loc = l
			break
		}
	}
	if loc == "" {
		return nil, errors.New("the release has no HTTPS download location")
	}
	want, h := pickChecksum(r.Checksums)
	if h == nil {
		return nil, errors.New("the release has no SHA-256 or SHA-1 checksum")
	}
	if r.Size > MaxSize {
		return nil, fmt.Errorf("the release is %d bytes, more than the %d this accepts", r.Size, MaxSize)
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL.Scheme != "https" {
		return nil, errors.New("the download was redirected away from HTTPS")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", loc, resp.Status)
	}
	f, err := os.CreateTemp(dir, "isotope-firmware-*.cab")
	if err != nil {
		return nil, err
	}
	// Unlinked at once: nothing is left behind, whatever happens next.
	os.Remove(f.Name())
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, MaxSize+1))
	if err == nil && n > MaxSize {
		err = fmt.Errorf("the download is larger than %d bytes", MaxSize)
	}
	if err == nil && r.Size != 0 && uint64(n) != r.Size {
		err = fmt.Errorf("the download is %d bytes, the release says %d", n, r.Size)
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = errors.New("the download does not match the release's checksum")
	}
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// pickChecksum takes the release's SHA-256, else its SHA-1.
func pickChecksum(sums []string) (string, hash.Hash) {
	for _, s := range sums {
		if s = strings.ToLower(s); len(s) == 64 && isHex(s) {
			return s, sha256.New()
		}
	}
	for _, s := range sums {
		if s = strings.ToLower(s); len(s) == 40 && isHex(s) {
			return s, sha1.New() //nolint:gosec
		}
	}
	return "", nil
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}
