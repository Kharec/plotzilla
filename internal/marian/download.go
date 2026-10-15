package marian

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// Download fetches files into dir. A file already there with the right
// size and digest is skipped. A file is downloaded into a ".partial" file
// that is renamed only once its digest checks out, so an interrupted
// download never leaves a half-written model behind; the partial file is
// kept and the next Download resumes it where it stopped. progress
// receives the bytes downloaded so far and the total.
func Download(ctx context.Context, client *http.Client, dir string, files []RemoteFile, progress func(done, total int64)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var total, done int64
	for _, f := range files {
		total += f.Size
	}
	report := func(n int64) {
		if progress != nil {
			progress(done+n, total)
		}
	}
	for _, f := range files {
		report(0)
		n, err := download(ctx, client, dir, f, report)
		if err != nil {
			return fmt.Errorf("downloading %s: %w", f.Name, err)
		}
		done += n
		report(0)
	}
	return nil
}

// download fetches one file, resuming the partial download left by an
// interrupted run when the server supports it.
func download(ctx context.Context, client *http.Client, dir string, f RemoteFile, progress func(int64)) (int64, error) {
	final := filepath.Join(dir, f.Name)
	if ok, err := verify(final, f); err != nil {
		return 0, err
	} else if ok {
		return f.Size, nil // downloaded by a previous run
	}
	partial := filepath.Join(dir, "."+f.Name+".partial")
	var off int64
	if st, err := os.Stat(partial); err == nil && st.Size() > 0 && st.Size() <= f.Size {
		off = st.Size()
	}
	if off == f.Size {
		// A previous run was interrupted between the digest check and
		// the rename.
		if ok, _ := verify(partial, f); !ok {
			off = 0 // corrupt: start over
		} else if err := os.Rename(partial, final); err != nil {
			return 0, err
		} else {
			return f.Size, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return 0, err
	}
	if off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if off > 0 && resp.StatusCode == http.StatusOK {
		off = 0 // the server cannot resume: start over
	}
	want := http.StatusOK
	if off > 0 {
		want = http.StatusPartialContent
	}
	if resp.StatusCode != want {
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			os.Remove(partial) // the partial file cannot be resumed
		}
		return 0, fmt.Errorf("server answered %s", resp.Status)
	}
	tmp, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return 0, err
	}
	hash := sha256.New()
	if off > 0 {
		// The digest covers the whole file: seed it with the bytes
		// already on disk.
		if _, err := io.CopyN(hash, tmp, off); err != nil {
			tmp.Close()
			return 0, fmt.Errorf("reading the partial download: %w", err)
		}
	} else if err := tmp.Truncate(0); err != nil {
		tmp.Close()
		return 0, err
	}
	if _, err := tmp.Seek(off, io.SeekStart); err != nil {
		tmp.Close()
		return 0, err
	}
	w := &countingWriter{w: io.MultiWriter(tmp, hash), progress: func(n int64) { progress(off + n) }}
	_, err = io.Copy(w, io.LimitReader(resp.Body, f.Size-off+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err // the partial file stays, for the next run
	}
	if got := off + w.n; got != f.Size {
		return 0, fmt.Errorf("got %d bytes, expected %d", got, f.Size)
	}
	if sum := hex.EncodeToString(hash.Sum(nil)); sum != f.SHA256 {
		os.Remove(partial) // corrupt: resuming it would fail again
		return 0, fmt.Errorf("checksum mismatch")
	}
	if err := os.Chmod(partial, 0o644); err != nil {
		return 0, err
	}
	return f.Size, os.Rename(partial, final)
}

// verify reports whether path holds the file, with its expected size and
// digest. A missing file is not an error.
func verify(path string, f RemoteFile) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if st.Size() != f.Size {
		return false, nil
	}
	sum, err := hashFile(path)
	if err != nil {
		return false, err
	}
	return sum == f.SHA256, nil
}

func hashFile(path string) (string, error) {
	r, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
