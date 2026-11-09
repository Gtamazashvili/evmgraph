// Package evm holds the EVM address and balance types the program shares
// between its packages.
package evm

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"

	"golang.org/x/crypto/sha3"
)

// AddressLen is the size of an EVM address in bytes.
const AddressLen = 20

// Address is a raw EVM address: the last 20 bytes of a Keccak-256 hash of a
// public key. Simulated addresses are random bytes, which look the same.
type Address [AddressLen]byte

// Hex returns the address as "0x" followed by 40 lowercase hex digits: the
// form the text storage formats write. People see Checksum instead.
func (a Address) Hex() string {
	return "0x" + hex.EncodeToString(a[:])
}

// ParseHex parses "0x" followed by 40 hex digits, in any letter case, without
// checking a checksum: the form the text storage formats read. Input typed by
// a person goes through Parse.
func ParseHex(s []byte) (Address, error) {
	var a Address
	if len(s) != 2+2*AddressLen || !bytes.HasPrefix(s, []byte("0x")) {
		return a, fmt.Errorf("bad address %q", s)
	}
	_, err := hex.Decode(a[:], s[2:])
	return a, err
}

// Checksum returns the address in EIP-55 form, as wallets and block explorers
// show it: "0x" followed by 40 hex digits whose letters are upper case where
// the Keccak-256 hash of the lowercase digits has a nibble of 8 or more, for
// example 0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed.
func (a Address) Checksum() string {
	var c Checksummer
	return string(c.Append(nil, a))
}

// A Checksummer formats many addresses in EIP-55 form, reusing one Keccak
// state and buffer, so printing a long list allocates nothing per address.
// The zero value is ready to use; it is not safe for concurrent use.
type Checksummer struct {
	h     hash.Hash
	digit [2 * AddressLen]byte
	sum   [32]byte
}

// Append appends a's EIP-55 form to dst and returns the extended slice.
func (c *Checksummer) Append(dst []byte, a Address) []byte {
	if c.h == nil {
		c.h = sha3.NewLegacyKeccak256()
	}
	hex.Encode(c.digit[:], a[:])
	c.h.Reset()
	c.h.Write(c.digit[:])
	sum := c.h.Sum(c.sum[:0])
	dst = append(dst, '0', 'x')
	for i, d := range c.digit {
		nibble := sum[i/2] >> 4
		if i%2 == 1 {
			nibble = sum[i/2] & 0x0f
		}
		if d >= 'a' && nibble >= 8 {
			d -= 'a' - 'A'
		}
		dst = append(dst, d)
	}
	return dst
}

var (
	// ErrMalformed means the input is not 40 hex digits with an optional 0x.
	ErrMalformed = errors.New("expected 0x followed by 40 hex digits")
	// ErrChecksum means the input mixes upper and lower case but the case
	// does not match its EIP-55 checksum, which usually means a typo.
	ErrChecksum = errors.New("mixed-case address does not match its EIP-55 checksum")
)

// Parse reads an address as a person types it (decision 007): 40 hex digits,
// with or without a 0x or 0X prefix. All-lowercase and all-uppercase digits
// are accepted as they are; mixed case must be the EIP-55 checksum, so a
// mistyped character in a copied address is caught instead of looked up.
func Parse(s string) (Address, error) {
	var a Address
	digits := s
	if len(digits) >= 2 && digits[0] == '0' && (digits[1] == 'x' || digits[1] == 'X') {
		digits = digits[2:]
	}
	if len(digits) != 2*AddressLen {
		return a, ErrMalformed
	}
	if _, err := hex.Decode(a[:], []byte(digits)); err != nil {
		return a, ErrMalformed
	}
	var lower, upper bool
	for i := range len(digits) {
		switch c := digits[i]; {
		case c >= 'a' && c <= 'f':
			lower = true
		case c >= 'A' && c <= 'F':
			upper = true
		}
	}
	if lower && upper && a.Checksum()[2:] != digits {
		return a, ErrChecksum
	}
	return a, nil
}
