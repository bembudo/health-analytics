package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"
)

func newHTTPClient(insecureSkipVerify bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecureSkipVerify}
	return &http.Client{
		Timeout:   attemptTimeout,
		Transport: transport,
	}
}

func checkHTTP(client *http.Client, ep EndpointConfig, timestamp string) bool {
	start := time.Now()
	// 1. Ping the primary URL
	resp, err := headWithoutKeepAlive(client, ep.URL)
	if err == nil && (resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented) {
		resp.Body.Close()
		resp, err = client.Get(ep.URL)
	}
	if err != nil {
		fmt.Printf("[%s] ERROR: Failed to reach %s: %v\n", timestamp, ep.label(), err)
		return false
	}
	defer resp.Body.Close()

	// 2. Evaluate if the service is healthy (2xx unless expected status is configured)
	if !ep.acceptsStatus(resp.StatusCode) {
		fmt.Printf("[%s] FAILURE: %s returned unhealthy status code: %d\n", timestamp, ep.label(), resp.StatusCode)
		return false
	}
	fmt.Printf("[%s] SUCCESS: %s returned status %d (%dms)\n", timestamp, ep.label(), resp.StatusCode, time.Since(start).Milliseconds())
	return true
}

func headWithoutKeepAlive(client *http.Client, rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Close = true
	return client.Do(req)
}
