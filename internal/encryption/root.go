package encryption

import (
	"errors"
	"fmt"
)

var (
	ErrNoMatchingWrap = errors.New("no root key wrap matches the cluster key")
	ErrInvalidRootKey = errors.New("invalid root key")
)

type ClusterKey interface {
	Type() string
	Mechanism() string
	Fingerprint() string
	Wrap(rootKey []byte) ([]byte, error)
	Unwrap(wrapped []byte) ([]byte, error)
	Zeroize()
}

type RootWrap struct {
	Fingerprint string
	Type        string
	Mechanism   string
	Wrapped     []byte
}

type Sealed struct {
	Wrap      RootWrap
	Keyring   []byte
	KeyringID []byte
}

func Initialize(ck ClusterKey) (Sealed, *Keyring, error) {
	k := NewKeyring()

	sealed, err := sealUnder(ck, k)
	if err != nil {
		k.Zeroize()

		return Sealed{}, nil, err
	}

	return sealed, k, nil
}

func Unlock(ck ClusterKey, wraps []RootWrap, sealedKeyring, keyringID []byte) (*Keyring, error) {
	root, err := unwrapRoot(ck, wraps)
	if err != nil {
		return nil, err
	}

	defer clear(root)

	return openKeyring(root, sealedKeyring, keyringID)
}

func Rewrap(to ClusterKey, wraps []RootWrap, sealedKeyring, keyringID []byte, from ClusterKey) (RootWrap, error) {
	root, err := unwrapRoot(from, wraps)
	if err != nil {
		return RootWrap{}, err
	}

	defer clear(root)

	k, err := openKeyring(root, sealedKeyring, keyringID)
	if err != nil {
		return RootWrap{}, err
	}

	k.Zeroize()

	return wrapRoot(to, root)
}

func sealUnder(ck ClusterKey, k *Keyring) (Sealed, error) {
	root := randomBytes(KeySize)
	defer clear(root)

	sealedKeyring, err := sealKeyring(root, k)
	if err != nil {
		return Sealed{}, err
	}

	wrap, err := wrapRoot(ck, root)
	if err != nil {
		return Sealed{}, err
	}

	return Sealed{Wrap: wrap, Keyring: sealedKeyring, KeyringID: k.ID()}, nil
}

func wrapRoot(ck ClusterKey, root []byte) (RootWrap, error) {
	wrapped, err := ck.Wrap(root)
	if err != nil {
		return RootWrap{}, fmt.Errorf("wrap the root key with the %s cluster key: %w", ck.Type(), err)
	}

	return RootWrap{Fingerprint: ck.Fingerprint(), Type: ck.Type(), Mechanism: ck.Mechanism(), Wrapped: wrapped}, nil
}

func unwrapRoot(ck ClusterKey, wraps []RootWrap) ([]byte, error) {
	for _, w := range wraps {
		if w.Fingerprint != ck.Fingerprint() || w.Type != ck.Type() || w.Mechanism != ck.Mechanism() {
			continue
		}

		root, err := ck.Unwrap(w.Wrapped)
		if err != nil {
			return nil, fmt.Errorf("unwrap the root key with the %s cluster key %s: %w", ck.Type(), ck.Fingerprint(), err)
		}

		if len(root) != KeySize {
			clear(root)

			return nil, fmt.Errorf("%w: %d bytes", ErrInvalidRootKey, len(root))
		}

		return root, nil
	}

	return nil, fmt.Errorf("%w: %s %s", ErrNoMatchingWrap, ck.Type(), ck.Fingerprint())
}
