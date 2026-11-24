package encryption

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
)

const (
	keyringVersion    byte = 0x01
	keyringAAD             = "ella-smsc/keyring/v1"
	keyringHeaderSize      = 1 + 4 + 4
	termEntrySize          = 4 + KeySize
)

var (
	ErrInvalidKeyring = errors.New("invalid keyring")
	ErrLocked         = errors.New("the encryption keys are locked")
)

type Keyring struct {
	mu     sync.RWMutex
	active uint32
	terms  map[uint32][]byte
}

func NewKeyring() *Keyring {
	return &Keyring{active: 1, terms: map[uint32][]byte{1: randomBytes(KeySize)}}
}

func (k *Keyring) ActiveTerm() uint32 {
	return k.active
}

func (k *Keyring) Terms() []uint32 {
	k.mu.RLock()
	defer k.mu.RUnlock()

	return slices.Sorted(maps.Keys(k.terms))
}

func (k *Keyring) encrypt(plaintext []byte, loc Location) ([]byte, error) {
	if err := loc.validate(); err != nil {
		return nil, err
	}

	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.terms == nil {
		return nil, ErrLocked
	}

	return seal(k.terms[k.active], k.active, plaintext, loc, randomBytes(saltSize), randomBytes(nonceSize))
}

func (k *Keyring) decrypt(value []byte, loc Location) ([]byte, error) {
	if err := loc.validate(); err != nil {
		return nil, err
	}

	term, err := Term(value)
	if err != nil {
		return nil, err
	}

	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.terms == nil {
		return nil, ErrLocked
	}

	dek, ok := k.terms[term]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownTerm, term)
	}

	return open(dek, value, loc)
}

func (k *Keyring) rotate() (*Keyring, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.terms == nil {
		return nil, ErrLocked
	}

	if k.active == math.MaxUint32 {
		return nil, fmt.Errorf("%w: no term after %d", ErrInvalidKeyring, k.active)
	}

	next := &Keyring{active: k.active + 1, terms: make(map[uint32][]byte, len(k.terms)+1)}
	for term, dek := range k.terms {
		next.terms[term] = bytes.Clone(dek)
	}

	next.terms[next.active] = randomBytes(KeySize)

	return next, nil
}

func (k *Keyring) Zeroize() {
	k.mu.Lock()
	defer k.mu.Unlock()

	for _, dek := range k.terms {
		clear(dek)
	}

	k.terms = nil
}

func (k *Keyring) marshal() ([]byte, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.terms == nil {
		return nil, ErrLocked
	}

	terms := slices.Sorted(maps.Keys(k.terms))

	b := make([]byte, 0, keyringHeaderSize+len(terms)*termEntrySize)
	b = append(b, keyringVersion)
	b = binary.BigEndian.AppendUint32(b, k.active)
	b = binary.BigEndian.AppendUint32(b, uint32(len(terms)))

	for _, term := range terms {
		b = binary.BigEndian.AppendUint32(b, term)
		b = append(b, k.terms[term]...)
	}

	return b, nil
}

func unmarshalKeyring(b []byte) (*Keyring, error) {
	if len(b) < keyringHeaderSize || b[0] != keyringVersion {
		return nil, fmt.Errorf("%w: bad header", ErrInvalidKeyring)
	}

	active := binary.BigEndian.Uint32(b[1:5])
	count := binary.BigEndian.Uint32(b[5:9])
	entries := b[keyringHeaderSize:]

	if count == 0 || uint64(len(entries)) != uint64(count)*termEntrySize {
		return nil, fmt.Errorf("%w: %d terms in %d bytes", ErrInvalidKeyring, count, len(entries))
	}

	k := &Keyring{active: active, terms: make(map[uint32][]byte, count)}

	var highest uint32

	for entry := range slices.Chunk(entries, termEntrySize) {
		term := binary.BigEndian.Uint32(entry[:4])
		if _, dup := k.terms[term]; term == 0 || dup {
			k.Zeroize()

			return nil, fmt.Errorf("%w: term %d is zero or repeated", ErrInvalidKeyring, term)
		}

		k.terms[term] = bytes.Clone(entry[4:])
		highest = max(highest, term)
	}

	if highest != active {
		k.Zeroize()

		return nil, fmt.Errorf("%w: active term %d is not the highest term %d", ErrInvalidKeyring, active, highest)
	}

	return k, nil
}

func sealKeyring(root []byte, k *Keyring) ([]byte, error) {
	plaintext, err := k.marshal()
	if err != nil {
		return nil, err
	}

	defer clear(plaintext)

	aead, err := newGCM(root)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 1+nonceSize, 1+nonceSize+len(plaintext)+tagSize)
	out[0] = keyringVersion
	copy(out[1:], randomBytes(nonceSize))

	return aead.Seal(out, out[1:1+nonceSize], plaintext, []byte(keyringAAD)), nil
}

func openKeyring(root, sealed []byte) (*Keyring, error) {
	if len(sealed) < 1+nonceSize+tagSize || sealed[0] != keyringVersion {
		return nil, fmt.Errorf("%w: bad sealed header", ErrInvalidKeyring)
	}

	aead, err := newGCM(root)
	if err != nil {
		return nil, err
	}

	plaintext, err := aead.Open(nil, sealed[1:1+nonceSize], sealed[1+nonceSize:], []byte(keyringAAD))
	if err != nil {
		return nil, fmt.Errorf("%w: keyring", ErrAuthentication)
	}

	defer clear(plaintext)

	return unmarshalKeyring(plaintext)
}
