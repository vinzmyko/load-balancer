package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
)

var requestCounter uint64

func main() {
	port := "8081"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddUint64(&requestCounter, 1)
		// Reply to the requesting client
		response := fmt.Sprintf("Backend port %s - Request #%d Path: %s\n", port, count, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	})

	log.Printf("Test backend listening on:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
