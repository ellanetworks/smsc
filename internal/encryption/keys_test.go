package encryption

import (
	"bytes"
	"errors"
	"testing"
)

func TestKeysLocked(t *testing.T) {
	var keys Keys

	if keys.Unlocked() {
		t.Fatal("a new holder is unlocked")
	}

	if _, err := keys.Encrypt([]byte("x"), tpdu); !errors.Is(err, ErrLocked) {
		t.Fatalf("encrypt while locked: %v", err)
	}

	if _, err := keys.Decrypt(make([]byte, Overhead), tpdu); !errors.Is(err, ErrLocked) {
		t.Fatalf("decrypt while locked: %v", err)
	}

	if _, err := keys.ActiveTerm(); !errors.Is(err, ErrLocked) {
		t.Fatalf("active term while locked: %v", err)
	}

	if _, _, err := keys.Rotate(testFileKey(t)); !errors.Is(err, ErrLocked) {
		t.Fatalf("rotate while locked: %v", err)
	}
}

func TestKeysInstallAndLock(t *testing.T) {
	var keys Keys

	first := NewKeyring()
	dek := first.terms[1]

	keys.Install(first)

	value, err := keys.Encrypt([]byte("secret"), tpdu)
	if err != nil {
		t.Fatal(err)
	}

	next, err := first.rotate()
	if err != nil {
		t.Fatal(err)
	}

	keys.Install(next)

	if !bytes.Equal(dek, make([]byte, KeySize)) {
		t.Fatal("the replaced keyring was not zeroized")
	}

	if got, err := keys.Decrypt(value, tpdu); err != nil || string(got) != "secret" {
		t.Fatalf("decrypt after install = %q, %v", got, err)
	}

	if term, err := keys.ActiveTerm(); err != nil || term != 2 {
		t.Fatalf("active term = %d, %v", term, err)
	}

	keys.Install(next)

	if _, err := keys.Decrypt(value, tpdu); err != nil {
		t.Fatalf("reinstalling the same keyring zeroized it: %v", err)
	}

	keys.Lock()

	if keys.Unlocked() {
		t.Fatal("unlocked after lock")
	}

	if _, err := keys.Decrypt(value, tpdu); !errors.Is(err, ErrLocked) {
		t.Fatalf("decrypt after lock: %v", err)
	}

	keys.Lock()
}

func TestLockDuringUse(t *testing.T) {
	var keys Keys

	k := NewKeyring()
	reference := &Keyring{active: 1, terms: map[uint32][]byte{1: bytes.Clone(k.terms[1])}}

	keys.Install(k)

	values := make(chan []byte, 1024)

	go func() {
		defer close(values)

		for {
			value, err := keys.Encrypt([]byte("secret"), tpdu)
			if errors.Is(err, ErrLocked) {
				return
			}

			if err != nil {
				t.Error(err)

				return
			}

			values <- value
		}
	}()

	<-values
	keys.Lock()

	for value := range values {
		if got, err := reference.decrypt(value, tpdu); err != nil || string(got) != "secret" {
			t.Fatalf("a value encrypted during lock = %q, %v", got, err)
		}
	}
}

func TestFileKeyAfterZeroize(t *testing.T) {
	ck := testFileKey(t)

	wrapped, err := ck.Wrap(randomBytes(KeySize))
	if err != nil {
		t.Fatal(err)
	}

	ck.Zeroize()

	if _, err := ck.Wrap(randomBytes(KeySize)); !errors.Is(err, ErrZeroized) {
		t.Fatalf("wrap after zeroize: %v", err)
	}

	if _, err := ck.Unwrap(wrapped); !errors.Is(err, ErrZeroized) {
		t.Fatalf("unwrap after zeroize: %v", err)
	}
}
