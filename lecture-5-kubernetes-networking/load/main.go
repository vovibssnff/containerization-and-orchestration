package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"time"
)

// load posts orders to the shop Service by DNS name and prints one line per
// second: unix ok fail. The chaos test graphs this against pods being deleted.
func main() {
	target := os.Getenv("TARGET")
	if target == "" {
		target = "http://api.shop.svc.cluster.local:8080/order"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	body := []byte(`{"item":"bread"}`)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var ok, fail int
	start := time.Now()
	for time.Since(start) < 100*time.Second {
		<-tick.C
		ok, fail = 0, 0
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if post(client, target, body) {
				ok++
			} else {
				fail++
			}
			time.Sleep(15 * time.Millisecond)
		}
		fmt.Printf("%d %d %d\n", time.Now().Unix(), ok, fail)
	}
}

// post sends one order. A failure is retried a few times: kube-proxy removes a
// pod from the EndpointSlice as soon as it is deleted, but a request already
// on that pod can still be reset or answered with an error. The next attempt
// is a new connection, and kube-proxy sends it to a pod that is still ready.
func post(client *http.Client, target string, body []byte) bool {
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusCreated {
			return true
		}
	}
	return false
}
