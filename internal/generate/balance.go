package generate

import (
	"encoding/binary"
	"math/bits"

	"evmgraph/internal/evm"
)

// The balance model (decision 006): a simulated address holds what real ones
// tend to hold. Many hold nothing, most of the rest hold dust or a little
// ETH, and a thin tail holds a lot, down to a few whales. The table gives,
// per billion addresses, how many hold nothing and how many hold an amount in
// each decade of ETH; inside a decade the amount is uniform, in whole wei.
//
// The shares follow the shape of public rich lists and are illustrative, not
// measured: about 40% hold nothing, about 0.67% hold 1 ETH or more, and
// about 1 in 5 million (210 per billion) holds 100,000 ETH or more. The mean
// is about 0.54 ETH per address, so a dataset the size of mainnet (a few
// hundred million addresses, approximate) would hold roughly the real supply
// (about 120 million ETH, approximate). The largest possible balance is just under 10 million ETH,
// far beyond 64 bits of wei (18.4 ETH), so balances stay u128.
//
// Only integer arithmetic is used, for the reason edges.go gives: the same
// seed must give the same balances on every CPU.
var balanceDecades = []struct {
	perBillion uint64 // addresses per billion in this decade
	exp        int    // the decade is [10^exp, 10^(exp+1)) wei; -1 means a zero balance
}{
	{400_000_000, -1}, // nothing
	{48_301_790, 9},   // 1 to 10 gwei
	{50_000_000, 10},
	{70_000_000, 11},
	{90_000_000, 12},
	{100_000_000, 13},
	{100_000_000, 14}, // 0.0001 to 0.001 ETH
	{80_000_000, 15},
	{40_000_000, 16},
	{15_000_000, 17}, // 0.1 to 1 ETH
	{5_600_000, 18},  // 1 to 10 ETH
	{950_000, 19},
	{128_000, 20},
	{18_000, 21}, // 1,000 to 10,000 ETH
	{2_000, 22},
	{200, 23},
	{10, 24}, // 1 million to 10 million ETH
}

// pow10 holds 10^0 to 10^19, every power of ten that fits in a uint64.
var pow10 = func() (p [20]uint64) {
	p[0] = 1
	for i := 1; i < len(p); i++ {
		p[i] = p[i-1] * 10
	}
	return p
}()

// balanceFromBytes draws one balance from 16 random bytes: the first 8 pick
// the decade, the last 8 the amount inside it. A position always takes 16
// bytes of its stream, so a range can start inside a block (fill's skip).
func balanceFromBytes(b []byte) evm.Wei {
	return balanceFrom(binary.LittleEndian.Uint64(b[:8]), binary.LittleEndian.Uint64(b[8:16]))
}

// balanceFrom maps two uniform 64-bit words to a balance of the model.
func balanceFrom(pick, amount uint64) evm.Wei {
	r := below(pick, 1_000_000_000)
	exp := -1
	for _, d := range balanceDecades {
		if r < d.perBillion {
			exp = d.exp
			break
		}
		r -= d.perBillion
	}
	switch {
	case exp < 0:
		return evm.Wei{}
	case exp <= 15:
		// [10^exp, 10^(exp+1)) in whole wei; 9·10^15 still fits a uint64.
		return evm.WeiFromParts(pow10[exp]+below(amount, 9*pow10[exp]), 0)
	default:
		// 16 significant digits, then scaled: m · 10^(exp-15), where
		// m < 10^16 and the scale is at most 10^9, so the product is below
		// 10^25, well inside 128 bits.
		m := pow10[15] + below(amount, 9*pow10[15])
		hi, lo := bits.Mul64(m, pow10[exp-15])
		return evm.WeiFromParts(lo, hi)
	}
}

// below maps a uniform 64-bit word to a uniform value in [0, n): the high
// word of x·n, Lemire's method, without a division.
func below(x, n uint64) uint64 {
	hi, _ := bits.Mul64(x, n)
	return hi
}
