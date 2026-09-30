package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"net/http"
	"os"
	"time"
)

func main() {
	url := flag.String("url", "https://localhost:8443/readyz", "health URL")
	ca := flag.String("ca", "", "CA PEM file")
	flag.Parse()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if *ca != "" {
		pem, err := os.ReadFile(*ca)
		if err != nil {
			os.Exit(1)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			os.Exit(1)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	client := http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Get(*url)
	if err != nil {
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		os.Exit(1)
	}
}
