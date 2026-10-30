package main

import (
	"os"
	"strconv"
	"time"
)

// batch is the stand-in for nightly analytics. It holds a byte slice so the
// working set is real, and optionally spins on CPU. It does no useful work.
func main() {
	mb := getenvInt("MEMORY_MB", 32)
	burn := os.Getenv("BURN_CPU") == "true"

	buf := make([]byte, mb<<20)
	touch(buf)

	if !burn {
		for {
			touch(buf)
			time.Sleep(time.Second)
		}
	}
	for {
		touch(buf)
	}
}

func touch(buf []byte) {
	for i := 0; i < len(buf); i += 4096 {
		buf[i]++
	}
}

func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return fallback
	}
	return n
}
