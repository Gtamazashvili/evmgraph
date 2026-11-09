package evm

import (
	"math"
	"testing"
)

// The boundaries of the 128-bit range, and the 64-bit edge a uint64 cannot
// cross, print exactly.
func TestWeiBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		w      Wei
		wei    string
		eth    string
		lo, hi uint64
	}{
		{"zero", WeiFromParts(0, 0), "0", "0.000000000000000000", 0, 0},
		{"one wei", WeiFromParts(1, 0), "1", "0.000000000000000001", 1, 0},
		{"one ETH", WeiFromParts(1_000_000_000_000_000_000, 0), "1000000000000000000", "1.000000000000000000", 1e18, 0},
		{"largest uint64 (about 18.4 ETH)", WeiFromParts(math.MaxUint64, 0), "18446744073709551615", "18.446744073709551615", math.MaxUint64, 0},
		{"2^64", WeiFromParts(0, 1), "18446744073709551616", "18.446744073709551616", 0, 1},
		{"2^128 - 1", WeiFromParts(math.MaxUint64, math.MaxUint64),
			"340282366920938463463374607431768211455", "340282366920938463463.374607431768211455", math.MaxUint64, math.MaxUint64},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.w.String(); got != c.wei {
				t.Errorf("String() = %s, want %s", got, c.wei)
			}
			if got := c.w.ETH(); got != c.eth {
				t.Errorf("ETH() = %s, want %s", got, c.eth)
			}
			if c.w.Lo() != c.lo || c.w.Hi() != c.hi {
				t.Errorf("Lo, Hi = %d, %d, want %d, %d", c.w.Lo(), c.w.Hi(), c.lo, c.hi)
			}
		})
	}
}

// The byte layout is fixed: little-endian, low half first. Files depend on it.
func TestWeiLayout(t *testing.T) {
	w := WeiFromParts(0x0807060504030201, 0x100f0e0d0c0b0a09)
	for i, b := range w {
		if b != byte(i+1) {
			t.Fatalf("byte %d is %#x, want %#x: %x", i, b, i+1, w)
		}
	}
}

// ParseWei reads every whole number of wei a Wei holds, and nothing else.
func TestParseWei(t *testing.T) {
	for s, want := range map[string]Wei{
		"0":                    {},
		"5":                    WeiFromParts(5, 0),
		"18446744073709551616": WeiFromParts(0, 1),
		"340282366920938463463374607431768211455": WeiFromParts(^uint64(0), ^uint64(0)),
	} {
		got, err := ParseWei(s)
		if err != nil || got != want {
			t.Errorf("ParseWei(%q) = %s, %v; want %s", s, got, err, want)
		}
	}
	for _, s := range []string{"", "-1", "+5", "1.5", "0x10", "1e18", "340282366920938463463374607431768211456", " 5"} {
		if _, err := ParseWei(s); err == nil {
			t.Errorf("ParseWei(%q) accepted it", s)
		}
	}
}
