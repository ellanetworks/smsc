package encryption

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const (
	TypeFile        = "file"
	MechanismAESGCM = "aes-gcm"
	MinFileKeySize  = 32

	fileKeyInfo           = "ella-smsc/cluster-key/v1"
	fingerprintLabel      = "ella-smsc-key-id"
	fingerprintSize       = 16
	wrapVersion      byte = 0x01
	wrapAADPrefix         = "ella-smsc/root/v1/"
	tempPattern           = ".encryption-key-*"
	maxKeyFileSize        = 4096
)

var (
	ErrInvalidKeyFile = errors.New("invalid encryption key file")
	ErrKeyFileExists  = errors.New("the encryption key file already exists")
	ErrZeroized       = errors.New("the cluster key has been zeroized")
)

type FileKey struct {
	mu          sync.RWMutex
	kek         []byte
	fingerprint string
}

func ParseFileKey(contents []byte) (*FileKey, error) {
	secret, err := decodeKey(bytes.TrimSpace(contents))
	if err != nil {
		return nil, err
	}

	defer clear(secret)

	if len(secret) < MinFileKeySize {
		return nil, fmt.Errorf("%w: %d bytes, want at least %d", ErrInvalidKeyFile, len(secret), MinFileKeySize)
	}

	kek, err := hkdf.Key(sha256.New, secret, nil, fileKeyInfo, KeySize)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKeyFile, err)
	}

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(fingerprintLabel))

	return &FileKey{kek: kek, fingerprint: hex.EncodeToString(mac.Sum(nil)[:fingerprintSize])}, nil
}

func decodeKey(text []byte) ([]byte, error) {
	if bytes.ContainsAny(text, " \t\r\n\v\f") {
		return nil, fmt.Errorf("%w: the key must be a single line", ErrInvalidKeyFile)
	}

	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		secret := make([]byte, enc.DecodedLen(len(text)))

		n, err := enc.Decode(secret, text)
		if err == nil {
			return secret[:n], nil
		}

		clear(secret)
	}

	return nil, fmt.Errorf("%w: not base64", ErrInvalidKeyFile)
}

func LoadFileKey(path string) (*FileKey, error) {
	contents, k, err := ReadFileKey(path)
	clear(contents)

	return k, err
}

func ReadFileKey(path string) ([]byte, *FileKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open the encryption key file: %w", err)
	}

	defer func() { _ = f.Close() }()

	if err := checkFile(f); err != nil {
		return nil, nil, err
	}

	contents, err := io.ReadAll(io.LimitReader(f, maxKeyFileSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read the encryption key file: %w", err)
	}

	if len(contents) > maxKeyFileSize {
		clear(contents)

		return nil, nil, fmt.Errorf("%s: %w: larger than %d bytes", path, ErrInvalidKeyFile, maxKeyFileSize)
	}

	k, err := ParseFileKey(contents)
	if err != nil {
		clear(contents)

		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	return contents, k, nil
}

func GenerateFileKey(path string) (*FileKey, error) {
	secret := randomBytes(KeySize)
	defer clear(secret)

	contents := make([]byte, base64.StdEncoding.EncodedLen(KeySize)+1)
	defer clear(contents)

	base64.StdEncoding.Encode(contents, secret)
	contents[len(contents)-1] = '\n'

	return CreateFileKey(path, contents)
}

func CreateFileKey(path string, contents []byte) (*FileKey, error) {
	k, err := ParseFileKey(contents)
	if err != nil {
		return nil, err
	}

	if err := createExclusive(path, contents); err != nil {
		k.Zeroize()

		return nil, err
	}

	return k, nil
}

func createExclusive(path string, contents []byte) error {
	dir := filepath.Dir(path)

	if err := removeStaleTemps(dir); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return fmt.Errorf("create the encryption key file: %w", err)
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(contents); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("write the encryption key file: %w", err)
	}

	if err := errors.Join(tmp.Sync(), tmp.Close()); err != nil {
		return fmt.Errorf("write the encryption key file: %w", err)
	}

	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrKeyFileExists, path)
		}

		return fmt.Errorf("install the encryption key file: %w", err)
	}

	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("sync the encryption key directory: %w", err)
	}

	return errors.Join(d.Sync(), d.Close())
}

func removeStaleTemps(dir string) error {
	stale, err := filepath.Glob(filepath.Join(dir, tempPattern))
	if err != nil {
		return err
	}

	for _, name := range stale {
		if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove a stale encryption key file: %w", err)
		}
	}

	return nil
}

func (k *FileKey) Type() string {
	return TypeFile
}

func (k *FileKey) Mechanism() string {
	return MechanismAESGCM
}

func (k *FileKey) Fingerprint() string {
	return k.fingerprint
}

func (k *FileKey) Wrap(root []byte) ([]byte, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.kek == nil {
		return nil, ErrZeroized
	}

	aead, err := newGCM(k.kek)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 1+nonceSize, 1+nonceSize+len(root)+tagSize)
	out[0] = wrapVersion
	copy(out[1:], randomBytes(nonceSize))

	return aead.Seal(out, out[1:1+nonceSize], root, k.wrapAAD()), nil
}

func (k *FileKey) Unwrap(wrapped []byte) ([]byte, error) {
	if len(wrapped) < 1+nonceSize+tagSize || wrapped[0] != wrapVersion {
		return nil, fmt.Errorf("%w: bad root key wrap header", ErrMalformed)
	}

	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.kek == nil {
		return nil, ErrZeroized
	}

	aead, err := newGCM(k.kek)
	if err != nil {
		return nil, err
	}

	root, err := aead.Open(nil, wrapped[1:1+nonceSize], wrapped[1+nonceSize:], k.wrapAAD())
	if err != nil {
		return nil, fmt.Errorf("%w: root key wrap", ErrAuthentication)
	}

	return root, nil
}

func (k *FileKey) Zeroize() {
	k.mu.Lock()
	defer k.mu.Unlock()

	clear(k.kek)
	k.kek = nil
}

func (k *FileKey) wrapAAD() []byte {
	return []byte(wrapAADPrefix + TypeFile + "/" + MechanismAESGCM + "/" + k.fingerprint)
}
