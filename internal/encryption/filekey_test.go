package encryption

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func encodeStd(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func TestFileKeyKnownAnswer(t *testing.T) {
	k, err := ParseFileKey([]byte("ZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXp7fH1+f4CBgoM=\n"))
	if err != nil {
		t.Fatal(err)
	}

	if k.Fingerprint() != "c4e4a9abd82c084952dbb96cef3e19a7" {
		t.Fatalf("fingerprint = %s", k.Fingerprint())
	}

	wrapped := mustHex(t, "010102030405060708090a0b0cf0c9f3ad5c6bba79e04d221c583f757d1c5b37554ed53d3a47cf1ef68b7e02e98cc703"+
		"e02ec8cff1a4ff5e7fe83e24fb")

	root, err := k.Unwrap(wrapped)
	if err != nil || !bytes.Equal(root, seq(200, 32)) {
		t.Fatalf("unwrap = %x, %v", root, err)
	}
}

func TestParseFileKey(t *testing.T) {
	secret := randomBytes(48)

	want, err := ParseFileKey([]byte(encodeStd(secret)))
	if err != nil {
		t.Fatal(err)
	}

	for name, text := range map[string]string{
		"newline":     encodeStd(secret) + "\n",
		"crlf":        " " + encodeStd(secret) + "\r\n",
		"raw":         base64.RawStdEncoding.EncodeToString(secret),
		"url":         base64.URLEncoding.EncodeToString(secret),
		"raw url":     base64.RawURLEncoding.EncodeToString(secret),
		"blank lines": encodeStd(secret) + "\n\n",
	} {
		k, err := ParseFileKey([]byte(text))
		if err != nil || k.Fingerprint() != want.Fingerprint() {
			t.Fatalf("%s: %v, %v", name, k, err)
		}
	}

	for name, text := range map[string]string{
		"empty":      "",
		"short":      encodeStd(randomBytes(MinFileKeySize - 1)),
		"not base64": "this is not base64 but it is long enough to be a key",
		"two lines":  encodeStd(secret) + "\n" + encodeStd(secret),
	} {
		if _, err := ParseFileKey([]byte(text)); !errors.Is(err, ErrInvalidKeyFile) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	other, err := ParseFileKey([]byte(encodeStd(randomBytes(48))))
	if err != nil || other.Fingerprint() == want.Fingerprint() {
		t.Fatalf("another key: %v, %v", other, err)
	}
}

func TestFileKeyWrap(t *testing.T) {
	k := testFileKey(t)
	root := randomBytes(KeySize)

	wrapped, err := k.Wrap(root)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Contains(wrapped, root) {
		t.Fatal("root key in the clear")
	}

	got, err := k.Unwrap(wrapped)
	if err != nil || !bytes.Equal(got, root) {
		t.Fatalf("unwrap = %x, %v", got, err)
	}

	if _, err := testFileKey(t).Unwrap(wrapped); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("unwrap with another key: %v", err)
	}

	for i := range wrapped {
		tampered := bytes.Clone(wrapped)
		tampered[i] ^= 0x01

		if _, err := k.Unwrap(tampered); err == nil {
			t.Fatalf("unwrapped with byte %d flipped", i)
		}
	}
}

func TestGenerateFileKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encryption.key")

	k, err := GenerateFileKey(path)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat = %v, %v", info, err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFileKey(path)
	if err != nil || loaded.Fingerprint() != k.Fingerprint() {
		t.Fatalf("load = %v, %v", loaded, err)
	}

	if _, err := GenerateFileKey(path); !errors.Is(err, ErrKeyFileExists) {
		t.Fatalf("generate over an existing key: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, contents) {
		t.Fatal("the existing key file changed")
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory holds %v, %v", entries, err)
	}
}

func TestCreateFileKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encryption.key")

	if _, err := CreateFileKey(path, []byte("short")); !errors.Is(err, ErrInvalidKeyFile) {
		t.Fatalf("create an invalid key: %v", err)
	}

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an invalid key was written: %v", err)
	}

	contents := []byte(encodeStd(randomBytes(KeySize)) + "\n")

	k, err := CreateFileKey(path, contents)
	if err != nil {
		t.Fatal(err)
	}

	want, _ := ParseFileKey(contents)
	if k.Fingerprint() != want.Fingerprint() {
		t.Fatal("fingerprint differs from the contents")
	}
}

func TestLoadFileKeyChecksPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encryption.key")
	if err := os.WriteFile(path, []byte(encodeStd(randomBytes(KeySize))), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadFileKey(path); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("load a 0644 key: %v", err)
	}

	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadFileKey(path); err != nil {
		t.Fatalf("load a 0400 key: %v", err)
	}

	if _, err := LoadFileKey(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("load a missing key: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	for name, contents := range map[string]string{
		"not base64": "not base64 at all, but more than thirty-two bytes long",
		"two lines":  encodeStd(randomBytes(KeySize)) + "\n" + encodeStd(randomBytes(KeySize)) + "\n",
		"too large":  encodeStd(randomBytes(4 * maxKeyFileSize)),
	} {
		if err := os.WriteFile(path, []byte(contents), 0o400); err != nil {
			t.Fatal(err)
		}

		if _, err := LoadFileKey(path); !errors.Is(err, ErrInvalidKeyFile) {
			t.Fatalf("%s: %v", name, err)
		}

		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadGeneratedKeyAfterFSGroupChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encryption.key")

	k, err := GenerateFileKey(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFileKey(path)
	if err != nil || loaded.Fingerprint() != k.Fingerprint() {
		t.Fatalf("load after fsGroup made the key 0660 = %v, %v", loaded, err)
	}
}

func TestGenerateFileKeyRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "encryption.key")

	type result struct {
		k   *FileKey
		err error
	}

	results := make(chan result, 8)

	for range cap(results) {
		go func() {
			k, err := GenerateFileKey(path)
			results <- result{k, err}
		}()
	}

	var winners []*FileKey

	for range cap(results) {
		r := <-results
		if r.err == nil {
			winners = append(winners, r.k)
		}
	}

	if len(winners) > 1 {
		t.Fatalf("%d generations succeeded", len(winners))
	}

	loaded, err := LoadFileKey(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(winners) == 1 && loaded.Fingerprint() != winners[0].Fingerprint() {
		t.Fatal("the key on disk is not the winner's")
	}
}

func TestStaleTempFilesAreRemoved(t *testing.T) {
	dir := t.TempDir()

	stale := filepath.Join(dir, ".encryption-key-123")
	if err := os.WriteFile(stale, []byte("leftover key material"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := GenerateFileKey(filepath.Join(dir, "encryption.key")); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale temp file: %v", err)
	}
}
