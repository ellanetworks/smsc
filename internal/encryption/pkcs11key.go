package encryption

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"sync"

	"github.com/miekg/pkcs11"
)

const (
	TypePKCS11        = "pkcs11"
	MechanismRSAOAEP  = "rsa-oaep"
	MinRSAKeyBits     = 3072
	maxPINFileSize    = 1024
	rsaFingerprintLen = 8
)

var (
	ErrPKCS11         = errors.New("pkcs11")
	ErrInvalidPINFile = errors.New("invalid PIN file")
)

type PKCS11Config struct {
	Library    string
	TokenLabel string
	Slot       *uint
	PINFile    string
	KeyLabel   string
	Mechanism  string
}

type PKCS11Key struct {
	mu          sync.Mutex
	ctx         *pkcs11.Ctx
	session     pkcs11.SessionHandle
	open        bool
	key         pkcs11.ObjectHandle
	public      *rsa.PublicKey
	mechanism   string
	fingerprint string
}

var (
	librariesMu sync.Mutex
	libraries   = map[string]*pkcs11.Ctx{}
)

func library(path string) (*pkcs11.Ctx, error) {
	librariesMu.Lock()
	defer librariesMu.Unlock()

	if ctx, ok := libraries[path]; ok {
		return ctx, nil
	}

	ctx := pkcs11.New(path)
	if ctx == nil {
		return nil, fmt.Errorf("%w: cannot load the library %s", ErrPKCS11, path)
	}

	if err := ctx.Initialize(); err != nil && !isCode(err, pkcs11.CKR_CRYPTOKI_ALREADY_INITIALIZED) {
		ctx.Destroy()

		return nil, fmt.Errorf("%w: initialize %s: %w", ErrPKCS11, path, err)
	}

	libraries[path] = ctx

	return ctx, nil
}

func isCode(err error, code uint) bool {
	var e pkcs11.Error

	return errors.As(err, &e) && uint(e) == code
}

func LoadPKCS11Key(cfg PKCS11Config) (*PKCS11Key, error) {
	if cfg.Mechanism != MechanismAESGCM && cfg.Mechanism != MechanismRSAOAEP {
		return nil, fmt.Errorf("%w: unsupported mechanism %q", ErrPKCS11, cfg.Mechanism)
	}

	pin, err := ReadPIN(cfg.PINFile)
	if err != nil {
		return nil, err
	}

	defer clear(pin)

	ctx, err := library(cfg.Library)
	if err != nil {
		return nil, err
	}

	slot, err := findSlot(ctx, cfg)
	if err != nil {
		return nil, err
	}

	session, err := ctx.OpenSession(slot, pkcs11.CKF_SERIAL_SESSION)
	if err != nil {
		return nil, fmt.Errorf("%w: open a session: %w", ErrPKCS11, err)
	}

	k := &PKCS11Key{ctx: ctx, session: session, open: true, mechanism: cfg.Mechanism}

	if err := k.find(cfg, string(pin)); err != nil {
		k.Zeroize()

		return nil, err
	}

	return k, nil
}

func ReadPIN(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open the PIN file: %w", err)
	}

	defer func() { _ = f.Close() }()

	if err := checkFile(f); err != nil {
		return nil, err
	}

	contents, err := io.ReadAll(io.LimitReader(f, maxPINFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read the PIN file: %w", err)
	}

	defer clear(contents)

	pin := bytes.TrimRight(contents, "\r\n")

	switch {
	case len(contents) > maxPINFileSize:
		return nil, fmt.Errorf("%s: %w: larger than %d bytes", path, ErrInvalidPINFile, maxPINFileSize)
	case len(pin) == 0:
		return nil, fmt.Errorf("%s: %w: empty", path, ErrInvalidPINFile)
	case bytes.ContainsAny(pin, "\r\n"):
		return nil, fmt.Errorf("%s: %w: the PIN must be a single line", path, ErrInvalidPINFile)
	}

	return bytes.Clone(pin), nil
}

