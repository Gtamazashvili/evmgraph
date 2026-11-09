package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"evmgraph/internal/generate"
)

const (
	manifestName  = "manifest"
	manifestMagic = "evmgraph dataset"
	maxManifest   = 1024 // bytes; a longer file is not a manifest
)

// Manifest versions:
//
//	1  rows in generation order (8 November 2026, before decision 010)
//	2  rows sorted by address, so lookup.Find can search the address file
//	3  also the connections: offsets.bin and neighbors.bin, and the degree
//	   and skew they were drawn with (decisions 011, 012)
//	4  balances from the realistic model (decision 006); no format line, as
//	   addresses are always binary (decision 002); the folder may hold
//	   updates.log, balance updates not yet folded in (decision 014)
//
// The program writes only version 4, and generates an older dataset again on
// its next run. The queries read versions 2 to 4; connections needs 3 or 4.
// Versions 1 to 3 name their address format, which must be binary.
const (
	VersionGenerationOrder = 1
	VersionSorted          = 2
	VersionConnections     = 3
	VersionCurrent         = 4
)

// folderName matches the numbered dataset folders a save creates.
var folderName = regexp.MustCompile(`^dataset-([0-9]{6,})$`)

// Manifest describes the cache's current dataset.
type Manifest struct {
	Version int
	Seed    uint64
	Count   int    // addresses, and so balances
	Folder  string // "dataset-000002"
	Degree  int    // average connections per address (version 3)
	Skew    int    // the graph model's skew (version 3)
}

// Sorted reports whether the rows are in address order, so they can be
// searched.
func (m Manifest) Sorted() bool { return m.Version >= VersionSorted }

// HasGraph reports whether the dataset holds its connections.
func (m Manifest) HasGraph() bool { return m.Version >= VersionConnections }

// Current reports whether the dataset is what this program generates; an
// older one is generated again on the next run.
func (m Manifest) Current() bool { return m.Version == VersionCurrent }

// String is the manifest's file content.
func (m Manifest) String() string {
	s := fmt.Sprintf("%s\nversion %d\n", manifestMagic, m.Version)
	if m.Version < VersionCurrent {
		s += "format binary\n" // the only format older versions can still be read in
	}
	s += fmt.Sprintf("seed %d\naddresses %d\nfolder %s\n", m.Seed, m.Count, m.Folder)
	if m.HasGraph() {
		s += fmt.Sprintf("degree %d\nskew %d\n", m.Degree, m.Skew)
	}
	return s
}

// ManifestPath is where the cache folder dir keeps its manifest.
func ManifestPath(dir string) string { return filepath.Join(dir, manifestName) }

// ParseManifest reads a manifest's text strictly: every field of its
// version, once, in any order, and nothing else.
func ParseManifest(text string) (Manifest, error) {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) == 0 || lines[0] != manifestMagic {
		return Manifest{}, errors.New("not an evmgraph manifest")
	}
	var m Manifest
	seen := map[string]bool{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, " ")
		if !ok || seen[key] {
			return Manifest{}, fmt.Errorf("bad line %q", line)
		}
		seen[key] = true
		var err error
		switch key {
		case "version":
			m.Version, err = strconv.Atoi(value)
			if err == nil && (m.Version < VersionGenerationOrder || m.Version > VersionCurrent) {
				err = fmt.Errorf("version %s, this program reads versions %d to %d", value, VersionGenerationOrder, VersionCurrent)
			}
		case "format":
			if value != "binary" {
				err = fmt.Errorf("format %q: the program now reads only binary addresses", value)
			}
		case "seed":
			m.Seed, err = strconv.ParseUint(value, 10, 64)
		case "addresses":
			m.Count, err = strconv.Atoi(value)
			if err == nil && m.Count < 0 {
				err = errors.New("negative address count")
			}
		case "folder":
			if !folderName.MatchString(value) {
				err = fmt.Errorf("bad folder %q", value)
			}
			m.Folder = value
		case "degree":
			m.Degree, err = strconv.Atoi(value)
			if err == nil {
				err = CheckDegree(m.Degree)
			}
		case "skew":
			m.Skew, err = strconv.Atoi(value)
			if err == nil {
				err = CheckSkew(m.Skew)
			}
		default:
			err = fmt.Errorf("unknown field %q", key)
		}
		if err != nil {
			return Manifest{}, err
		}
	}
	required := []string{"version", "seed", "addresses", "folder"}
	if m.Version < VersionCurrent {
		required = append(required, "format")
	} else if seen["format"] {
		return Manifest{}, fmt.Errorf("version %d has no format line", VersionCurrent)
	}
	if m.HasGraph() {
		required = append(required, "degree", "skew")
	} else if seen["degree"] || seen["skew"] {
		return Manifest{}, fmt.Errorf("degree and skew belong to version %d", VersionConnections)
	}
	for _, key := range required {
		if !seen[key] {
			return Manifest{}, fmt.Errorf("no %s", key)
		}
	}
	return m, nil
}

// MaxDegree bounds the degree; with 4-byte offsets the graph also needs
// count × degree below 2^32 (graph.MaxEntries).
const MaxDegree = 1000

// CheckDegree reports a degree the graph model cannot use: it must be even,
// as each link adds one entry to both endpoints' lists.
func CheckDegree(d int) error {
	if d < 0 || d > MaxDegree || d%2 != 0 {
		return fmt.Errorf("degree %d: it must be an even number from 0 to %d", d, MaxDegree)
	}
	return nil
}

// CheckSkew reports a skew the graph model cannot use.
func CheckSkew(s int) error {
	if s < generate.MinSkew || s > generate.MaxSkew {
		return fmt.Errorf("skew %d: it must be from %d to %d", s, generate.MinSkew, generate.MaxSkew)
	}
	return nil
}

// errNotOurs marks a file in the cache's way that a save did not write.
var errNotOurs = errors.New("not written by this program")

// readSmallRegularFile reads path if it is a regular file of at most limit
// bytes, and returns errNotOurs if it is anything else.
func readSmallRegularFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errNotOurs
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// writeManifest replaces the manifest atomically, the way columns.Write saves (a
// new temporary file created exclusively, fsynced, renamed into place), then
// fsyncs the folder, so the rename itself survives a power cut.
func writeManifest(dir string, m Manifest) (err error) {
	f, err := os.CreateTemp(dir, manifestName+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	if _, err := io.WriteString(f, m.String()); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), ManifestPath(dir)); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
