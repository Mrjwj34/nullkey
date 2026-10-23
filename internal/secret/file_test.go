package secret

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// openFileForTest forces the file backend on every OS.
func openFileForTest(t *testing.T, home string) Store {
	t.Helper()
	t.Setenv(BackendEnv, "file")
	store, err := Open(home, testLogger())
	if err != nil {
		t.Fatalf("Open file backend: %v", err)
	}
	if _, ok := store.(*fileStore); !ok {
		t.Fatalf("Open returned %T, want *fileStore", store)
	}
	return store
}

// failingReader simulates an entropy source failure.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy source failed")
}

func secretsDir(home string) string {
	return filepath.Join(home, secretsDirName)
}

func encryptedFiles(t *testing.T, home string) []string {
	t.Helper()
	entries, err := os.ReadDir(secretsDir(home))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var paths []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), secretFileSuffix) {
			paths = append(paths, filepath.Join(secretsDir(home), entry.Name()))
		}
	}
	return paths
}

func TestFileBackendRoundTripPersistence(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	store := openFileForTest(t, home)
	const account = "cred-roundtrip"
	value := []byte("sk-test-file-backend-0123456789abcdef")

	if err := store.Put(ctx, account, value); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, account)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("Get = %q, want %q", got, value)
	}

	// No plaintext may appear in any file under secrets/.
	entries, err := os.ReadDir(secretsDir(home))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(secretsDir(home), entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", entry.Name(), err)
		}
		if bytes.Contains(data, value) {
			t.Fatalf("plaintext secret found in %s", entry.Name())
		}
		if strings.HasPrefix(entry.Name(), ".secret-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}

	// Key and secret files are owner-only; the directory is owner-only too.
	keyPath := filepath.Join(secretsDir(home), masterKeyFileName)
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat master key: %v", err)
	}
	if perm := keyInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("master key mode = %04o, want 0600", perm)
	}
	dirInfo, err := os.Stat(secretsDir(home))
	if err != nil {
		t.Fatalf("Stat secrets dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("secrets dir mode = %04o, want 0700", perm)
	}
	files := encryptedFiles(t, home)
	if len(files) != 1 {
		t.Fatalf("encrypted files = %d, want 1", len(files))
	}
	secretInfo, err := os.Stat(files[0])
	if err != nil {
		t.Fatalf("Stat secret file: %v", err)
	}
	if perm := secretInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("secret file mode = %04o, want 0600", perm)
	}

	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("Read master key: %v", err)
	}

	// Reopening the store reuses the master key and reads persisted data.
	reopened := openFileForTest(t, home)
	got, err = reopened.Get(ctx, account)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("Get after reopen = %q, want %q", got, value)
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("Read master key after reopen: %v", err)
	}
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("master key changed across reopen")
	}

	// Overwrite persists too.
	replacement := []byte("sk-test-replacement-value")
	if err := reopened.Put(ctx, account, replacement); err != nil {
		t.Fatalf("overwrite Put: %v", err)
	}
	got, err = reopened.Get(ctx, account)
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if !bytes.Equal(got, replacement) {
		t.Fatalf("Get after overwrite = %q, want %q", got, replacement)
	}

	// Delete is idempotent and survives reopen.
	if err := reopened.Delete(ctx, account); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := reopened.Delete(ctx, account); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
	if _, err := reopened.Get(ctx, account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete error = %v, want ErrNotFound", err)
	}
	afterDelete := openFileForTest(t, home)
	if _, err := afterDelete.Get(ctx, account); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete/reopen error = %v, want ErrNotFound", err)
	}
}

func TestFileBackendWrongKeyFails(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	store := openFileForTest(t, home)
	const account = "cred-wrong-key"
	value := []byte("sk-test-wrong-key-value")
	if err := store.Put(ctx, account, value); err != nil {
		t.Fatalf("Put: %v", err)
	}

	keyPath := filepath.Join(secretsDir(home), masterKeyFileName)
	wrongKey := make([]byte, masterKeySize)
	if _, err := rand.Read(wrongKey); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.WriteFile(keyPath, wrongKey, 0o600); err != nil {
		t.Fatalf("overwrite master key: %v", err)
	}

	reopened := openFileForTest(t, home)
	got, err := reopened.Get(ctx, account)
	if err == nil {
		t.Fatalf("Get with wrong key succeeded and returned %q", got)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("Get with wrong key = %v, want a decryption error", err)
	}
	if bytes.Contains([]byte(err.Error()), value) {
		t.Fatalf("error leaked the secret value: %v", err)
	}
}