func findSlot(ctx *pkcs11.Ctx, cfg PKCS11Config) (uint, error) {
	if cfg.Slot != nil {
		return *cfg.Slot, nil
	}

	slots, err := ctx.GetSlotList(true)
	if err != nil {
		return 0, fmt.Errorf("%w: list the slots: %w", ErrPKCS11, err)
	}

	var found []uint

	for _, slot := range slots {
		info, err := ctx.GetTokenInfo(slot)
		if err != nil {
			continue
		}

		if info.Label == cfg.TokenLabel {
			found = append(found, slot)
		}
	}

	switch len(found) {
	case 0:
		return 0, fmt.Errorf("%w: no token labelled %q", ErrPKCS11, cfg.TokenLabel)
	case 1:
		return found[0], nil
	default:
		return 0, fmt.Errorf("%w: %d tokens labelled %q", ErrPKCS11, len(found), cfg.TokenLabel)
	}
}

func (k *PKCS11Key) find(cfg PKCS11Config, pin string) error {
	if err := k.ctx.Login(k.session, pkcs11.CKU_USER, pin); err != nil &&
		!isCode(err, pkcs11.CKR_USER_ALREADY_LOGGED_IN) {
		return fmt.Errorf("%w: log in to the token: %w", ErrPKCS11, err)
	}

	if k.mechanism == MechanismAESGCM {
		return k.findAES(cfg.KeyLabel)
	}

	return k.findRSA(cfg.KeyLabel)
}

func (k *PKCS11Key) findAES(label string) error {
	key, err := k.object(label, pkcs11.CKO_SECRET_KEY, pkcs11.CKK_AES)
	if err != nil {
		return err
	}

	attrs, err := k.ctx.GetAttributeValue(k.session, key, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_VALUE_LEN, nil),
	})
	if err != nil {
		return fmt.Errorf("%w: read the length of the key %q: %w", ErrPKCS11, label, err)
	}

	if n := ulong(attrs[0].Value); n != KeySize {
		return fmt.Errorf("%w: the key %q is AES-%d, want AES-%d", ErrPKCS11, label, 8*n, 8*KeySize)
	}

	k.key = key
	k.fingerprint = label

	check, err := k.ctx.GetAttributeValue(k.session, key, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CHECK_VALUE, nil),
	})
	if err == nil && len(check[0].Value) > 0 {
		k.fingerprint = label + "/" + hex.EncodeToString(check[0].Value)
	}

	return nil
}

func (k *PKCS11Key) findRSA(label string) error {
	key, err := k.object(label, pkcs11.CKO_PRIVATE_KEY, pkcs11.CKK_RSA)
	if err != nil {
		return err
	}

	pub, err := k.rsaPublic(key)
	if err != nil {
		public, findErr := k.object(label, pkcs11.CKO_PUBLIC_KEY, pkcs11.CKK_RSA)
		if findErr != nil {
			return fmt.Errorf("%w: read the public key of %q: %w", ErrPKCS11, label, err)
		}

		if pub, err = k.rsaPublic(public); err != nil {
			return fmt.Errorf("%w: read the public key of %q: %w", ErrPKCS11, label, err)
		}
	}

	if pub.N.BitLen() < MinRSAKeyBits {
		return fmt.Errorf("%w: the key %q is RSA-%d, want at least RSA-%d", ErrPKCS11, label, pub.N.BitLen(),
			MinRSAKeyBits)
	}

	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(der)

	k.key = key
	k.public = pub
	k.fingerprint = label + "/" + hex.EncodeToString(sum[:rsaFingerprintLen])

	return nil
}

func (k *PKCS11Key) rsaPublic(key pkcs11.ObjectHandle) (*rsa.PublicKey, error) {
	attrs, err := k.ctx.GetAttributeValue(k.session, key, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_MODULUS, nil),
		pkcs11.NewAttribute(pkcs11.CKA_PUBLIC_EXPONENT, nil),
	})
	if err != nil {
		return nil, err
	}

	e := new(big.Int).SetBytes(attrs[1].Value)
	if len(attrs[0].Value) == 0 || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
		return nil, errors.New("invalid RSA public key")
	}

	return &rsa.PublicKey{N: new(big.Int).SetBytes(attrs[0].Value), E: int(e.Int64())}, nil
}

