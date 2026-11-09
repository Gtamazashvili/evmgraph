//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Only one program writes a cache folder at a time: `evmgraph run` while it
// opens and cleans the cache, generates and commits a dataset or folds
// updates in (only Prepare, which creates the folder the lock lives in, comes
// before it), and `evmgraph update` while it appends one. Without it, one run's Clean could
// remove another's draft folder, and an update appended during a fold could
// be lost. The queries only read and never take it.
//
// The lock is an advisory flock(2) on the file "lock" in the cache folder.
// The kernel releases it when the holder exits, however it exits, so a crash
// never leaves the folder locked.

const lockName = "lock"

// ErrLocked means another program holds the cache's write lock.
var ErrLocked = errors.New("another evmgraph is writing this cache folder")

// Lock is a held write lock.
type Lock struct{ f *os.File }

// TryLock takes the write lock, or returns ErrLocked at once if another
// program holds it. The cache folder must exist (Prepare).
func (c Cache) TryLock() (*Lock, error) { return lock(c.Dir, syscall.LOCK_NB) }

// WaitLock takes the write lock, waiting for its holder to release it.
func WaitLock(dir string) (*Lock, error) { return lock(dir, 0) }

func lock(dir string, how int) (*Lock, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return nil, ErrLocked
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", dir, err)
	}
	return &Lock{f: f}, nil
}

// Unlock releases the lock; it is safe to call more than once.
func (l *Lock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Close() // closing the file releases the flock
	l.f = nil
}