func TestFileBackendAccountBindingAndCorruption(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	store := openFileForTest(t, home)
	fs := store.(*fileStore)

	if err := store.Put(ctx, "cred-a", []byte("sk-test-a")); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := store.Put(ctx, "cred-b", []byte("sk-test-b")); err != nil {
		t.Fatalf("Put b: %v", err)
	}

	// Ciphertexts are bound to the account via AAD; swapping files fails.
	bCiphertext, err := os.ReadFile(fs.path("cred-b"))
	if err != nil {
		t.Fatalf("ReadFile b: %v", err)
	}
	if err := os.WriteFile(fs.path("cred-a"), bCiphertext, 0o600); err != nil {
		t.Fatalf("WriteFile a: %v", err)
	}
	if _, err := store.Get(ctx, "cred-a"); err == nil {
		t.Fatal("Get accepted a ciphertext from another account")
	}

	if err := store.Put(ctx, "cred-a", []byte("sk-test-a")); err != nil {
		t.Fatalf("Put a again: %v", err)
	}
	if err := os.WriteFile(fs.path("cred-a"), []byte("garbage"), 0o600); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	if _, err := store.Get(ctx, "cred-a"); err == nil {
		t.Fatal("Get accepted an unknown file format")
	}
	if err := os.WriteFile(fs.path("cred-a"), []byte(secretFileMagic), 0o600); err != nil {
		t.Fatalf("truncate file: %v", err)
	}
	if _, err := store.Get(ctx, "cred-a"); err == nil {
		t.Fatal("Get accepted a truncated file")
	}

	// Long enough to pass the truncation check but with the wrong magic.
	wrongMagic := make([]byte, len(secretFileMagic)+nonceSize+1)
	if err := os.WriteFile(fs.path("cred-a"), wrongMagic, 0o600); err != nil {
		t.Fatalf("write wrong magic: %v", err)
	}
	if _, err := store.Get(ctx, "cred-a"); err == nil {
		t.Fatal("Get accepted an unknown file format")
	}
}

func TestFileBackendHashedFilenames(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	store := openFileForTest(t, home)

	accounts := []string{"../../escape", "a/b/c"}
	for _, account := range accounts {
		if err := store.Put(ctx, account, []byte("sk-test-value")); err != nil {
			t.Fatalf("Put(%q): %v", account, err)
		}
		got, err := store.Get(ctx, account)
		if err != nil {
			t.Fatalf("Get(%q): %v", account, err)
		}
		if string(got) != "sk-test-value" {
			t.Fatalf("Get(%q) = %q", account, got)
		}
	}
	files := encryptedFiles(t, home)
	if len(files) != len(accounts) {
		t.Fatalf("encrypted files = %d, want %d", len(files), len(accounts))
	}
	for _, path := range files {
		if filepath.Dir(path) != secretsDir(home) {
			t.Fatalf("secret escaped secrets dir: %s", path)
		}
		if len(filepath.Base(path)) != 64+len(secretFileSuffix) {
			t.Fatalf("unexpected hashed filename: %s", path)
		}
	}
}

func TestFileBackendEmptyValue(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	store := openFileForTest(t, home)
	if err := store.Put(ctx, "cred-empty", nil); err != nil {
		t.Fatalf("Put nil: %v", err)
	}
	got, err := store.Get(ctx, "cred-empty")
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Get empty = %q, want empty", got)
	}
}

func TestFileBackendInvalidAccountAndContext(t *testing.T) {
	home := t.TempDir()
	store := openFileForTest(t, home)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(canceled, "cred", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put canceled error = %v", err)
	}
	if _, err := store.Get(canceled, "cred"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get canceled error = %v", err)
	}
	if err := store.Delete(canceled, "cred"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete canceled error = %v", err)
	}

	ctx := context.Background()
	for _, account := range []string{"", "a\x00b", string([]byte{0xff})} {
		if err := store.Put(ctx, account, []byte("x")); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("Put(%q) error = %v", account, err)
		}
		if _, err := store.Get(ctx, account); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("Get(%q) error = %v", account, err)
		}
		if err := store.Delete(ctx, account); !errors.Is(err, ErrInvalidAccount) {
			t.Fatalf("Delete(%q) error = %v", account, err)
		}
	}
}

func TestFileBackendMasterKeyFailClosed(t *testing.T) {
	t.Run("permissions too open", func(t *testing.T) {
		home := t.TempDir()
		dir := secretsDir(home)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		key := make([]byte, masterKeySize)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("rand: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, masterKeyFileName), key, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Chmod(filepath.Join(dir, masterKeyFileName), 0o644); err != nil {
			t.Fatalf("Chmod: %v", err)
		}
		t.Setenv(BackendEnv, "file")
		if _, err := Open(home, testLogger()); err == nil {
			t.Fatal("Open accepted a world-readable master key")
		}
	})

	t.Run("wrong size", func(t *testing.T) {
		home := t.TempDir()
		dir := secretsDir(home)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, masterKeyFileName), []byte("short"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Setenv(BackendEnv, "file")
		if _, err := Open(home, testLogger()); err == nil {
			t.Fatal("Open accepted a short master key")
		}
	})

	t.Run("not a regular file", func(t *testing.T) {
		home := t.TempDir()
		dir := secretsDir(home)
		if err := os.MkdirAll(filepath.Join(dir, masterKeyFileName), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		t.Setenv(BackendEnv, "file")
		if _, err := Open(home, testLogger()); err == nil {
			t.Fatal("Open accepted a master key directory")
		}
	})

	t.Run("secrets dir is a symlink", func(t *testing.T) {
		home := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, secretsDir(home)); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		t.Setenv(BackendEnv, "file")
		if _, err := Open(home, testLogger()); err == nil {
			t.Fatal("Open followed a symlinked secrets directory")
		}
	})
}

