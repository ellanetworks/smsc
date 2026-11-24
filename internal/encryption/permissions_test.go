package encryption

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckMode(t *testing.T) {
	for _, c := range []struct {
		owner, euid uint32
		perm        fs.FileMode
		ok          bool
	}{
		{1000, 1000, 0o600, true},
		{1000, 1000, 0o400, true},
		{1000, 1000, 0o640, false},
		{1000, 1000, 0o604, false},
		{1000, 1000, 0o610, false},
		{0, 1000, 0o600, true},
		{0, 1000, 0o640, true},
		{0, 1000, 0o440, true},
		{0, 1000, 0o660, false},
		{0, 1000, 0o650, false},
		{0, 1000, 0o644, false},
		{0, 1000, 0o641, false},
		{2000, 1000, 0o600, false},
		{0, 0, 0o600, true},
		{0, 0, 0o640, true},
		{0, 0, 0o440, true},
		{0, 0, 0o660, false},
		{1000, 0, 0o600, false},
	} {
		err := checkMode(c.owner, c.euid, c.perm)
		if c.ok != (err == nil) || err != nil && !errors.Is(err, ErrInsecurePermissions) {
			t.Fatalf("owner %d, euid %d, mode %#o: %v", c.owner, c.euid, c.perm, err)
		}
	}
}

func TestOwnGroupOnly(t *testing.T) {
	for _, c := range []struct {
		owner, group uint32
		perm         fs.FileMode
		want         bool
	}{
		{1000, 1000, 0o660, true},
		{1000, 1000, 0o640, true},
		{1000, 1000, 0o600, false},
		{1000, 1000, 0o664, false},
		{1000, 2000, 0o660, false},
		{2000, 1000, 0o660, false},
	} {
		if got := ownGroupOnly(c.owner, c.group, 1000, 1000, c.perm); got != c.want {
			t.Fatalf("owner %d, group %d, mode %#o = %t", c.owner, c.group, c.perm, got)
		}
	}
}

func TestCheckPrivate(t *testing.T) {
	dir := t.TempDir()

	if err := CheckPrivate(dir); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("a directory: %v", err)
	}

	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CheckPrivate(path); err != nil {
		t.Fatalf("0600: %v", err)
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}

	if err := CheckPrivate(link); err != nil {
		t.Fatalf("a symlink to a 0600 file: %v", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CheckPrivate(link); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("a symlink to a 0644 file: %v", err)
	}

	if err := CheckPrivate(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file: %v", err)
	}
}

func TestCheckPrivateRemovesOwnGroupAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}

	if err := CheckPrivate(path); err != nil {
		t.Fatalf("a 0660 file of the process's own group: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after the check = %v, %v, want 0600", info.Mode().Perm(), err)
	}
}
