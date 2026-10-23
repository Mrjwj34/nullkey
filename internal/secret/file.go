package secret

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	secretsDirName    = "secrets"
	masterKeyFileName = "master.key"
	masterKeySize     = 32
	nonceSize         = 12
	secretFileSuffix  = ".enc"
	// secretFileMagic versions the on-disk format. Changing it must be paired
	// with a migration because old files become unreadable.
	secretFileMagic = "NKSEC1\x00"
)

// aad binds a ciphertext to its account so files cannot be swapped between
// accounts even though the file names are hashes.
func aad(account string) []byte {
	return []byte("nullkey.secret.v1:" + account)
}

// fileStore is the encrypted-file backend used on non-darwin platforms and when
// NULLKEY_SECRET_BACKEND=file is set. It is CI-only; macOS production uses the
// Keychain.
type fileStore struct {
	dir  string
	aead cipher.AEAD
}

func (s *fileStore) path(account string) string {
	sum := sha256.Sum256([]byte(account))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+secretFileSuffix)
}

// openFileStore creates <home>/secrets with mode 0700 and loads (or atomically
// creates) the random master key. It fails closed if the key or directory
// permissions are unsafe.
func openFileStore(home string) (Store, error) {
	if home == "" {
		return nil, errors.New("secret: empty home directory")
	}
	dir := filepath.Join(home, secretsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secret: create secrets directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("secret: inspect secrets directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("secret: %s is not a directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secret: secure secrets directory: %w", err)
	}
	key, err := loadOrCreateMasterKey(dir)
	if err != nil {
		return nil, err
	}
	return newFileStore(dir, key)
}

// newFileStore constructs the AEAD once. The key is always 32 bytes, so any
// construction error would indicate a programming or platform fault; fail
// closed rather than continuing without encryption.
func newFileStore(dir string, key [masterKeySize]byte) (*fileStore, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("secret: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: gcm: %w", err)
	}
	return &fileStore{dir: dir, aead: aead}, nil
}

// loadOrCreateMasterKey uses O_EXCL so two first-time writers cannot clobber
// each other's key. The loser of the race reads the winner's key.
func loadOrCreateMasterKey(dir string) ([masterKeySize]byte, error) {
	var zero [masterKeySize]byte
	path := filepath.Join(dir, masterKeyFileName)
	key, err := readMasterKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}
	key, err = createMasterKey(path)
	if errors.Is(err, os.ErrExist) {
		return readMasterKey(path)
	}
	if err != nil {
		return zero, err
	}
	return key, nil
}

func readMasterKey(path string) ([masterKeySize]byte, error) {
	var key [masterKeySize]byte
	info, err := os.Lstat(path)
	if err != nil {
		return key, err
	}
	if !info.Mode().IsRegular() {
		return key, fmt.Errorf("secret: master key %s is not a regular file", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return key, fmt.Errorf("secret: master key permissions %04o are too open (want 0600)", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return key, fmt.Errorf("secret: read master key: %w", err)
	}
	if len(data) != masterKeySize {
		return key, fmt.Errorf("secret: master key must be %d bytes, got %d", masterKeySize, len(data))
	}
	copy(key[:], data)
	return key, nil
}

func createMasterKey(path string) ([masterKeySize]byte, error) {
	var key [masterKeySize]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return key, fmt.Errorf("secret: generate master key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return key, fmt.Errorf("secret: create master key: %w", err)
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(path)
	}
	if err := f.Chmod(0o600); err != nil {
		cleanup()
		return key, fmt.Errorf("secret: secure master key: %w", err)
	}
	if _, err := f.Write(key[:]); err != nil {
		cleanup()
		return key, fmt.Errorf("secret: write master key: %w", err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return key, fmt.Errorf("secret: sync master key: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return key, fmt.Errorf("secret: close master key: %w", err)
	}
	return key, nil
}

func (s *fileStore) Put(ctx context.Context, account string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAccount(account); err != nil {
		return err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("secret: nonce: %w", err)
	}
	payload := make([]byte, 0, len(secretFileMagic)+len(nonce)+len(value)+s.aead.Overhead())
	payload = append(payload, secretFileMagic...)
	payload = append(payload, nonce...)
	payload = s.aead.Seal(payload, nonce, value, aad(account))
	return s.writeAtomic(account, payload)
}

func (s *fileStore) writeAtomic(account string, payload []byte) (err error) {
	tmp, err := os.CreateTemp(s.dir, ".secret-*")
	if err != nil {
		return fmt.Errorf("secret: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	// os.CreateTemp already creates the file with mode 0600.
	if _, err := tmp.Write(payload); err != nil {
		return fmt.Errorf("secret: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("secret: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("secret: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path(account)); err != nil {
		return fmt.Errorf("secret: install secret file: %w", err)
	}
	syncDirBestEffort(s.dir)
	return nil
}

// syncDirBestEffort makes the rename durable where the platform supports
// fsync on directories. macOS may reject it with EINVAL, which is harmless:
// the file contents themselves are already synced.
func syncDirBestEffort(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func (s *fileStore) Get(ctx context.Context, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateAccount(account); err != nil {
		return nil, err
	}
	payload, err := os.ReadFile(s.path(account))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: account not found", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("secret: read secret file: %w", err)
	}
	if len(payload) < len(secretFileMagic)+nonceSize {
		return nil, errors.New("secret: encrypted file is truncated")
	}
	if !bytes.Equal(payload[:len(secretFileMagic)], []byte(secretFileMagic)) {
		return nil, errors.New("secret: encrypted file has an unknown format")
	}
	nonce := payload[len(secretFileMagic) : len(secretFileMagic)+nonceSize]
	ciphertext := payload[len(secretFileMagic)+nonceSize:]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, aad(account))
	if err != nil {
		return nil, fmt.Errorf("secret: decrypt secret file: %w", err)
	}
	return plaintext, nil
}

// Delete is idempotent: removing an absent account returns nil so credential
// revocation can be retried safely.
func (s *fileStore) Delete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAccount(account); err != nil {
		return err
	}
	if err := os.Remove(s.path(account)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("secret: delete secret file: %w", err)
	}
	return nil
}

var (
	_ Store = (*memoryStore)(nil)
	_ Store = (*fileStore)(nil)
)
