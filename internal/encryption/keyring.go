package encryption

import (
	"bytes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"
)

const (
	KeyringIDSize = 16

	keyringVersion    byte = 0x01
	upgradeVersion    byte = 0x01
	keyringAAD             = "ella-smsc/keyring/v1/"
	upgradeInfo            = "ella-smsc/upgrade/v1"
	keyringHeaderSize      = 1 + 4 + 4
	termEntrySize          = 4 + KeySize
)

var (
	ErrInvalidKeyring = errors.New("invalid keyring")
	ErrLocked         = errors.New("the encryption keys are locked")
	ErrNoUpgrade      = errors.New("the keyring cannot be upgraded in place")
)

type Keyring struct {
	mu     sync.RWMutex
	id     []byte
	active uint32
	terms  map[uint32][]byte
}

type Upgrade struct {
	Term   uint32
	Sealed []byte
}

func NewKeyring() *Keyring {
	return &Keyring{id: randomBytes(KeyringIDSize), active: 1, terms: map[uint32][]byte{1: randomBytes(KeySize)}}
}

func (k *Keyring) ID() []byte {
	return bytes.Clone(k.id)
}

func (k *Keyring) ActiveTerm() uint32 {
	return k.active
}

func (k *Keyring) Encrypt(plaintext []byte, loc Location) ([]byte, error) {
	return k.encrypt(plaintext, loc)
}

func (k *Keyring) Decrypt(value []byte, loc Location) ([]byte, error) {
	return k.decrypt(value, loc)
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

func (k *Keyring) rotate() (*Keyring, Upgrade, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.terms == nil {
		return nil, Upgrade{}, ErrLocked
	}

	if k.active == math.MaxUint32 {
		return nil, Upgrade{}, fmt.Errorf("%w: no term after %d", ErrInvalidKeyring, k.active)
	}

	next := k.cloneLocked()
	next.active = k.active + 1
	next.terms[next.active] = randomBytes(KeySize)

	sealed, err := sealUpgrade(k.terms[k.active], k.id, next.active, next.terms[next.active])
	if err != nil {
		next.Zeroize()

		return nil, Upgrade{}, err
	}

	return next, Upgrade{Term: next.active, Sealed: sealed}, nil
}

func (k *Keyring) without(remove []uint32) (*Keyring, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	if k.terms == nil {
		return nil, ErrLocked
	}

	next := k.cloneLocked()

	for _, term := range remove {
		if term == k.active {
			next.Zeroize()

			return nil, fmt.Errorf("%w: the active term %d cannot be removed", ErrInvalidKeyring, term)
		}

		clear(next.terms[term])
		delete(next.terms, term)
	}

	return next, nil
}

func (k *Keyring) advance(id []byte, active uint32, live []uint32, upgrades map[uint32][]byte) (*Keyring, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	switch {
	case k.terms == nil:
		return nil, ErrLocked
	case !bytes.Equal(k.id, id):
		return nil, fmt.Errorf("%w: another keyring", ErrNoUpgrade)
	case active < k.active:
		return nil, fmt.Errorf("%w: the active term %d is below %d", ErrNoUpgrade, active, k.active)
	}

	next := k.cloneLocked()

	for term := k.active + 1; term <= active; term++ {
		dek, err := openUpgrade(next.terms[term-1], k.id, term, upgrades[term])
		if err != nil {
			next.Zeroize()

			return nil, fmt.Errorf("%w: term %d: %w", ErrNoUpgrade, term, err)
		}

		next.terms[term] = dek
		next.active = term
	}

	keep := make(map[uint32]bool, len(live))
	for _, term := range live {
		keep[term] = true
	}

	for term, dek := range next.terms {
		if !keep[term] {
			clear(dek)
			delete(next.terms, term)
		}
	}

	if len(next.terms) != len(keep) || next.terms[next.active] == nil {
		next.Zeroize()

		return nil, fmt.Errorf("%w: the live terms %v are not all known", ErrNoUpgrade, live)
	}

	return next, nil
}

func (k *Keyring) cloneLocked() *Keyring {
	next := &Keyring{id: bytes.Clone(k.id), active: k.active, terms: make(map[uint32][]byte, len(k.terms)+1)}
	for term, dek := range k.terms {
		next.terms[term] = bytes.Clone(dek)
	}

	return next
}

func upgradeAAD(id []byte, term uint32) []byte {
	aad := append([]byte(upgradeInfo+"/"), id...)
	return binary.BigEndian.AppendUint32(aad, term)
}

func upgradeAEAD(previous []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, previous, nil, upgradeInfo, KeySize)
	if err != nil {
		return nil, err
	}

	defer clear(key)

	return newGCM(key)
}

func sealUpgrade(previous, id []byte, term uint32, dek []byte) ([]byte, error) {
	aead, err := upgradeAEAD(previous)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 1+nonceSize, 1+nonceSize+KeySize+tagSize)
	out[0] = upgradeVersion
	copy(out[1:], randomBytes(nonceSize))

	return aead.Seal(out, out[1:1+nonceSize], dek, upgradeAAD(id, term)), nil
}

func openUpgrade(previous, id []byte, term uint32, sealed []byte) ([]byte, error) {
	if previous == nil {
		return nil, fmt.Errorf("the term %d is not known", term-1)
	}

	if len(sealed) != 1+nonceSize+KeySize+tagSize || sealed[0] != upgradeVersion {
		return nil, fmt.Errorf("%w: upgrade", ErrMalformed)
	}

	aead, err := upgradeAEAD(previous)
	if err != nil {
		return nil, err
	}

	dek, err := aead.Open(nil, sealed[1:1+nonceSize], sealed[1+nonceSize:], upgradeAAD(id, term))
	if err != nil {
		return nil, fmt.Errorf("%w: upgrade", ErrAuthentication)
	}

	return dek, nil
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
	if len(k.id) != KeyringIDSize {
		return nil, fmt.Errorf("%w: a keyring ID of %d bytes", ErrInvalidKeyring, len(k.id))
	}

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

	return aead.Seal(out, out[1:1+nonceSize], plaintext, append([]byte(keyringAAD), k.id...)), nil
}

func openKeyring(root, sealed, id []byte) (*Keyring, error) {
	if len(sealed) < 1+nonceSize+tagSize || sealed[0] != keyringVersion {
		return nil, fmt.Errorf("%w: bad sealed header", ErrInvalidKeyring)
	}

	aead, err := newGCM(root)
	if err != nil {
		return nil, err
	}

	plaintext, err := aead.Open(nil, sealed[1:1+nonceSize], sealed[1+nonceSize:], append([]byte(keyringAAD), id...))
	if err != nil {
		return nil, fmt.Errorf("%w: keyring", ErrAuthentication)
	}

	defer clear(plaintext)

	k, err := unmarshalKeyring(plaintext)
	if err != nil {
		return nil, err
	}

	k.id = bytes.Clone(id)

	return k, nil
}
