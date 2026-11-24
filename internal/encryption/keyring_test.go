package encryption

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"testing"
)

func TestSealedKeyringKnownAnswer(t *testing.T) {
	sealed := mustHex(t, "010d0e0f101112131415161718f199a10b43c4df7e86274ac890b5bb4043d5c09e9cdf2eca59ce45107f0298cd6c5553d206"+
		"e92a680ebba3918903f5960503d3d6690c301188fa2d74e7f27180e37c324994f3771034216f9309ca847e96ef426437e6e2"+
		"832a182e190ebc7ec91c")

	k, err := openKeyring(seq(200, 32), sealed, seq(100, 16))
	if err != nil {
		t.Fatal(err)
	}

	if k.ActiveTerm() != 2 || !slices.Equal(k.Terms(), []uint32{1, 2}) {
		t.Fatalf("active %d, terms %v", k.ActiveTerm(), k.Terms())
	}

	if !bytes.Equal(k.terms[1], bytes.Repeat([]byte{0x11}, 32)) || !bytes.Equal(k.terms[2], seq(0, 32)) {
		t.Fatalf("terms = %x", k.terms)
	}
}

func TestRotate(t *testing.T) {
	k := NewKeyring()

	old, err := k.encrypt([]byte("before"), tpdu)
	if err != nil {
		t.Fatal(err)
	}

	next, _, err := k.rotate()
	if err != nil {
		t.Fatal(err)
	}

	if next.ActiveTerm() != 2 || !slices.Equal(next.Terms(), []uint32{1, 2}) {
		t.Fatalf("active %d, terms %v", next.ActiveTerm(), next.Terms())
	}

	if bytes.Equal(next.terms[1], next.terms[2]) {
		t.Fatal("the new term repeats the old data key")
	}

	k.Zeroize()

	if got, err := next.decrypt(old, tpdu); err != nil || string(got) != "before" {
		t.Fatalf("old value after rotation = %q, %v", got, err)
	}

	fresh, err := next.encrypt([]byte("after"), tpdu)
	if err != nil {
		t.Fatal(err)
	}

	if term, _ := Term(fresh); term != 2 {
		t.Fatalf("new value under term %d", term)
	}

	if _, _, err := (&Keyring{active: math.MaxUint32, terms: map[uint32][]byte{math.MaxUint32: seq(0, 32)}}).rotate(); !errors.Is(err, ErrInvalidKeyring) {
		t.Fatalf("rotate past the last term: %v", err)
	}
}

func TestSealKeyring(t *testing.T) {
	k, _, err := NewKeyring().rotate()
	if err != nil {
		t.Fatal(err)
	}

	root := randomBytes(KeySize)

	sealed, err := sealKeyring(root, k)
	if err != nil {
		t.Fatal(err)
	}

	opened, err := openKeyring(root, sealed, k.id)
	if err != nil {
		t.Fatal(err)
	}

	if opened.ActiveTerm() != k.ActiveTerm() || !slices.Equal(opened.Terms(), k.Terms()) {
		t.Fatalf("opened %d %v, want %d %v", opened.ActiveTerm(), opened.Terms(), k.ActiveTerm(), k.Terms())
	}

	for term, dek := range k.terms {
		if !bytes.Equal(opened.terms[term], dek) {
			t.Fatalf("term %d differs", term)
		}
	}

	if _, err := openKeyring(randomBytes(KeySize), sealed, k.id); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("open with another root key: %v", err)
	}

	for i := range sealed {
		tampered := bytes.Clone(sealed)
		tampered[i] ^= 0x01

		if _, err := openKeyring(root, tampered, k.id); err == nil {
			t.Fatalf("opened with byte %d flipped", i)
		}
	}
}

func TestUnmarshalKeyring(t *testing.T) {
	entry := func(term uint32) []byte {
		return append(binary.BigEndian.AppendUint32(nil, term), seq(0, KeySize)...)
	}

	ring := func(active, count uint32, entries ...[]byte) []byte {
		b := []byte{keyringVersion}
		b = binary.BigEndian.AppendUint32(b, active)
		b = binary.BigEndian.AppendUint32(b, count)

		return append(b, slices.Concat(entries...)...)
	}

	if _, err := unmarshalKeyring(ring(2, 2, entry(1), entry(2))); err != nil {
		t.Fatalf("valid keyring: %v", err)
	}

	for name, b := range map[string][]byte{
		"empty":          nil,
		"version":        append([]byte{0x02}, ring(1, 1, entry(1))[1:]...),
		"no terms":       ring(0, 0),
		"count too high": ring(1, 2, entry(1)),
		"count too low":  ring(2, 1, entry(1), entry(2)),
		"trailing byte":  append(ring(1, 1, entry(1)), 0),
		"term zero":      ring(0, 1, entry(0)),
		"repeated term":  ring(1, 2, entry(1), entry(1)),
		"stale active":   ring(1, 2, entry(1), entry(2)),
		"missing active": ring(3, 2, entry(1), entry(2)),
		"huge count":     ring(1, math.MaxUint32, entry(1)),
	} {
		if _, err := unmarshalKeyring(b); !errors.Is(err, ErrInvalidKeyring) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestZeroize(t *testing.T) {
	k := NewKeyring()
	dek := k.terms[1]

	k.Zeroize()

	if !bytes.Equal(dek, make([]byte, KeySize)) {
		t.Fatal("data key not zeroized")
	}

	if _, err := k.encrypt([]byte("x"), tpdu); !errors.Is(err, ErrLocked) {
		t.Fatalf("encrypt with a zeroized keyring: %v", err)
	}

	if _, _, err := k.rotate(); !errors.Is(err, ErrLocked) {
		t.Fatalf("rotate a zeroized keyring: %v", err)
	}

	if _, err := sealKeyring(randomBytes(KeySize), k); !errors.Is(err, ErrLocked) {
		t.Fatalf("seal a zeroized keyring: %v", err)
	}
}
