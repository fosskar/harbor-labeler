// Command gencert emits a self-signed TLS certificate for the e2e TLS
// stage (e2e/run.sh). It exists so the suite needs no openssl: the Go
// toolchain is already in the devshell. The certificate is its own CA —
// clients trust tls.crt directly.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	ip := flag.String("ip", "", "IP address the certificate is valid for")
	dir := flag.String("dir", ".", "directory to write tls.crt and tls.key into")
	flag.Parse()

	addr := net.ParseIP(*ip)
	if addr == nil {
		log.Fatalf("-ip %q is not a valid IP address", *ip)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("generating key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "harbor-labeler-e2e-tls"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{addr},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		log.Fatalf("creating certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatalf("marshaling key: %v", err)
	}

	writePEM := func(name, blockType string, b []byte) {
		path := filepath.Join(*dir, name)
		data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: b})
		if err := os.WriteFile(path, data, 0o600); err != nil {
			log.Fatalf("writing %s: %v", path, err)
		}
	}
	writePEM("tls.crt", "CERTIFICATE", der)
	writePEM("tls.key", "EC PRIVATE KEY", keyDER)
}
