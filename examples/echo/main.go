// A local upstream for trying the gateway. Run with: go run ./examples/echo
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9000", "HTTP listen address")
	name := flag.String("name", "", "Optional service name shown in responses")
	cacheSeconds := flag.Int("cache-seconds", 0, "Mark demo GET responses public and cacheable for this many seconds")
	flag.Parse()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if *cacheSeconds > 0 && r.Method == http.MethodGet {
			w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(*cacheSeconds))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Service string `json:"service,omitempty"`
			Method  string `json:"method"`
			Path    string `json:"path"`
			Query   string `json:"query"`
		}{*name, r.Method, r.URL.Path, r.URL.RawQuery})
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
