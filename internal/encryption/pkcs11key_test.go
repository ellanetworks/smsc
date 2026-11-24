package encryption_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ellanetworks/smsc/internal/encryption"
	"github.com/ellanetworks/smsc/internal/encryption/softhsmtest"
)

func TestMain(m *testing.M) {
	code := m.Run()

	softhsmtest.Remove()
	os.Exit(code)
}

func hsmKey(t *testing.T, mechanism, label string) encryption.PKCS11Config {
	t.Helper()

	tok := softhsmtest.Open(t)

	if mechanism == encryption.MechanismRSAOAEP {
		tok.RSA(t, label, encryption.MinRSAKeyBits)
	} else {
		tok.AES(t, label, encryption.KeySize)
	}

	return encryption.PKCS11Config{
		Library: tok.Library, TokenLabel: tok.Label, PINFile: tok.PINFile, KeyLabel: label, Mechanism: mechanism,
	}
}

func loadHSM(t *testing.T, cfg encryption.PKCS11Config) *encryption.PKCS11Key {
	t.Helper()

	k, err := encryption.LoadPKCS11Key(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(k.Zeroize)

	return k
}

func roundTrip(t *testing.T, k *encryption.Keys) []byte {
	t.Helper()

	value, err := k.Encrypt([]byte("jwt secret"), encryption.Location{Table: "secrets", Column: "jwt"})
	if err != nil {
		t.Fatal(err)
	}

	return value
}

func TestPKCS11KeyWrapsTheRootKey(t *testing.T) {
	for _, mechanism := range []string{encryption.MechanismAESGCM, encryption.MechanismRSAOAEP} {
		t.Run(mechanism, func(t *testing.T) {
			cfg := hsmKey(t, mechanism, "wrap-"+mechanism)
			ck := loadHSM(t, cfg)

			if ck.Type() != encryption.TypePKCS11 || ck.Mechanism() != mechanism ||
				!strings.HasPrefix(ck.Fingerprint(), cfg.KeyLabel+"/") {
				t.Fatalf("key = %s %s %s", ck.Type(), ck.Mechanism(), ck.Fingerprint())
			}

			sealed, keyring, err := encryption.Initialize(ck)
			if err != nil {
				t.Fatal(err)
			}

			var before encryption.Keys

			before.Install(keyring)
			value := roundTrip(t, &before)

			again := loadHSM(t, cfg)
			if again.Fingerprint() != ck.Fingerprint() {
				t.Fatalf("fingerprint %s, then %s", ck.Fingerprint(), again.Fingerprint())
			}

			unlocked, err := encryption.Unlock(again, []encryption.RootWrap{sealed.Wrap}, sealed.Keyring)
			if err != nil {
				t.Fatal(err)
			}

			var after encryption.Keys

			after.Install(unlocked)

			got, err := after.Decrypt(value, encryption.Location{Table: "secrets", Column: "jwt"})
			if err != nil || string(got) != "jwt secret" {
				t.Fatalf("decrypt after unlock = %q, %v", got, err)
			}
		})
	}
}

func TestPKCS11KeyRefusesAnotherKeysWrap(t *testing.T) {
	for _, mechanism := range []string{encryption.MechanismAESGCM, encryption.MechanismRSAOAEP} {
		t.Run(mechanism, func(t *testing.T) {
			ck := loadHSM(t, hsmKey(t, mechanism, "mine-"+mechanism))
			other := loadHSM(t, hsmKey(t, mechanism, "other-"+mechanism))

			sealed, _, err := encryption.Initialize(ck)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := encryption.Unlock(other, []encryption.RootWrap{sealed.Wrap}, sealed.Keyring); !errors.Is(err,
				encryption.ErrNoMatchingWrap) {
				t.Fatalf("unlock with another key: %v", err)
			}

			forged := sealed.Wrap
			forged.Fingerprint = other.Fingerprint()

			if _, err := encryption.Unlock(other, []encryption.RootWrap{forged}, sealed.Keyring); !errors.Is(err,
				encryption.ErrAuthentication) {
				t.Fatalf("unlock a forged wrap: %v", err)
			}

			relabelled := sealed.Wrap
			relabelled.Wrapped = bytes.Clone(relabelled.Wrapped)
			relabelled.Wrapped[len(relabelled.Wrapped)-1] ^= 1

			if _, err := encryption.Unlock(ck, []encryption.RootWrap{relabelled}, sealed.Keyring); !errors.Is(err,
				encryption.ErrAuthentication) {
				t.Fatalf("unlock a tampered wrap: %v", err)
			}
		})
	}
}

func TestPKCS11KeyOnAToken(t *testing.T) {
	cfg := encryption.PKCS11Config{
		Library: os.Getenv("ELLA_PKCS11_LIBRARY"), TokenLabel: os.Getenv("ELLA_PKCS11_TOKEN"),
		PINFile: os.Getenv("ELLA_PKCS11_PIN_FILE"), KeyLabel: os.Getenv("ELLA_PKCS11_KEY"),
		Mechanism: os.Getenv("ELLA_PKCS11_MECHANISM"),
	}

	if cfg.Library == "" {
		t.Skip("set ELLA_PKCS11_LIBRARY, _TOKEN, _PIN_FILE, _KEY and _MECHANISM to test a token")
	}

	ck := loadHSM(t, cfg)

	sealed, _, err := encryption.Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	again := loadHSM(t, cfg)
	if again.Fingerprint() != ck.Fingerprint() {
		t.Fatalf("fingerprint %s, then %s", ck.Fingerprint(), again.Fingerprint())
	}

	if _, err := encryption.Unlock(again, []encryption.RootWrap{sealed.Wrap}, sealed.Keyring); err != nil {
		t.Fatal(err)
	}

	tampered := sealed.Wrap
	tampered.Wrapped = bytes.Clone(tampered.Wrapped)
	tampered.Wrapped[len(tampered.Wrapped)-1] ^= 1

	if _, err := encryption.Unlock(again, []encryption.RootWrap{tampered}, sealed.Keyring); !errors.Is(err,
		encryption.ErrAuthentication) {
		t.Fatalf("unlock a tampered wrap: %v", err)
	}

	t.Logf("%s %s %s", ck.Type(), ck.Mechanism(), ck.Fingerprint())
}

func TestZeroizedPKCS11KeyRefusesToWork(t *testing.T) {
	ck := loadHSM(t, hsmKey(t, encryption.MechanismAESGCM, "zeroized"))

	sealed, _, err := encryption.Initialize(ck)
	if err != nil {
		t.Fatal(err)
	}

	ck.Zeroize()

	if _, err := ck.Wrap(make([]byte, encryption.KeySize)); !errors.Is(err, encryption.ErrZeroized) {
		t.Fatalf("wrap after zeroize: %v", err)
	}

	if _, err := ck.Unwrap(sealed.Wrap.Wrapped); !errors.Is(err, encryption.ErrZeroized) {
		t.Fatalf("unwrap after zeroize: %v", err)
	}
}

func TestRewrapFromAFileKeyToAnHSMKey(t *testing.T) {
	secret := make([]byte, encryption.KeySize)
	_, _ = rand.Read(secret)

	file, err := encryption.ParseFileKey([]byte(base64.StdEncoding.EncodeToString(secret)))
	if err != nil {
		t.Fatal(err)
	}

	hsm := loadHSM(t, hsmKey(t, encryption.MechanismRSAOAEP, "rewrap"))

	sealed, _, err := encryption.Initialize(file)
	if err != nil {
		t.Fatal(err)
	}

	wrap, err := encryption.Rewrap(hsm, []encryption.RootWrap{sealed.Wrap}, sealed.Keyring, file)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := encryption.Unlock(hsm, []encryption.RootWrap{sealed.Wrap, wrap}, sealed.Keyring); err != nil {
		t.Fatalf("unlock with the HSM key after the rewrap: %v", err)
	}
}

func TestLoadPKCS11KeyRefusesBadKeys(t *testing.T) {
	tok := softhsmtest.Open(t)
	tok.AES(t, "aes-128", 16)
	tok.RSA(t, "rsa-2048", 2048)

	base := hsmKey(t, encryption.MechanismAESGCM, "good")

	insecure := filepath.Join(t.TempDir(), "pin")
	if err := os.WriteFile(insecure, []byte("1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]func(*encryption.PKCS11Config){
		"AES-128":        func(c *encryption.PKCS11Config) { c.KeyLabel = "aes-128" },
		"RSA-2048":       func(c *encryption.PKCS11Config) { c.KeyLabel = "rsa-2048"; c.Mechanism = encryption.MechanismRSAOAEP },
		"no such key":    func(c *encryption.PKCS11Config) { c.KeyLabel = "missing" },
		"wrong type":     func(c *encryption.PKCS11Config) { c.Mechanism = encryption.MechanismRSAOAEP },
		"no such token":  func(c *encryption.PKCS11Config) { c.TokenLabel = "missing" },
		"wrong PIN":      func(c *encryption.PKCS11Config) { c.PINFile = tok.WrongPINFile(t) },
		"insecure PIN":   func(c *encryption.PKCS11Config) { c.PINFile = insecure },
		"bad mechanism":  func(c *encryption.PKCS11Config) { c.Mechanism = "aes-cbc" },
		"no such module": func(c *encryption.PKCS11Config) { c.Library = filepath.Join(t.TempDir(), "missing.so") },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)

			if k, err := encryption.LoadPKCS11Key(cfg); err == nil {
				k.Zeroize()
				t.Fatal("loaded")
			}
		})
	}
}