func (k *PKCS11Key) object(label string, class, keyType uint) (pkcs11.ObjectHandle, error) {
	template := []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, class),
		pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, keyType),
		pkcs11.NewAttribute(pkcs11.CKA_LABEL, label),
	}

	if err := k.ctx.FindObjectsInit(k.session, template); err != nil {
		return 0, fmt.Errorf("%w: find the key %q: %w", ErrPKCS11, label, err)
	}

	found, _, err := k.ctx.FindObjects(k.session, 2)
	if finalErr := k.ctx.FindObjectsFinal(k.session); err == nil {
		err = finalErr
	}

	switch {
	case err != nil:
		return 0, fmt.Errorf("%w: find the key %q: %w", ErrPKCS11, label, err)
	case len(found) == 0:
		return 0, fmt.Errorf("%w: no key labelled %q", ErrPKCS11, label)
	case len(found) > 1:
		return 0, fmt.Errorf("%w: more than one key labelled %q", ErrPKCS11, label)
	}

	return found[0], nil
}

func ulong(b []byte) uint64 {
	switch len(b) {
	case 8:
		return binary.NativeEndian.Uint64(b)
	case 4:
		return uint64(binary.NativeEndian.Uint32(b))
	default:
		return 0
	}
}

func (k *PKCS11Key) Type() string {
	return TypePKCS11
}

func (k *PKCS11Key) Mechanism() string {
	return k.mechanism
}

func (k *PKCS11Key) Fingerprint() string {
	return k.fingerprint
}

func (k *PKCS11Key) wrapAAD() []byte {
	return []byte(wrapAADPrefix + TypePKCS11 + "/" + k.mechanism + "/" + k.fingerprint)
}

func (k *PKCS11Key) Wrap(root []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if !k.open {
		return nil, ErrZeroized
	}

	if k.mechanism == MechanismRSAOAEP {
		wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, k.public, root, k.wrapAAD())
		if err != nil {
			return nil, err
		}

		return append([]byte{wrapVersion}, wrapped...), nil
	}

	params := pkcs11.NewGCMParams(randomBytes(nonceSize), k.wrapAAD(), 8*tagSize)
	defer params.Free()

	if err := k.ctx.EncryptInit(k.session, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_GCM, params)},
		k.key); err != nil {
		return nil, fmt.Errorf("%w: wrap the root key: %w", ErrPKCS11, err)
	}

	sealed, err := k.ctx.Encrypt(k.session, root)
	if err != nil {
		return nil, fmt.Errorf("%w: wrap the root key: %w", ErrPKCS11, err)
	}

	iv := params.IV()
	if len(iv) != nonceSize {
		return nil, fmt.Errorf("%w: the token used a %d-byte GCM nonce", ErrPKCS11, len(iv))
	}

	out := make([]byte, 0, 1+nonceSize+len(sealed))

	return append(append(append(out, wrapVersion), iv...), sealed...), nil
}

func (k *PKCS11Key) Unwrap(wrapped []byte) ([]byte, error) {
	if len(wrapped) < 1+nonceSize+tagSize || wrapped[0] != wrapVersion {
		return nil, fmt.Errorf("%w: bad root key wrap header", ErrMalformed)
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	if !k.open {
		return nil, ErrZeroized
	}

	var (
		mechanism  *pkcs11.Mechanism
		ciphertext []byte
	)

	if k.mechanism == MechanismRSAOAEP {
		params := pkcs11.NewOAEPParams(pkcs11.CKM_SHA256, pkcs11.CKG_MGF1_SHA256, pkcs11.CKZ_DATA_SPECIFIED,
			k.wrapAAD())
		mechanism = pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS_OAEP, params)
		ciphertext = wrapped[1:]
	} else {
		params := pkcs11.NewGCMParams(bytes.Clone(wrapped[1:1+nonceSize]), k.wrapAAD(), 8*tagSize)
		defer params.Free()

		mechanism = pkcs11.NewMechanism(pkcs11.CKM_AES_GCM, params)
		ciphertext = wrapped[1+nonceSize:]
	}

	if err := k.ctx.DecryptInit(k.session, []*pkcs11.Mechanism{mechanism}, k.key); err != nil {
		return nil, fmt.Errorf("%w: unwrap the root key: %w", ErrPKCS11, err)
	}

	root, err := k.ctx.Decrypt(k.session, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("%w: root key wrap: %w", ErrAuthentication, err)
	}

	return root, nil
}

func (k *PKCS11Key) Zeroize() {
	k.mu.Lock()
	defer k.mu.Unlock()

	if !k.open {
		return
	}

	_ = k.ctx.CloseSession(k.session)
	k.open = false
	k.public = nil
}
