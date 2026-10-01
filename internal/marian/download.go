package marian

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// RemoteFile is a model file to download, checked against its size and
// SHA-256 digest.
type RemoteFile struct {
	Name   string
	URL    string
	Size   int64
	SHA256 string
}

// Installed reports whether dir holds every file of a model.
func Installed(dir string) bool {
	for _, f := range Files {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.Size() == 0 {
			return false
		}
	}
	return true
}

// Download fetches files into dir. Each file is written next to its
// final name and renamed once its digest is checked, so an interrupted
// download never leaves a half-written model behind. progress receives
// the bytes downloaded so far and the total.
func Download(ctx context.Context, client *http.Client, dir string, files []RemoteFile, progress func(done, total int64)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var total, done int64
	for _, f := range files {
		total += f.Size
	}
	for _, f := range files {
		n, err := download(ctx, client, dir, f, func(n int64) {
			if progress != nil {
				progress(done+n, total)
			}
		})
		if err != nil {
			return fmt.Errorf("downloading %s: %w", f.Name, err)
		}
		done += n
	}
	return nil
}

func download(ctx context.Context, client *http.Client, dir string, f RemoteFile, progress func(int64)) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("server answered %s", resp.Status)
	}
	tmp, err := os.CreateTemp(dir, "."+f.Name+".*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	hash := sha256.New()
	w := &countingWriter{w: io.MultiWriter(tmp, hash), progress: progress}
	_, err = io.Copy(w, io.LimitReader(resp.Body, f.Size+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if w.n != f.Size {
		return 0, fmt.Errorf("got %d bytes, expected %d", w.n, f.Size)
	}
	if sum := hex.EncodeToString(hash.Sum(nil)); sum != f.SHA256 {
		return 0, fmt.Errorf("checksum mismatch")
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return 0, err
	}
	return w.n, os.Rename(tmp.Name(), filepath.Join(dir, f.Name))
}

type countingWriter struct {
	w        io.Writer
	n        int64
	progress func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.progress(c.n)
	return n, err
}
