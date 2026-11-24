package softhsmtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/miekg/pkcs11"
)

const (
	EnvLibrary = "SOFTHSM2_LIB"
	TokenLabel = "ella-smsc-test"
	userPIN    = "1234"
	soPIN      = "123456"
)

var defaultLibraries = []string{
	"/usr/lib/softhsm/libsofthsm2.so",
	"/usr/lib/x86_64-linux-gnu/softhsm/libsofthsm2.so",
	"/usr/lib/aarch64-linux-gnu/softhsm/libsofthsm2.so",
	"/usr/local/lib/softhsm/libsofthsm2.so",
}

type Token struct {
	Library string
	Label   string
	PINFile string

	ctx  *pkcs11.Ctx
	slot uint
	mu   sync.Mutex
	keys map[string]bool
}

var (
	once    sync.Once
	token   *Token
	dir     string
	openErr error
)

func Open(t testing.TB) *Token {
	t.Helper()

	library, ok := findLibrary(t)
	if !ok {
		t.Skip("SoftHSM2 is not installed: set " + EnvLibrary)
	}

	once.Do(func() { token, openErr = setUp(library) })

	if openErr != nil {
		t.Fatalf("set up SoftHSM2: %v", openErr)
	}

	return token
}

func Remove() {
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

func findLibrary(t testing.TB) (string, bool) {
	if path := os.Getenv(EnvLibrary); path != "" {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", EnvLibrary, err)
		}

		return path, true
	}

	for _, path := range defaultLibraries {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}

	return "", false
}

func setUp(library string) (*Token, error) {
	var err error

	if dir, err = os.MkdirTemp("", "softhsm-"); err != nil {
		return nil, err
	}

	tokens := filepath.Join(dir, "tokens")
	if err := os.Mkdir(tokens, 0o700); err != nil {
		return nil, err
	}

	conf := filepath.Join(dir, "softhsm2.conf")
	if err := os.WriteFile(conf, []byte("directories.tokendir = "+tokens+"\nobjectstore.backend = file\n"+
		"log.level = ERROR\n"), 0o600); err != nil {
		return nil, err
	}

	if err := os.Setenv("SOFTHSM2_CONF", conf); err != nil {
		return nil, err
	}

	pinFile := filepath.Join(dir, "pin")
	if err := os.WriteFile(pinFile, []byte(userPIN+"\n"), 0o600); err != nil {
		return nil, err
	}

	ctx := pkcs11.New(library)
	if ctx == nil {
		return nil, fmt.Errorf("load %s", library)
	}

	if err := ctx.Initialize(); err != nil {
		return nil, err
	}

	slot, err := initToken(ctx)
	if err != nil {
		return nil, err
	}

	return &Token{Library: library, Label: TokenLabel, PINFile: pinFile, ctx: ctx, slot: slot, keys: map[string]bool{}},
		nil
}

func initToken(ctx *pkcs11.Ctx) (uint, error) {
	slots, err := ctx.GetSlotList(true)
	if err != nil || len(slots) == 0 {
		return 0, fmt.Errorf("no SoftHSM2 slot: %w", err)
	}

	if err := ctx.InitToken(slots[0], soPIN, TokenLabel); err != nil {
		return 0, fmt.Errorf("initialize the token: %w", err)
	}

	slots, err = ctx.GetSlotList(true)
	if err != nil {
		return 0, err
	}

	for _, slot := range slots {
		info, err := ctx.GetTokenInfo(slot)
		if err != nil || info.Label != TokenLabel {
			continue
		}

		session, err := ctx.OpenSession(slot, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
		if err != nil {
			return 0, err
		}

		defer func() { _ = ctx.CloseSession(session) }()

		if err := ctx.Login(session, pkcs11.CKU_SO, soPIN); err != nil {
			return 0, err
		}

		if err := ctx.InitPIN(session, userPIN); err != nil {
			return 0, err
		}

		return slot, ctx.Logout(session)
	}

	return 0, fmt.Errorf("the token %s is missing after its initialization", TokenLabel)
}

func (tok *Token) AES(t testing.TB, label string, bytes int) {
	t.Helper()

	tok.generate(t, label, func(ctx *pkcs11.Ctx, session pkcs11.SessionHandle) error {
		_, err := ctx.GenerateKey(session, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_KEY_GEN, nil)},
			[]*pkcs11.Attribute{
				pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_SECRET_KEY),
				pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_AES),
				pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
				pkcs11.NewAttribute(pkcs11.CKA_PRIVATE, true),
				pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, true),
				pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, false),
				pkcs11.NewAttribute(pkcs11.CKA_ENCRYPT, true),
				pkcs11.NewAttribute(pkcs11.CKA_DECRYPT, true),
				pkcs11.NewAttribute(pkcs11.CKA_VALUE_LEN, bytes),
				pkcs11.NewAttribute(pkcs11.CKA_LABEL, label),
			})

		return err
	})
}

func (tok *Token) RSA(t testing.TB, label string, bits int) {
	t.Helper()

	tok.generate(t, label, func(ctx *pkcs11.Ctx, session pkcs11.SessionHandle) error {
		_, _, err := ctx.GenerateKeyPair(session,
			[]*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS_KEY_PAIR_GEN, nil)},
			[]*pkcs11.Attribute{
				pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
				pkcs11.NewAttribute(pkcs11.CKA_ENCRYPT, true),
				pkcs11.NewAttribute(pkcs11.CKA_MODULUS_BITS, bits),
				pkcs11.NewAttribute(pkcs11.CKA_PUBLIC_EXPONENT, []byte{1, 0, 1}),
				pkcs11.NewAttribute(pkcs11.CKA_LABEL, label),
			},
			[]*pkcs11.Attribute{
				pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true),
				pkcs11.NewAttribute(pkcs11.CKA_PRIVATE, true),
				pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, true),
				pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, false),
				pkcs11.NewAttribute(pkcs11.CKA_DECRYPT, true),
				pkcs11.NewAttribute(pkcs11.CKA_LABEL, label),
			})

		return err
	})
}

func (tok *Token) generate(t testing.TB, label string, gen func(*pkcs11.Ctx, pkcs11.SessionHandle) error) {
	t.Helper()

	tok.mu.Lock()
	defer tok.mu.Unlock()

	if tok.keys[label] {
		return
	}

	session, err := tok.ctx.OpenSession(tok.slot, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
	if err != nil {
		t.Fatalf("open a SoftHSM2 session: %v", err)
	}

	defer func() { _ = tok.ctx.CloseSession(session) }()

	if err := tok.ctx.Login(session, pkcs11.CKU_USER, userPIN); err != nil &&
		err != pkcs11.Error(pkcs11.CKR_USER_ALREADY_LOGGED_IN) {
		t.Fatalf("log in to SoftHSM2: %v", err)
	}

	if err := gen(tok.ctx, session); err != nil {
		t.Fatalf("generate the key %q: %v", label, err)
	}

	tok.keys[label] = true
}

func (tok *Token) WrongPINFile(t testing.TB) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "pin")
	if err := os.WriteFile(path, []byte("0000\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}
