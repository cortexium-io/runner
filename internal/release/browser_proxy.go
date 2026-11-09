package release

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/http"
	"time"
)

// TLS and forwarded-header handling are exercised on loopback with an ephemeral
// certificate. The browser accepts this certificate only for this isolated run.
func serveBrowserProxy(handler func(string) http.Handler) (string, func(), error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", nil, err
	}
	certificate := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &certificate, &certificate, &key.PublicKey, key)
	if err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	secure := tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	origin := "https://" + listener.Addr().String()
	server := &http.Server{Handler: handler(origin), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 15 * time.Second}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(secure) }()
	return origin, func() { _ = server.Close(); <-done }, nil
}
