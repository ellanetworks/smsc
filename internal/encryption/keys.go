package encryption

import "sync"

type Keys struct {
	mu   sync.RWMutex
	ring *Keyring
}

func (h *Keys) Install(k *Keyring) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.ring != nil && h.ring != k {
		h.ring.Zeroize()
	}

	h.ring = k
}

func (h *Keys) Lock() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.ring != nil {
		h.ring.Zeroize()
	}

	h.ring = nil
}

func (h *Keys) Unlocked() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.ring != nil
}

func (h *Keys) ActiveTerm() (uint32, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.ring == nil {
		return 0, ErrLocked
	}

	return h.ring.ActiveTerm(), nil
}

func (h *Keys) Encrypt(plaintext []byte, loc Location) ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.ring == nil {
		return nil, ErrLocked
	}

	return h.ring.encrypt(plaintext, loc)
}

func (h *Keys) Decrypt(value []byte, loc Location) ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.ring == nil {
		return nil, ErrLocked
	}

	return h.ring.decrypt(value, loc)
}

func (h *Keys) KeyringID() ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.ring == nil {
		return nil, ErrLocked
	}

	return h.ring.ID(), nil
}

func (h *Keys) Rotate(ck ClusterKey) (Sealed, *Keyring, Upgrade, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.ring == nil {
		return Sealed{}, nil, Upgrade{}, ErrLocked
	}

	next, upgrade, err := h.ring.rotate()
	if err != nil {
		return Sealed{}, nil, Upgrade{}, err
	}

	sealed, err := sealUnder(ck, next)
	if err != nil {
		next.Zeroize()

		return Sealed{}, nil, Upgrade{}, err
	}

	return sealed, next, upgrade, nil
}

func (h *Keys) Prune(ck ClusterKey, remove []uint32) (Sealed, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.ring == nil {
		return Sealed{}, ErrLocked
	}

	next, err := h.ring.without(remove)
	if err != nil {
		return Sealed{}, err
	}

	defer next.Zeroize()

	return sealUnder(ck, next)
}

func (h *Keys) Advance(id []byte, active uint32, live []uint32, upgrades map[uint32][]byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.ring == nil {
		return ErrLocked
	}

	next, err := h.ring.advance(id, active, live, upgrades)
	if err != nil {
		return err
	}

	h.ring.Zeroize()
	h.ring = next

	return nil
}
