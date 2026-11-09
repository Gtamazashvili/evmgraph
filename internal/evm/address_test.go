package evm

import (
	"errors"
	"strings"
	"testing"
)

func TestHexRoundTrip(t *testing.T) {
	const s = "0x2efaa0f697888f79f5f93cef3a5890f377324e0c"
	a, err := ParseHex([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hex() != s {
		t.Fatalf("got %s, want %s", a.Hex(), s)
	}
}

func TestParseHexRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"2efaa0f697888f79f5f93cef3a5890f377324e0c",     // no 0x
		"0x2efaa0f697888f79f5f93cef3a5890f377324e0",    // 39 digits
		"0x2efaa0f697888f79f5f93cef3a5890f377324e0c00", // 42 digits
		"0xzzfaa0f697888f79f5f93cef3a5890f377324e0c",   // not hex
	} {
		if _, err := ParseHex([]byte(s)); err == nil {
			t.Errorf("ParseHex(%q) accepted bad input", s)
		}
	}
}

// eip55 holds the test vectors published with EIP-55: each is its own
// checksummed form, including two that happen to be all upper case and two
// all lower case.
var eip55 = []string{
	"0x52908400098527886E0F7030069857D2E4169EE7",
	"0x8617E340B3D01FA5F11F306F4090FD50E238070D",
	"0xde709f2102306220921060314715629080e2fb77",
	"0x27b1fdb04752bbc536007a920d24acb045561c26",
	"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
	"0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
	"0xdbF03B407c01E7cD3CBea99509d93f8DDDC8C6FB",
	"0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb",
}

func TestChecksumMatchesEIP55Vectors(t *testing.T) {
	for _, v := range eip55 {
		a, err := ParseHex([]byte("0x" + strings.ToLower(v[2:])))
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Checksum(); got != v {
			t.Errorf("Checksum = %s, want %s", got, v)
		}
	}
}

func TestChecksummerReuse(t *testing.T) {
	var c Checksummer
	var out []byte
	for _, v := range eip55 {
		a, _ := Parse(v)
		out = c.Append(out, a)
		out = append(out, '\n')
	}
	if want := strings.Join(eip55, "\n") + "\n"; string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

func TestParseAccepts(t *testing.T) {
	const checksummed = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"
	want, _ := ParseHex([]byte(strings.ToLower(checksummed)))
	for _, s := range []string{
		checksummed,                             // EIP-55
		strings.ToLower(checksummed),            // all lower case
		"0x" + strings.ToUpper(checksummed[2:]), // all upper case
		checksummed[2:],                         // no prefix
		"0X" + checksummed[2:],                  // upper-case prefix
	} {
		a, err := Parse(s)
		if err != nil {
			t.Errorf("Parse(%q): %v", s, err)
		} else if a != want {
			t.Errorf("Parse(%q) = %s, want %s", s, a.Hex(), want.Hex())
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, c := range []struct {
		in   string
		want error
	}{
		{"", ErrMalformed},
		{"0x", ErrMalformed},
		{"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAe", ErrMalformed},    // 39 digits
		{"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed00", ErrMalformed}, // 42 digits
		{"0xzaAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", ErrMalformed},   // not hex
		{"0x 5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAe", ErrMalformed},   // a space
		{"0x5AAeb6053F3E94C9b9A09f33669435E7Ef1BeAed", ErrChecksum},    // one letter's case changed
		{"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAee", ErrChecksum},    // last digit mistyped
	} {
		if _, err := Parse(c.in); !errors.Is(err, c.want) {
			t.Errorf("Parse(%q) = %v, want %v", c.in, err, c.want)
		}
	}
}

func BenchmarkChecksummer(b *testing.B) {
	var c Checksummer
	a, _ := Parse(eip55[4])
	buf := make([]byte, 0, 64)
	for b.Loop() {
		buf = c.Append(buf[:0], a)
	}
}
