package encryption

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

var ErrInsecurePermissions = errors.New("insecure file permissions")

func CheckPrivate(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}

	defer func() { _ = f.Close() }()

	return checkFile(f)
}

func checkFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrInsecurePermissions, f.Name())
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: cannot read the owner of %s", ErrInsecurePermissions, f.Name())
	}

	euid, perm := uint32(os.Geteuid()), info.Mode().Perm()

	if ownGroupOnly(st.Uid, st.Gid, euid, uint32(os.Getegid()), perm) {
		if err := f.Chmod(perm &^ 0o070); err != nil {
			return fmt.Errorf("%w: remove group access from %s: %w", ErrInsecurePermissions, f.Name(), err)
		}

		perm &^= 0o070
	}

	if err := checkMode(st.Uid, euid, perm); err != nil {
		return fmt.Errorf("%s: %w", f.Name(), err)
	}

	return nil
}

func ownGroupOnly(owner, group, euid, egid uint32, perm fs.FileMode) bool {
	return owner == euid && group == egid && perm&0o070 != 0 && perm&0o007 == 0
}

func checkMode(owner, euid uint32, perm fs.FileMode) error {
	switch owner {
	case 0:
		if perm&0o037 != 0 {
			return fmt.Errorf("%w: owned by root with mode %#o, want 0640 or stricter", ErrInsecurePermissions, perm)
		}
	case euid:
		if perm&0o077 != 0 {
			return fmt.Errorf("%w: mode %#o allows group or other access, want 0600 or stricter", ErrInsecurePermissions, perm)
		}
	default:
		return fmt.Errorf("%w: owned by uid %d, want uid %d or root", ErrInsecurePermissions, owner, euid)
	}

	return nil
}
