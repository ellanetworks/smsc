package encryption

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

var tpdu = Location{Table: "messages", Column: "tpdu", Row: "+15550000001"}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func seq(from, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(from + i)
	}

	return b
}

func TestValueKnownAnswer(t *testing.T) {
	want := mustHex(t, "e10100000007202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f404142434445464748"+
		"494a4ba85abb823b0039abb8b4d977dd2057979d171a40083eb37c9b3f536f1cf46b025f7b8f16b324")

	got, err := seal(seq(0, 32), 7, []byte("ella-smsc known answer"), tpdu, seq(32, 32), seq(64, 12))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("seal = %x, %v, want %x", got, err, want)
	}

	k := &Keyring{active: 7, terms: map[uint32][]byte{7: seq(0, 32)}}

	plaintext, err := k.decrypt(want, tpdu)
	if err != nil || string(plaintext) != "ella-smsc known answer" {
		t.Fatalf("decrypt = %q, %v", plaintext, err)
	}
}

func TestRoundTrip(t *testing.T) {
	k := NewKeyring()

	for _, size := range []int{1, 16, 140, 4096} {
		plaintext := randomBytes(size)

		value, err := k.encrypt(plaintext, tpdu)
		if err != nil {
			t.Fatal(err)
		}

		if len(value) != Overhead+size {
			t.Fatalf("%d-byte plaintext: %d-byte value, want %d", size, len(value), Overhead+size)
		}

		if size >= 16 && bytes.Contains(value, plaintext) {
			t.Fatalf("%d-byte plaintext in the clear", size)
		}

		got, err := k.decrypt(value, tpdu)
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Fatalf("%d-byte plaintext: decrypt = %x, %v", size, got, err)
		}
	}
}

func TestEncryptIsRandomized(t *testing.T) {
	k := NewKeyring()

	a, _ := k.encrypt([]byte("same"), tpdu)
	b, _ := k.encrypt([]byte("same"), tpdu)

	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext are equal")
	}
}

func TestEmptyPlaintextIsAuthenticated(t *testing.T) {
	k := NewKeyring()

	for _, in := range [][]byte{nil, {}} {
		value, err := k.encrypt(in, tpdu)
		if err != nil || len(value) != Overhead {
			t.Fatalf("encrypt(%v) = %d bytes, %v, want %d", in, len(value), err, Overhead)
		}

		plaintext, err := k.decrypt(value, tpdu)
		if err != nil || plaintext == nil || len(plaintext) != 0 {
			t.Fatalf("decrypt = %v, %v, want an empty non-nil value", plaintext, err)
		}

		if _, err := k.decrypt(value, Location{Table: "messages", Column: "tpdu", Row: "other"}); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("an empty value moved to another row: %v", err)
		}
	}

	for _, in := range [][]byte{nil, {}} {
		if _, err := k.decrypt(in, tpdu); !errors.Is(err, ErrNotEncrypted) {
			t.Fatalf("decrypt(%v) = %v, want %v", in, err, ErrNotEncrypted)
		}
	}
}

func TestLocationBinding(t *testing.T) {
	k := NewKeyring()

	value, err := k.encrypt([]byte("secret"), tpdu)
	if err != nil {
		t.Fatal(err)
	}

	for _, loc := range []Location{
		{Table: "messages", Column: "tpdu", Row: "+15550000002"},
		{Table: "messages", Column: "tpdu", Row: ""},
		{Table: "messages", Column: "deliver_report", Row: "+15550000001"},
		{Table: "delivery_attempts", Column: "tpdu", Row: "+15550000001"},
		{Table: "messages", Column: "tpdu/+15550000001", Row: ""},
	} {
		if _, err := k.decrypt(value, loc); err == nil {
			t.Fatalf("decrypted at %+v", loc)
		}
	}
}

func TestInvalidLocation(t *testing.T) {
	k := NewKeyring()

	for _, loc := range []Location{
		{Column: "tpdu"},
		{Table: "messages"},
		{Table: "messages/x", Column: "tpdu"},
		{Table: "messages", Column: "tp/du"},
	} {
		if _, err := k.encrypt([]byte("x"), loc); !errors.Is(err, ErrInvalidLocation) {
			t.Fatalf("encrypt at %+v: %v", loc, err)
		}

		if _, err := k.decrypt(make([]byte, Overhead), loc); !errors.Is(err, ErrInvalidLocation) {
			t.Fatalf("decrypt at %+v: %v", loc, err)
		}
	}
}

func TestTampering(t *testing.T) {
	k := NewKeyring()

	value, err := k.encrypt([]byte("secret"), tpdu)
	if err != nil {
		t.Fatal(err)
	}

	for i := range value {
		tampered := bytes.Clone(value)
		tampered[i] ^= 0x01

		if _, err := k.decrypt(tampered, tpdu); err == nil {
			t.Fatalf("decrypted with byte %d flipped", i)
		}
	}

	for n := range value {
		if _, err := k.decrypt(value[:n], tpdu); err == nil {
			t.Fatalf("decrypted a %d-byte prefix", n)
		}
	}
}

func TestTermAndHeaderErrors(t *testing.T) {
	k := NewKeyring()

	value, err := k.encrypt([]byte("secret"), tpdu)
	if err != nil {
		t.Fatal(err)
	}

	if term, err := Term(value); err != nil || term != 1 {
		t.Fatalf("term = %d, %v", term, err)
	}

	cases := []struct {
		name  string
		value []byte
		want  error
	}{
		{"plaintext", []byte("a plaintext secret of some length that is long enough to pass"), ErrNotEncrypted},
		{"short", []byte{magic, version1}, ErrNotEncrypted},
		{"version", append([]byte{magic, 0x02}, value[2:]...), ErrUnsupportedVersion},
		{"truncated", value[:Overhead-1], ErrMalformed},
		{"term", append(append([]byte{magic, version1}, 0, 0, 0, 9), value[headerSize:]...), ErrUnknownTerm},
	}

	for _, c := range cases {
		if _, err := k.decrypt(c.value, tpdu); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

func TestSubmitLikePlaintextIsNotCiphertext(t *testing.T) {
	k := NewKeyring()

	submit := append([]byte{0xE1, 0x01, 0x00, 0x00, 0x00, 0x01}, make([]byte, Overhead)...)

	if _, err := Term(submit); err != nil {
		t.Fatalf("term = %v", err)
	}

	if _, err := k.decrypt(submit, tpdu); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("decrypt = %v, want %v", err, ErrAuthentication)
	}
}

func BenchmarkEncrypt(b *testing.B) {
	k := NewKeyring()
	plaintext := randomBytes(160)

	for b.Loop() {
		if _, err := k.encrypt(plaintext, tpdu); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecrypt(b *testing.B) {
	k := NewKeyring()

	value, err := k.encrypt(randomBytes(160), tpdu)
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		if _, err := k.decrypt(value, tpdu); err != nil {
			b.Fatal(err)
		}
	}
}
