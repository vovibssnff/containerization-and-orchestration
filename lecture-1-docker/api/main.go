package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
)

var (
	mu    sync.Mutex
	eaten [][]byte
)

func health(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintln(w, "ok")
}

func eat(w http.ResponseWriter, r *http.Request) {
	mb, err := strconv.Atoi(r.URL.Query().Get("mb"))
	if err != nil || mb <= 0 {
		http.Error(w, "mb must be a positive integer", http.StatusBadRequest)
		return
	}

	// Touch every page so the memory is resident and charged to the cgroup.
	chunk := make([]byte, mb<<20)
	for i := 0; i < len(chunk); i += 4096 {
		chunk[i] = 1
	}

	mu.Lock()
	eaten = append(eaten, chunk)
	total := 0
	for _, c := range eaten {
		total += len(c)
	}
	mu.Unlock()

	fmt.Fprintf(w, "holding %d MB total\n", total>>20)
}

func burn(w http.ResponseWriter, _ *http.Request) {
	go func() {
		for {
		}
	}()
	fmt.Fprintln(w, "burning one core")
}

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "0.0.0.0:8080"
	}

	http.HandleFunc("/health", health)
	http.HandleFunc("/eat", eat)
	http.HandleFunc("/burn", burn)

	log.Printf("api pid=%d listening on %s", os.Getpid(), addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
