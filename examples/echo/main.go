// A local upstream for trying the gateway. Run with: go run ./examples/echo
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9000", "HTTP listen address")
	flag.Parse()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			Query  string `json:"query"`
		}{r.Method, r.URL.Path, r.URL.RawQuery})
	})
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("example upstream listening on %s", *listen)
	log.Fatal(server.ListenAndServe())
}
