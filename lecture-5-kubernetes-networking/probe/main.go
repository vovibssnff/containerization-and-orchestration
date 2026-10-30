package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

// probe dials TCP targets from the arguments (host:port) and prints OK or FAIL.
// It is the client used to walk the access matrix. Exit status is 0 only if
// every target connected.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe host:port [host:port...]")
		os.Exit(2)
	}
	fail := false
	for _, target := range os.Args[1:] {
		conn, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err != nil {
			fmt.Printf("FAIL %s %s\n", target, err)
			fail = true
			continue
		}
		conn.Close()
		fmt.Printf("OK   %s\n", target)
	}
	if fail {
		os.Exit(1)
	}
}
