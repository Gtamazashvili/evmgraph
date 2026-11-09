package evm

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// WeiLen is the size of a balance in bytes.
const WeiLen = 16

// Wei is a balance: an unsigned 128-bit number of wei (1 ETH = 10^18 wei),
// stored little-endian. Bytes 0–7 are the low 64 bits and bytes 8–15 the high
// 64 bits (decision 006). It is a byte array, not two uint64 fields, so a
// mapped file can be viewed as []Wei on any CPU, with no alignment or byte
// order assumptions.
type Wei [WeiLen]byte

// WeiFromParts returns the balance hi × 2^64 + lo.
func WeiFromParts(lo, hi uint64) Wei {
	var w Wei
	binary.LittleEndian.PutUint64(w[:8], lo)
	binary.LittleEndian.PutUint64(w[8:], hi)
	return w
}

// Lo returns the low 64 bits.
func (w Wei) Lo() uint64 { return binary.LittleEndian.Uint64(w[:8]) }

// Hi returns the high 64 bits.
func (w Wei) Hi() uint64 { return binary.LittleEndian.Uint64(w[8:]) }

// Big returns the balance as a big.Int, for printing and checks; the program
// never does arithmetic on balances through it.
func (w Wei) Big() *big.Int {
	b := new(big.Int).SetUint64(w.Hi())
	b.Lsh(b, 64)
	return b.Or(b, new(big.Int).SetUint64(w.Lo()))
}

// String returns the exact balance in wei, in decimal.
func (w Wei) String() string { return w.Big().String() }

// ETH returns the exact balance in ETH, with all 18 decimals:
// "1.500000000000000000".
func (w Wei) ETH() string {
	s := w.String()
	if len(s) <= 18 {
		s = strings.Repeat("0", 19-len(s)) + s
	}
	return s[:len(s)-18] + "." + s[len(s)-18:]
}

// ParseWei reads a balance written in wei, as a whole decimal number from 0
// up to 2^128 − 1, the largest balance a Wei holds.
func ParseWei(s string) (Wei, error) {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.Sign() < 0 || n.BitLen() > 128 || strings.HasPrefix(s, "+") {
		return Wei{}, fmt.Errorf("%q is not a balance in wei: expected a whole number from 0 to %s", s, maxWei)
	}
	var lo, hi big.Int
	lo.And(n, new(big.Int).SetUint64(^uint64(0)))
	hi.Rsh(n, 64)
	return WeiFromParts(lo.Uint64(), hi.Uint64()), nil
}

// maxWei is 2^128 − 1, written out for error messages.
var maxWei = WeiFromParts(^uint64(0), ^uint64(0)).String()
