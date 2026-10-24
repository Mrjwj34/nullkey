// Package tlsutil creates the local certificate authority used by the
// sandbox-facing data plane. It never installs the CA system-wide: Codex is
// pointed at the CA file with CODEX_CA_CERTIFICATE, so trust stays scoped to
// the sandbox process.
package tlsutil

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	caCertFile     = "ca.pem"
	caKeyFile      = "ca-key.pem"
	serverCertFile = "server.pem"
	serverKeyFile  = "server-key.pem"
	caValidity     = 10 * 365 * 24 * time.Hour
	leafValidity   = 2 * 365 * 24 * time.Hour
)

// Authority is the daemon-local CA and server certificate.
type Authority struct {
	CAPath string
	CAPEM  []byte
	cert   tls.Certificate
}

// LoadOrCreate loads a persisted CA/leaf pair from dir or creates one. The CA
// is stable across daemon restarts so existing sandbox config files keep
// working. A corrupt or expired pair is replaced atomically.
func LoadOrCreate(dir string) (*Authority, error) {
	if dir == "" {
		return nil, errors.New("tlsutil: empty directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("tlsutil: create directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("tlsutil: secure directory: %w", err)
	}
	if authority, err := load(dir); err == nil {
		return authority, nil
	}
	return generate(dir)
}

func load(dir string) (*Authority, error) {
	caPEM, err := os.ReadFile(filepath.Join(dir, caCertFile))
	if err != nil {
		return nil, err
	}
	caKeyPEM, err := os.ReadFile(filepath.Join(dir, caKeyFile))
	if err != nil {
		return nil, err
	}
	serverCertPEM, err := os.ReadFile(filepath.Join(dir, serverCertFile))
	if err != nil {
		return nil, err
	}
	serverKeyPEM, err := os.ReadFile(filepath.Join(dir, serverKeyFile))
	if err != nil {
		return nil, err
	}
	caCert, caKey, err := parseCA(caPEM, caKeyPEM)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	if time.Now().After(leaf.NotAfter) || time.Now().After(caCert.NotAfter) {
		return nil, errors.New("tlsutil: certificate expired")
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1"}); err != nil {
		return nil, err
	}
	_ = caKey
	cert.Leaf = leaf
	return &Authority{CAPath: filepath.Join(dir, caCertFile), CAPEM: caPEM, cert: cert}, nil
}

func generate(dir string) (*Authority, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: generate CA key: %w", err)
	}
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: "Nullkey Local CA", Organization: []string{"Nullkey"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: create CA: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: parse CA: %w", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caKeyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: marshal CA key: %w", err)
	}
	caKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER})

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: generate server key: %w", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: "nullkeyd gateway", Organization: []string{"Nullkey"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: create server certificate: %w", err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: marshal server key: %w", err)
	}
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{caCertFile, caPEM, 0o644},
		{caKeyFile, caKeyPEM, 0o600},
		{serverCertFile, leafPEM, 0o644},
		{serverKeyFile, leafKeyPEM, 0o600},
	}
	for _, file := range files {
		if err := writeAtomic(filepath.Join(dir, file.name), file.data, file.mode); err != nil {
			return nil, err
		}
	}
	cert, err := tls.X509KeyPair(leafPEM, leafKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: load server key pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("tlsutil: parse server certificate: %w", err)
	}
	cert.Leaf = leaf
	return &Authority{CAPath: filepath.Join(dir, caCertFile), CAPEM: caPEM, cert: cert}, nil
}

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, errors.New("tlsutil: invalid CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("tlsutil: invalid CA key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if !cert.IsCA {
		return nil, nil, errors.New("tlsutil: certificate is not a CA")
	}
	return cert, key, nil
}

func randomSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return serial
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tls-*")
	if err != nil {
		return fmt.Errorf("tlsutil: create temp file: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tlsutil: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tlsutil: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tlsutil: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tlsutil: close temp file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("tlsutil: install %s: %w", path, err)
	}
	return nil
}

// TLSConfig returns a server config restricted to the local certificate.
func (a *Authority) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{a.cert},
		NextProtos:   []string{"http/1.1"},
	}
}

// MuxListener accepts both plaintext HTTP and TLS on one loopback port. It
// sniffs only the first byte, so existing http:// sandbox clients keep working
// while Codex can use https:// with the local CA.
func (a *Authority) MuxListener(inner net.Listener) net.Listener {
	return &muxListener{Listener: inner, config: a.TLSConfig()}
}

type muxListener struct {
	net.Listener
	config *tls.Config
}

func (l *muxListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	first, err := reader.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	buffered := &bufferedConn{Conn: conn, reader: reader}
	if first[0] == 0x16 { // TLS handshake record
		return tls.Server(buffered, l.config), nil
	}
	return buffered, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
