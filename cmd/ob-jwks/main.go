// ob-jwks reads a Revolut Open Banking sandbox keypair + signing
// cert and prints a JWKS suitable for hosting at the URL Revolut's
// developer portal asks for under "Setup JWKs endpoint".
//
// Default input directory is ~/.config/revolut-go/sandbox/openbanking
// containing private.key (PEM) and signing.der (DER X.509). The
// JWKS is written to stdout (or -out) so the caller can pipe it
// into a static-hosted file (GitHub Pages, S3, gist, etc.).
//
// Run after the portal's CSR-upload step:
//
//	go run ./cmd/ob-jwks -out docs/jwks.json
//	# enable GitHub Pages on /docs and paste the resulting URL
//	# into the "Setup JWKs endpoint" tile in the portal.
package main

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/greatliontech/revolut-go/openbanking"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ob-jwks:", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	defaultDir := filepath.Join(home, ".config", "revolut-go", "sandbox", "openbanking")

	dir := flag.String("dir", defaultDir, "directory containing private.key and signing.der")
	keyFile := flag.String("key", "private.key", "private key filename within -dir")
	certFile := flag.String("cert", "signing.der", "signing certificate filename within -dir")
	out := flag.String("out", "", "output path for the JWKS JSON (default: stdout)")
	alg := flag.String("alg", openbanking.AlgPS256, "JWS algorithm to advertise (PS256, PS384, PS512, RS256, RS384, RS512)")
	use := flag.String("use", "sig", "JWK 'use' value (typically 'sig')")
	kid := flag.String("kid", "", "explicit kid; default is the RFC 7638 thumbprint of the key")
	flag.Parse()

	keyPath := filepath.Join(*dir, *keyFile)
	certPath := filepath.Join(*dir, *certFile)

	priv, err := loadPrivateKey(keyPath)
	if err != nil {
		return fmt.Errorf("load key: %w", err)
	}
	certDER, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("read cert: %w", err)
	}
	// Sanity-check the cert parses; BuildJWKS also validates the
	// public-key match, but a stand-alone parse error here is
	// easier to diagnose than the downstream message.
	if _, err := x509.ParseCertificate(certDER); err != nil {
		return fmt.Errorf("parse cert: %w", err)
	}

	jwks, err := openbanking.BuildJWKS(openbanking.JWKEntry{
		Public:  &priv.PublicKey,
		CertDER: certDER,
		Use:     *use,
		Alg:     *alg,
		Kid:     *kid,
	})
	if err != nil {
		return err
	}

	if *out == "" {
		_, err = os.Stdout.Write(append(jwks, '\n'))
		return err
	}
	if err := os.WriteFile(*out, append(jwks, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
	return nil
}

// loadPrivateKey reads a PEM-encoded RSA private key. Accepts both
// PKCS#1 ("RSA PRIVATE KEY") and PKCS#8 ("PRIVATE KEY") blocks —
// openssl picks PKCS#8 by default in newer versions.
func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS#8 key is %T, want *rsa.PrivateKey", key)
		}
		return rsaKey, nil
	}
	return nil, fmt.Errorf("unsupported PEM block %q", block.Type)
}