func TestFileBackendTightensDirectoryPermissions(t *testing.T) {
	home := t.TempDir()
	dir := secretsDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	_ = openFileForTest(t, home)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("secrets dir mode = %04o, want 0700", perm)
	}
}

func TestFileBackendWriteAndDeleteErrors(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	store := openFileForTest(t, home)
	fs := store.(*fileStore)

	// A directory at the target path makes rename fail.
	const blocked = "cred-blocked"
	if err := os.Mkdir(fs.path(blocked), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := store.Put(ctx, blocked, []byte("x")); err == nil {
		t.Fatal("Put succeeded over a directory")
	}
	entries, err := os.ReadDir(secretsDir(home))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".secret-") {
			t.Fatalf("temporary file left after failed Put: %s", entry.Name())
		}
	}

	// Reading a directory as the secret file is a non-ENOENT read error.
	if _, err := store.Get(ctx, blocked); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(directory) error = %v, want a read error", err)
	}

	// A non-empty directory cannot be removed, exercising the Delete error path.
	nonEmpty := fs.path("cred-nonempty")
	if err := os.Mkdir(nonEmpty, 0o700); err != nil {
		t.Fatalf("Mkdir non-empty: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmpty, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile child: %v", err)
	}
	if err := store.Delete(ctx, "cred-nonempty"); err == nil {
		t.Fatal("Delete succeeded on a non-empty directory")
	}
}

func TestFileBackendOpenRejectsFileAsHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(home, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv(BackendEnv, "file")
	if _, err := Open(home, testLogger()); err == nil {
		t.Fatal("Open accepted a regular file as home")
	}
}

func TestFileBackendEntropyFailures(t *testing.T) {
	oldReader := rand.Reader
	t.Cleanup(func() { rand.Reader = oldReader })

	t.Setenv(BackendEnv, "file")
	rand.Reader = failingReader{}
	if _, err := Open(t.TempDir(), testLogger()); err == nil {
		t.Fatal("Open succeeded without entropy")
	}

	home := t.TempDir()
	rand.Reader = oldReader
	store := openFileForTest(t, home)
	rand.Reader = failingReader{}
	if err := store.Put(context.Background(), "cred", []byte("x")); err == nil {
		t.Fatal("Put succeeded without nonce entropy")
	}
	rand.Reader = oldReader
}

func TestCreateMasterKeyRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), masterKeyFileName)
	first, err := createMasterKey(path)
	if err != nil {
		t.Fatalf("createMasterKey: %v", err)
	}
	if _, err := createMasterKey(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second createMasterKey error = %v, want os.ErrExist", err)
	}
	second, err := readMasterKey(path)
	if err != nil {
		t.Fatalf("readMasterKey: %v", err)
	}
	if first != second {
		t.Fatal("existing master key was overwritten")
	}
}

func TestFileBackendWriteTempFailure(t *testing.T) {
	home := t.TempDir()
	store := openFileForTest(t, home)
	fs := store.(*fileStore)
	fs.dir = filepath.Join(t.TempDir(), "missing")
	if err := store.Put(context.Background(), "cred", []byte("x")); err == nil {
		t.Fatal("Put succeeded without a secrets directory")
	}
}

func TestSyncDirBestEffortMissingDirectory(t *testing.T) {
	// Must not panic or block when the directory is already gone.
	syncDirBestEffort(filepath.Join(t.TempDir(), "missing"))
}

func TestFileBackendUnreadableMasterKey(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-0000 file")
	}
	home := t.TempDir()
	dir := secretsDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	key := make([]byte, masterKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	keyPath := filepath.Join(dir, masterKeyFileName)
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(keyPath, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Setenv(BackendEnv, "file")
	if _, err := Open(home, testLogger()); err == nil {
		t.Fatal("Open succeeded with an unreadable master key")
	}
}

func TestFileBackendConcurrent(t *testing.T) {
	home := t.TempDir()
	store := openFileForTest(t, home)
	ctx := context.Background()

	const goroutines = 16
	const iterations = 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			account := fmt.Sprintf("cred-concurrent-%d", g)
			for i := 0; i < iterations; i++ {
				value := []byte(fmt.Sprintf("value-%d-%d", g, i))
				if err := store.Put(ctx, account, value); err != nil {
					errs <- fmt.Errorf("Put: %w", err)
					return
				}
				got, err := store.Get(ctx, account)
				if err != nil {
					errs <- fmt.Errorf("Get: %w", err)
					return
				}
				if !bytes.Equal(got, value) {
					errs <- fmt.Errorf("Get = %q, want %q", got, value)
					return
				}
				if i%3 == 0 {
					if err := store.Delete(ctx, account); err != nil {
						errs <- fmt.Errorf("Delete: %w", err)
						return
					}
				}
			}
			if err := store.Delete(ctx, account); err != nil {
				errs <- fmt.Errorf("final Delete: %w", err)
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
