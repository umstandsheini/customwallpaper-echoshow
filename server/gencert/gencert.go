// One-off tool: generates a leaf TLS certificate for the impersonated
// hostnames, signed by your own CA (ca.pem, containing both cert and
// private key, PEM-concatenated) which must already be installed in the
// Echo Show's system trust store. See ../../SETUP.md.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"os"
	"time"
)

// Add/remove hostnames here depending on which endpoints you want to
// hijack - see ../../PROTOCOL.md for the ones this project documents.
var hosts = []string{
	"d1cg7g7aedi1wy.cloudfront.net", // "Art" category CDN
	"thumbnails-photos.amazon.de",   // "Personal Photos" (Amazon Photos)
}

func loadCA(path string) (*x509.Certificate, *rsa.PrivateKey) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	var certBlock, keyBlock *pem.Block
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			certBlock = block
		case "RSA PRIVATE KEY":
			keyBlock = block
		}
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		log.Fatal(err)
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		log.Fatal(err)
	}
	return cert, key
}

func main() {
	caCert, caKey := loadCA("ca.pem")

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: hosts[0]},
		DNSNames:     hosts,
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		log.Fatal(err)
	}

	certOut, _ := os.Create("leaf-cert.pem")
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	certOut.Close()

	keyOut, _ := os.Create("leaf-key.pem")
	pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	keyOut.Close()

	log.Println("wrote leaf-cert.pem and leaf-key.pem for:", hosts)
}
