package security

import (
	"crypto/tls"
	"errors"
	"sync"
	"time"
)

// TLSConfig supports renewal without restarting the gateway. Reload checks are
// throttled to avoid disk I/O on every handshake; a failed reload keeps the last
// valid pair so a partial cert/key replacement does not interrupt new connections.
func TLSConfig(certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("both TLS certificate and private key files are required")
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	checked := time.Now()
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(checked) > time.Minute {
			if renewed, err := tls.LoadX509KeyPair(certFile, keyFile); err == nil {
				pair = renewed
			}
			checked = time.Now()
		}
		return &pair, nil
	}}, nil
}
