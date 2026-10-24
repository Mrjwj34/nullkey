package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthorityPersistsAndMuxesHTTPAndHTTPS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	authority, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if authority.CAPath == "" {
		t.Fatal("missing CA path")
	}
	again, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if string(authority.CAPEM) != string(again.CAPEM) {
		t.Fatal("CA was regenerated")
	}
	info, err := os.Stat(filepath.Join(dir, caKeyFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("CA key mode = %v, %v", info.Mode().Perm(), err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			_, _ = io.WriteString(w, "plain")
			return
		}
		_, _ = io.WriteString(w, "tls")
	})}
	go func() { _ = server.Serve(authority.MuxListener(listener)) }()
	t.Cleanup(func() { _ = server.Close() })

	plain, err := http.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("plain GET: %v", err)
	}
	body, _ := io.ReadAll(plain.Body)
	plain.Body.Close()
	if string(body) != "plain" {
		t.Fatalf("plain body = %q", body)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(authority.CAPEM) {
		t.Fatal("CA PEM did not parse")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	secure, err := client.Get("https://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("TLS GET: %v", err)
	}
	body, _ = io.ReadAll(secure.Body)
	secure.Body.Close()
	if string(body) != "tls" {
		t.Fatalf("TLS body = %q", body)
	}
}

func TestAuthorityRegeneratesCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, caCertFile), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	authority, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if len(authority.CAPEM) == 0 {
		t.Fatal("empty CA")
	}
	if _, err := x509.ParseCertificate(authority.cert.Certificate[0]); err != nil {
		t.Fatalf("leaf: %v", err)
	}
	if time.Now().After(authority.cert.Leaf.NotAfter) {
		t.Fatal("leaf already expired")
	}
}
