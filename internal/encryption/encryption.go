package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	KeySize  = 32
	Overhead = headerSize + saltSize + nonceSize + tagSize

	magic      byte = 0xE1
	version1   byte = 0x01
	headerSize      = 6
	saltSize        = 32
	nonceSize       = 12
	tagSize         = 16
	valueInfo       = "ella-smsc/v1"
	aadPrefix       = "ella-smsc/v1/"
)

var (
	ErrNotEncrypted       = errors.New("the value is not encrypted")
	ErrUnsupportedVersion = errors.New("the encrypted value has an unsupported version")
	ErrMalformed          = errors.New("the encrypted value is malformed")
	ErrUnknownTerm        = errors.New("the encrypted value uses an unknown data key term")
	ErrAuthentication     = errors.New("the encrypted value failed authentication")
	ErrInvalidLocation    = errors.New("invalid encryption location")
)

type Location struct {
	Table  string
	Column string
	Row    string
}

func (l Location) validate() error {
	if l.Table == "" || l.Column == "" || strings.Contains(l.Table, "/") || strings.Contains(l.Column, "/") {
		return fmt.Errorf("%w: table %q, column %q", ErrInvalidLocation, l.Table, l.Column)
	}

	return nil
}

func (l Location) aad(header []byte) []byte {
	aad := make([]byte, 0, len(header)+len(aadPrefix)+len(l.Table)+len(l.Column)+len(l.Row)+2)
	aad = append(aad, header...)
	aad = append(aad, aadPrefix...)
	aad = append(aad, l.Table...)
	aad = append(aad, '/')
	aad = append(aad, l.Column...)
	aad = append(aad, '/')

	return append(aad, l.Row...)
}

func Term(value []byte) (uint32, error) {
	if len(value) < headerSize || value[0] != magic {
		return 0, ErrNotEncrypted
	}

	if value[1] != version1 {
		return 0, fmt.Errorf("%w: %#x", ErrUnsupportedVersion, value[1])
	}

	if len(value) < Overhead {
		return 0, fmt.Errorf("%w: %d bytes, want at least %d", ErrMalformed, len(value), Overhead)
	}

	return binary.BigEndian.Uint32(value[2:headerSize]), nil
}

func seal(dek []byte, term uint32, plaintext []byte, loc Location, salt, nonce []byte) ([]byte, error) {
	out := make([]byte, headerSize+saltSize+nonceSize, Overhead+len(plaintext))
	out[0], out[1] = magic, version1
	binary.BigEndian.PutUint32(out[2:headerSize], term)
	copy(out[headerSize:], salt)
	copy(out[headerSize+saltSize:], nonce)

	aead, err := valueAEAD(dek, salt)
	if err != nil {
		return nil, err
	}

	return aead.Seal(out, nonce, plaintext, loc.aad(out[:headerSize])), nil
}

func open(dek, value []byte, loc Location) ([]byte, error) {
	salt := value[headerSize : headerSize+saltSize]
	nonce := value[headerSize+saltSize : headerSize+saltSize+nonceSize]

	aead, err := valueAEAD(dek, salt)
	if err != nil {
		return nil, err
	}

	plaintext, err := aead.Open(nil, nonce, value[headerSize+saltSize+nonceSize:], loc.aad(value[:headerSize]))
	if err != nil {
		return nil, ErrAuthentication
	}

	if plaintext == nil {
		plaintext = []byte{}
	}

	return plaintext, nil
}

func valueAEAD(dek, salt []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, dek, salt, valueInfo, KeySize)
	if err != nil {
		return nil, fmt.Errorf("derive a value key: %w", err)
	}

	defer clear(key)

	return newGCM(key)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return cipher.NewGCM(block)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)

	return b
}
