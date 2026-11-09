package columns

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// SaveUint32s writes the same bytes, with the package's safe save.
func TestSaveUint32s(t *testing.T) {
	path := filepath.Join(t.TempDir(), OffsetsFileName)
	values := []uint32{0, 3, 3, 9, 4294967295}
	if err := SaveUint32s(context.Background(), path, nil, values); err != nil {
		t.Fatal(err)
	}
	noTempFiles(t, path)
	r, err := OpenUint32s(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !slices.Equal(r.Values, values) {
		t.Fatalf("got %v", r.Values)
	}
}

// Empty files work; a size that is not whole uint32s is invalid; a missing
// file is fs.ErrNotExist.
func TestUint32sEdgeCases(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := SaveUint32s(context.Background(), empty, nil, nil); err != nil {
		t.Fatal(err)
	}
	if r, err := OpenUint32s(empty); err != nil || len(r.Values) != 0 {
		t.Fatalf("empty file: %v", err)
	}
	odd := filepath.Join(dir, "odd")
	if err := os.WriteFile(odd, make([]byte, 6), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenUint32s(odd); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("6 bytes: %v", err)
	}
	if _, err := OpenUint32s(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}
