// Package store keeps the dataset in its cache folder (decisions 002, 009,
// 012): one file per column in a numbered folder, and a manifest that says
// which folder is current.
//
//	cache/manifest                  version, seed, count, folder, degree, skew
//	cache/lock                      the write lock (lock.go)
//	cache/dataset-000002/
//	    addresses.bin               20 bytes per address
//	    balances.bin                16 bytes per address, same order
//	    offsets.bin, neighbors.bin  the connections (CSR, decision 012)
//	    updates.log                 balance updates not yet folded in (updates.go)
//
// Row i of every column file belongs to the same address. A save writes a new
// numbered folder (a Draft), fsyncs it, and only then replaces the manifest,
// the single commit point; the old folder is deleted afterwards. A crash at
// any moment leaves the old manifest pointing at the complete old folder.
// Folders a save left unfinished are removed on the next start.
//
// The store never deletes a file or folder it finds in its way, because it
// cannot tell its own broken cache from someone else's data: -data may point
// at a file by mistake, and a folder chosen with -data may already hold an
// unrelated addresses.csv. Anything it cannot use is moved aside instead, to
// a new name next to it, and Events says where.
//
// Cache is the long-running program's view, which may create, move aside and
// clean up; OpenReadOnly is the queries' view, which changes nothing.
package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"evmgraph/internal/columns"
)

// Events is told what the store does to the cache folder, so the program can
// tell the person running it.
type Events interface {
	DirCreated(dir string)
	DirMovedAside(dir, movedTo string)
	MovedAside(path, movedTo, reason string)
	RemovedUnfinished(folder string)
	NotOurFolder(path string)
}

// Cache is a cache folder as the long-running program uses it.
type Cache struct {
	Dir    string
	Events Events
}

// Prepare makes sure the cache folder exists and is a directory, moving
// anything else in its place aside. The rest of Cache's methods need the
// write lock (TryLock), which lives in this folder.
func (c Cache) Prepare() error { return c.ensureDir() }

func (c Cache) ensureDir() error {
	info, err := os.Stat(c.Dir)
	switch {
	case err == nil && info.IsDir():
		return nil
	case err == nil:
		moved, err := moveAside(c.Dir)
		if err != nil {
			return err
		}
		c.Events.DirMovedAside(c.Dir, moved)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	c.Events.DirCreated(c.Dir)
	return os.MkdirAll(c.Dir, 0o755)
}

// Open loads the dataset the manifest names, or returns nil when there is
// none. A manifest that cannot be read as one, or whose files are missing,
// invalid or of the wrong length, is moved aside and counts as none; its
// folder is then left for Clean. Other errors, such as a missing permission,
// are returned as they are, so a good dataset is never touched.
//
// The program runs for long, so Open also checks every address and graph
// entry once, which the one-shot queries cannot afford (they check only the
// lists they read): addresses strictly increasing, lists strictly increasing
// and in range. A dataset that fails is set aside like a missing file.
func (c Cache) Open() (*Dataset, error) {
	// Temporary files of a manifest write that a crash interrupted. Only the
	// lock holder writes the manifest, so none of them is in progress.
	if err := columns.RemoveStaleTemps(ManifestPath(c.Dir)); err != nil {
		return nil, err
	}
	path := ManifestPath(c.Dir)
	b, err := readSmallRegularFile(path, maxManifest)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil && !errors.Is(err, errNotOurs) {
		return nil, err
	}
	if errors.Is(err, errNotOurs) {
		return nil, c.setAside(path, "it is not a small regular file")
	}
	m, err := ParseManifest(string(b))
	if err != nil {
		return nil, c.setAside(path, err.Error())
	}
	d, err := load(c.Dir, m)
	if errors.Is(err, errIncomplete) {
		return nil, c.setAside(path, err.Error())
	}
	if err != nil {
		return nil, err
	}
	if err := checkSorted(d); err != nil {
		d.Close()
		return nil, c.setAside(path, err.Error())
	}
	if m.HasGraph() {
		if err := d.Graph().Validate(); err != nil {
			d.Close()
			return nil, c.setAside(path, err.Error())
		}
	}
	return d, nil
}

// checkSorted reports a sorted dataset whose addresses are not strictly
// increasing: lookups would then give wrong answers, without an error
// (lookup.Find stays in bounds and ends whatever the data).
func checkSorted(d *Dataset) error {
	if !d.Manifest.Sorted() {
		return nil
	}
	a := d.Addresses.Addrs
	for i := 1; i < len(a); i++ {
		if bytes.Compare(a[i-1][:], a[i][:]) >= 0 {
			return fmt.Errorf("the addresses are out of order or repeated at row %d", i)
		}
	}
	return nil
}

// setAside moves path aside and reports why.
func (c Cache) setAside(path, reason string) error {
	moved, err := moveAside(path)
	if err != nil {
		return err
	}
	c.Events.MovedAside(path, moved, reason)
	return nil
}

// Clean removes dataset folders other than keep: those a save created but
// never committed, the previous one if deleting it failed, and one whose
// manifest was set aside. It removes only folders that hold nothing but files
// a save writes. Anything else is left alone and reported, including a folder
// with an update log: balance updates are what a person typed, and unlike the
// rest they cannot be generated again.
func (c Cache) Clean(keep string) error {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !folderName.MatchString(e.Name()) || e.Name() == keep {
			continue
		}
		path := filepath.Join(c.Dir, e.Name())
		if !e.IsDir() || !onlyOurFiles(path) {
			c.Events.NotOurFolder(path)
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		c.Events.RemovedUnfinished(path)
	}
	return nil
}

// onlyOurFiles reports whether folder holds only regular files with names a
// save writes: a column file or its temporary file.
func onlyOurFiles(folder string) bool {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return false
	}
	ours := map[string]bool{columns.AddressesFileName: true, columns.BalancesFileName: true,
		columns.OffsetsFileName: true, columns.NeighborsFileName: true}
	for _, e := range entries {
		if !e.Type().IsRegular() || !isOurFile(e.Name(), ours) {
			return false
		}
	}
	return true
}

// isOurFile reports whether name is a column file or a save's temporary file
// of one (<column file>.<random>.tmp).
func isOurFile(name string, ours map[string]bool) bool {
	if ours[name] {
		return true
	}
	if !strings.HasSuffix(name, ".tmp") {
		return false
	}
	for n := range ours {
		if strings.HasPrefix(name, n+".") {
			return true
		}
	}
	return false
}

// nextFolder names a new dataset folder, one higher than any existing one.
func nextFolder(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	highest := 0
	for _, e := range entries {
		if m := folderName.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			highest = max(highest, n)
		}
	}
	return fmt.Sprintf("dataset-%06d", highest+1), nil
}

// moveAside renames path to path+".invalid-<date>-<time>", adding "-2", "-3"
// and so on if that name is taken, and returns the new name. It renames a
// symlink itself, never the file it points to, and it never replaces anything
// that already exists.
func moveAside(path string) (string, error) {
	base := path + ".invalid-" + time.Now().Format("20060102-150405")
	for i := 1; i < 1000; i++ {
		moved := base
		if i > 1 {
			moved = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Lstat(moved); err == nil {
			continue // taken: os.Rename would replace it
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if err := os.Rename(path, moved); err != nil {
			return "", err
		}
		return moved, nil
	}
	return "", fmt.Errorf("cannot move %s aside: too many %s* files", path, base)
}
