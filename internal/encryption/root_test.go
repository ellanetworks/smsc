package encryption

import (
	"errors"
	"testing"
)

func testFileKey(t *testing.T) *FileKey {
	t.Helper()

	k, err := ParseFileKey([]byte(encodeStd(randomBytes(KeySize))))
	if err != nil {
		t.Fatal(err)
	}

	return k
}

func TestInitializeAndUnlock(t *testing.T) {
	ck := testFileKey(t)

	sealed, k, err := Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	if sealed.Wrap.Fingerprint != ck.Fingerprint() || sealed.Wrap.Type != TypeFile || sealed.Wrap.Mechanism != MechanismAESGCM {
		t.Fatalf("wrap = %+v", sealed.Wrap)
	}

	value, err := k.encrypt([]byte("jwt secret"), Location{Table: "jwt_secret", Column: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	unlocked, err := Unlock(ck, []RootWrap{sealed.Wrap}, sealed.Keyring)
	if err != nil {
		t.Fatal(err)
	}

	got, err := unlocked.decrypt(value, Location{Table: "jwt_secret", Column: "secret"})
	if err != nil || string(got) != "jwt secret" {
		t.Fatalf("decrypt after unlock = %q, %v", got, err)
	}
}

func TestUnlockWithAnotherKey(t *testing.T) {
	sealed, _, err := Initialize(testFileKey(t))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Unlock(testFileKey(t), []RootWrap{sealed.Wrap}, sealed.Keyring); !errors.Is(err, ErrNoMatchingWrap) {
		t.Fatalf("unlock with another key: %v", err)
	}
}

func TestUnlockRejectsTampering(t *testing.T) {
	ck := testFileKey(t)

	sealed, _, err := Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	wrap := sealed.Wrap
	wrap.Wrapped = append([]byte(nil), wrap.Wrapped...)
	wrap.Wrapped[len(wrap.Wrapped)-1] ^= 0x01

	if _, err := Unlock(ck, []RootWrap{wrap}, sealed.Keyring); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("unlock with a tampered wrap: %v", err)
	}

	mismatched := sealed.Wrap
	mismatched.Mechanism = "rsa-oaep"

	if _, err := Unlock(ck, []RootWrap{mismatched}, sealed.Keyring); !errors.Is(err, ErrNoMatchingWrap) {
		t.Fatalf("unlock with another mechanism: %v", err)
	}

	other, _, err := Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Unlock(ck, []RootWrap{sealed.Wrap}, other.Keyring); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("unlock another cluster's keyring: %v", err)
	}
}

func TestRotateRoot(t *testing.T) {
	ck := testFileKey(t)
	loc := Location{Table: "oidc", Column: "client_secret"}

	first, k, err := Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	old, err := k.encrypt([]byte("client secret"), loc)
	if err != nil {
		t.Fatal(err)
	}

	var keys Keys

	keys.Install(k)

	second, next, err := keys.Rotate(ck)
	if err != nil {
		t.Fatal(err)
	}

	if next.ActiveTerm() != 2 {
		t.Fatalf("active term %d", next.ActiveTerm())
	}

	unlocked, err := Unlock(ck, []RootWrap{second.Wrap}, second.Keyring)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := unlocked.decrypt(old, loc); err != nil || string(got) != "client secret" {
		t.Fatalf("old value after rotation = %q, %v", got, err)
	}

	if _, err := Unlock(ck, []RootWrap{first.Wrap}, second.Keyring); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("the old root key opens the new keyring: %v", err)
	}
}

func TestRewrap(t *testing.T) {
	previous, current := testFileKey(t), testFileKey(t)

	sealed, _, err := Initialize(previous)
	if err != nil {
		t.Fatal(err)
	}

	wrap, err := Rewrap(current, []RootWrap{sealed.Wrap}, sealed.Keyring, previous)
	if err != nil {
		t.Fatal(err)
	}

	if wrap.Fingerprint != current.Fingerprint() {
		t.Fatalf("wrap fingerprint %s, want %s", wrap.Fingerprint, current.Fingerprint())
	}

	wraps := []RootWrap{sealed.Wrap, wrap}

	for _, ck := range []*FileKey{previous, current} {
		if _, err := Unlock(ck, wraps, sealed.Keyring); err != nil {
			t.Fatalf("unlock with %s: %v", ck.Fingerprint(), err)
		}
	}

	if _, err := Rewrap(current, []RootWrap{sealed.Wrap}, sealed.Keyring, testFileKey(t)); !errors.Is(err, ErrNoMatchingWrap) {
		t.Fatalf("rewrap from an unknown key: %v", err)
	}

	stale, _, err := Initialize(previous)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Rewrap(current, []RootWrap{stale.Wrap}, sealed.Keyring, previous); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("rewrap a stale root key: %v", err)
	}
}

func TestUnlockSkipsOtherWraps(t *testing.T) {
	ck := testFileKey(t)

	sealed, _, err := Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	other, _, err := Initialize(testFileKey(t))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Unlock(ck, []RootWrap{other.Wrap, sealed.Wrap}, sealed.Keyring); err != nil {
		t.Fatalf("unlock with the matching wrap second: %v", err)
	}
}

type shortKey struct{ *FileKey }

func (shortKey) Unwrap([]byte) ([]byte, error) {
	return make([]byte, 16), nil
}

func TestUnwrapChecksRootKeySize(t *testing.T) {
	ck := shortKey{testFileKey(t)}

	sealed, _, err := Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Unlock(ck, []RootWrap{sealed.Wrap}, sealed.Keyring); !errors.Is(err, ErrInvalidRootKey) {
		t.Fatalf("unlock with a 16-byte root key: %v", err)
	}
}
