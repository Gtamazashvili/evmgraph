// Package columns writes the dataset's column files and maps them back
// (decisions 002, 009): addresses.bin (20 bytes per row), balances.bin (16)
// and the graph's offsets.bin and neighbors.bin (4-byte uint32s). The files
// are plain arrays of fixed-size records, so opening one maps it into memory
// and uses it in place: nothing is decoded, and only the pages a lookup
// touches are read.
//
// Every file is written the same way (Write):
//
//   - a new temporary file next to the target, created exclusively (O_EXCL)
//     with a random name, so a write never goes through a symlink or into a
//     file that already exists; RemoveStaleTemps cleans up after a crash;
//   - ctx is checked, and progress reported, every Batch records; when ctx is
//     cancelled the temporary file is removed and ctx.Err() returned;
//   - fsync every syncEvery bytes while writing, and at the end, then rename
//     into place, so the target is always either the previous complete file
//     or the new complete file.
//
// Opening returns an error wrapping fs.ErrNotExist for a missing file and
// ErrInvalidFile for a file that cannot hold the records, including a
// directory in its place. Other errors, such as a missing permission, are
// returned as they are.
package columns

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"strings"
)

// Batch is how many records are written between checks for cancellation and
// progress reports.
const Batch = 1 << 16

// ErrInvalidFile marks a file that exists but cannot hold the records. Other
// errors, such as a missing permission, are not the file's fault.
var ErrInvalidFile = errors.New("invalid column file")

// RecordWriter writes records one at a time in one encoding. i is the
// record's position in the whole file, counting from 0.
type RecordWriter[T any] interface {
	Write(i int, v *T) error
	Close() error // writes any trailer and flushes buffered data
}

// Parts yields the records of parts in order, as one sequence.
func Parts[T any](parts ...[]T) iter.Seq[*T] {
	return func(yield func(*T) bool) {
		for _, part := range parts {
			for i := range part {
				if !yield(&part[i]) {
					return
				}
			}
		}
	}
}

// Write writes records to path the safe way the package describes: a new
// temporary file, checking ctx and reporting progress every Batch records,
// fsync, then rename into place. On any error, including cancellation, the
// temporary file is removed. progress, if not nil, is called after every
// Batch records and once at the end with the total.
func Write[T any](ctx context.Context, path string, progress func(saved int), records iter.Seq[*T],
	newWriter func(io.Writer) (RecordWriter[T], error)) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// os.CreateTemp picks a random name and opens it with O_CREATE|O_EXCL: if
	// anything already exists under that name, a symlink included, it fails
	// instead of following or truncating it. A predictable name such as
	// path+".tmp" would let anyone who can write to the folder plant a symlink
	// there and have the write overwrite the file it points to.
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()

	bw := bufio.NewWriterSize(&syncingWriter{f: f}, 1<<20)
	rw, err := newWriter(bw)
	if err != nil {
		return err
	}
	saved := 0
	for v := range records {
		if saved%Batch == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if progress != nil && saved > 0 {
				progress(saved)
			}
		}
		if err := rw.Write(saved, v); err != nil {
			return err
		}
		saved++
	}
	if err := rw.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if progress != nil {
		progress(saved)
	}
	return nil
}

// syncEvery is how many bytes a write lets the operating system hold before
// it fsyncs them. Unwritten file data sits in the page cache, which counts
// against a container's memory limit; Docker Desktop's kernel did not write
// it back on its own before the limit was reached, and a 20M save near the
// limit was killed holding 316 MB of it (tests/docker,
// TestRunsCloseToTheMemoryLimit). Syncing as it goes keeps that below 32 MB
// and cost nothing measurable: a 20M save took 3.6–4.6 s either way.
const syncEvery = 32 << 20

// syncingWriter passes writes to f and fsyncs f every syncEvery bytes.
type syncingWriter struct {
	f       *os.File
	pending int
}

func (w *syncingWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if w.pending += n; err == nil && w.pending >= syncEvery {
		w.pending, err = 0, w.f.Sync()
	}
	return n, err
}

// rawWriter writes fixed-size records as their bytes.
type rawWriter[T any] struct {
	w     io.Writer
	bytes func(*T) []byte
}

func (r rawWriter[T]) Write(_ int, v *T) error { _, err := r.w.Write(r.bytes(v)); return err }
func (r rawWriter[T]) Close() error            { return nil }

// writeRaw writes records as their bytes, back to back.
func writeRaw[T any](ctx context.Context, path string, progress func(int), records iter.Seq[*T], bytes func(*T) []byte) error {
	return Write(ctx, path, progress, records, func(w io.Writer) (RecordWriter[T], error) {
		return rawWriter[T]{w: w, bytes: bytes}, nil
	})
}

// RemoveStaleTemps deletes the temporary files a write into path leaves
// behind when the process is killed before it can clean up (kill -9, a crash
// or a power cut). It removes only names a write can create,
// path+".<digits>.tmp" and the older path+".tmp", and only regular files and
// symlinks; a symlink is removed itself, never the file it points to.
// Anything else is left alone.
func RemoveStaleTemps(path string) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !isTempName(e.Name(), base) {
			continue
		}
		if t := e.Type(); !t.IsRegular() && t&fs.ModeSymlink == 0 {
			continue // a directory or device with a matching name is not ours
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// isTempName reports whether name is a temporary file of a write into base:
// base+".tmp", or base+"."+digits+".tmp", the digits being what os.CreateTemp
// puts in place of the "*".
func isTempName(name, base string) bool {
	if name == base+".tmp" {
		return true
	}
	mid, ok := strings.CutPrefix(name, base+".")
	if !ok {
		return false
	}
	mid, ok = strings.CutSuffix(mid, ".tmp")
	return ok && mid != "" && strings.Trim(mid, "0123456789") == ""
}
