// Command fakeserver is a throwaway local HTTP server standing in for a
// real manifestServerUrl during manual Stage/Rigger testing
// (docs/REQUIREMENTS.md §5-6). It serves static files — manifest.json, and
// later a JRE archive for on-demand provisioning testing — from a
// directory, logging every request so you can watch Rigger's
// manifest-refresh fetches happen live.
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	dir := flag.String("dir", "public", "directory to serve")
	flag.Parse()

	fileServer := http.FileServer(http.Dir(*dir))
	logged := func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		fileServer.ServeHTTP(w, r)
	}

	log.Printf("fakeserver: serving %s on http://%s", *dir, *addr)
	if err := http.ListenAndServe(*addr, http.HandlerFunc(logged)); err != nil {
		log.Fatal(err)
	}
}
