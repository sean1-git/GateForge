package security

import (
	"crypto/tls"
	"errors"
	"sync"
	"time"
)

// TLSConfig checks certificates at startup and reloads files at most once a minute.
// Atomic replacement of renewed cert/key files makes renewal possible without restart.
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
