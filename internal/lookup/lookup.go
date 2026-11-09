// Package lookup finds an address's row in a dataset sorted by address
// (decision 010), with interpolation search: addresses are uniformly random,
// so the first 8 bytes of the address sought predict where it sits. A guess
// lands within a few rows on average, so a lookup reads about 5 addresses and
// touches about 3 pages of a mapped file, with no index file at all.
package lookup

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/bits"

	"evmgraph/internal/evm"
)

// maxGuesses is how many interpolation guesses are made before falling back
// to binary search, so skewed or clustered data still costs O(log n) reads.
const maxGuesses = 8

// Find returns the row of a in addrs, which must be sorted ascending, and
// whether it is there. With equal addresses it returns the first. It works on
// any []evm.Address: a mapped file or decoded addresses alike.
func Find(addrs []evm.Address, a evm.Address) (row int, ok bool) {
	row, ok, _ = find(addrs, a)
	return row, ok
}

// find is Find that also counts the addresses it read.
func find(addrs []evm.Address, a evm.Address) (row int, ok bool, probes int) {
	key := prefix(&a)
	// The search range is rows lo..hi. vlo and vhi bound the prefixes in it:
	// the prefixes of the rows just outside it, or 0 and the largest value
	// before anything has been read. They are values already read, so a
	// guess costs no extra reads.
	lo, hi := 0, len(addrs)-1
	vlo, vhi := uint64(0), uint64(math.MaxUint64)
	for guesses := 0; lo <= hi; guesses++ {
		mid := lo + (hi-lo)/2 // binary search
		if guesses < maxGuesses && vhi > vlo {
			mid = guess(key, vlo, vhi, lo, hi)
		}
		probes++
		switch c := bytes.Compare(addrs[mid][:], a[:]); {
		case c == 0:
			for mid > 0 && addrs[mid-1] == a {
				mid--
				probes++
			}
			return mid, true, probes
		case c < 0:
			lo, vlo = mid+1, prefix(&addrs[mid])
		default:
			hi, vhi = mid-1, prefix(&addrs[mid])
		}
	}
	return lo, false, probes
}

// guess interpolates key's row between lo and hi, given that the prefixes of
// rows lo..hi lie between vlo and vhi: lo + (key−vlo)·(hi−lo) / (vhi−vlo),
// computed in 128 bits so it cannot overflow, and clamped to lo..hi.
func guess(key, vlo, vhi uint64, lo, hi int) int {
	switch {
	case key <= vlo:
		return lo
	case key >= vhi:
		return hi
	}
	// (key−vlo) < (vhi−vlo), so the 128-bit product divided by (vhi−vlo) is
	// below hi−lo and its high word is below the divisor, as Div64 requires.
	// The divisor is vhi−vlo, not vhi−vlo+1, which wraps to 0 for the full
	// range on the first guess.
	h, l := bits.Mul64(key-vlo, uint64(hi-lo))
	q, _ := bits.Div64(h, l, vhi-vlo)
	return lo + int(q)
}

// prefix is the first 8 bytes of a, big-endian, so it orders like a.
func prefix(a *evm.Address) uint64 { return binary.BigEndian.Uint64(a[:8]) }
