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

func (h *Keys) Rotate(ck ClusterKey) (Sealed, *Keyring, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.ring == nil {
		return Sealed{}, nil, ErrLocked
	}

	next, err := h.ring.rotate()
	if err != nil {
		return Sealed{}, nil, err
	}

	sealed, err := sealUnder(ck, next)
	if err != nil {
		next.Zeroize()

		return Sealed{}, nil, err
	}

	return sealed, next, nil
}
