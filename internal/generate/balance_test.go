package generate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"evmgraph/internal/evm"
)

// The decades cover exactly one billion addresses, in increasing order.
func TestBalanceDecadesCoverABillion(t *testing.T) {
	var sum uint64
	last := -2
	for _, d := range balanceDecades {
		sum += d.perBillion
		if d.exp <= last {
			t.Fatalf("decade 10^%d is out of order", d.exp)
		}
		last = d.exp
	}
	if sum != 1_000_000_000 {
		t.Fatalf("the decades cover %d per billion, want 1,000,000,000", sum)
	}
}

// Every draw lands inside its decade, including the extreme words.
func TestBalanceFromStaysInsideItsDecade(t *testing.T) {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(25), nil) // 10 million ETH
	for _, pick := range []uint64{0, 1 << 63, ^uint64(0) - 1, ^uint64(0)} {
		for _, amount := range []uint64{0, 1, 1 << 63, ^uint64(0)} {
			b := balanceFrom(pick, amount).Big()
			if b.Sign() < 0 || b.Cmp(limit) >= 0 {
				t.Fatalf("balanceFrom(%d, %d) = %s wei, outside [0, 10^25)", pick, amount, b)
			}
		}
	}
	// The last word lands in the top decade: at least 1 million ETH.
	if b := balanceFrom(^uint64(0), 0).Big(); b.Cmp(new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)) < 0 {
		t.Fatalf("the top pick gave %s wei, below the whale decade", b)
	}
	if b := balanceFrom(0, ^uint64(0)); b != (evm.Wei{}) {
		t.Fatalf("the lowest pick gave %s wei, want a zero balance", b)
	}
}

// A large sample has the model's shape: about 40% hold nothing, about 0.67%
// hold 1 ETH or more, and the mean is about 0.54 ETH (it varies with the few
// whales, so the bound is loose).
func TestBalanceModelShape(t *testing.T) {
	const n = 2_000_000
	_, bals, err := datasetOf(context.Background(), 1, 0, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	oneETH := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	zero, rich := 0, 0
	total := new(big.Int)
	for _, b := range bals {
		v := b.Big()
		switch {
		case v.Sign() == 0:
			zero++
		case v.Cmp(oneETH) >= 0:
			rich++
		}
		total.Add(total, v)
	}
	if share := float64(zero) / n; share < 0.395 || share > 0.405 {
		t.Errorf("%.2f%% hold nothing, want about 40%%", 100*share)
	}
	if share := float64(rich) / n; share < 0.006 || share > 0.0074 {
		t.Errorf("%.3f%% hold 1 ETH or more, want about 0.67%%", 100*share)
	}
	mean, _ := new(big.Rat).SetFrac(total, new(big.Int).Mul(oneETH, big.NewInt(n))).Float64()
	if mean < 0.1 || mean > 3 {
		t.Errorf("mean %.3f ETH per address, want about 0.54", mean)
	}
	t.Logf("%.2f%% zero, %.3f%% at 1 ETH or more, mean %.3f ETH", 100*float64(zero)/n, 100*float64(rich)/n, mean)
}

// goldenBalances is the SHA-256 of the balances of seed 1, positions 0 to
// 100,000. A change to the model, the stream keys or the byte order changes
// it; update it only on purpose, with a decision record.
const goldenBalances = "58bfa3085a80261a1697097831ab51e50edc3e75a7a64f29a76f64b7406dbb17"

func balancesHash(t *testing.T) string {
	t.Helper()
	_, bals, err := datasetOf(context.Background(), 1, 0, 100_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, b := range bals {
		h.Write(b[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestBalancesGolden(t *testing.T) {
	if got := balancesHash(t); got != goldenBalances {
		t.Fatalf("balances hash %s, want %s", got, goldenBalances)
	}
}

// The same balances on amd64, through Rosetta on an Apple silicon Mac (see
// TestEdgesGoldenOnAMD64).
func TestBalancesGoldenOnAMD64(t *testing.T) {
	if runtime.GOARCH == "amd64" || os.Getenv("EVMGRAPH_IN_AMD64") != "" {
		t.Skip("already amd64")
	}
	if runtime.GOOS != "darwin" || testing.Short() {
		t.Skip("needs Rosetta (macOS on Apple silicon) and a full run")
	}
	cmd := exec.Command("go", "test", "-count=1", "-run", "^TestBalancesGolden$", ".")
	cmd.Env = append(os.Environ(), "GOARCH=amd64", "EVMGRAPH_IN_AMD64=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "bad CPU type") {
			t.Skip("Rosetta is not installed")
		}
		t.Fatalf("amd64 run: %v\n%s", err, out)
	}
}
