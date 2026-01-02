package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := "8081"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		response := fmt.Sprintf("Backend on port %s - Path: %s\n", port, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	})

	log.Printf("Test backend listening on:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
