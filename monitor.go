package main

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"slices"
	"time"
)

type worker struct {
	endpoint EndpointConfig
	cancel   context.CancelFunc
}

func reconcileWorkers(running []worker, endpoints []EndpointConfig) []worker {
	pending := slices.Clone(endpoints)
	var kept []worker

	for _, w := range running {
		if i := slices.IndexFunc(pending, func(ep EndpointConfig) bool {
			return reflect.DeepEqual(ep, w.endpoint)
		}); i >= 0 {
			pending = slices.Delete(pending, i, i+1)
			kept = append(kept, w)
			continue
		}
		log.Printf("Stopping monitor for %s", w.endpoint.label())
		w.cancel()
	}

	for _, ep := range pending {
		log.Printf("Starting monitor for %s (every %v)", ep.label(), ep.RefreshRate)
		ctx, cancel := context.WithCancel(context.Background())
		go runEndpoint(ctx, ep)
		kept = append(kept, worker{endpoint: ep, cancel: cancel})
	}

	return kept
}

func runEndpoint(ctx context.Context, ep EndpointConfig) {
	ticker := time.NewTicker(ep.RefreshRate)
	defer ticker.Stop()

	performCheck(ep)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			performCheck(ep)
		}
	}
}

func performCheck(ep EndpointConfig) {
	// Create an HTTP client with a reasonable timeout so requests don't hang
	client := newHTTPClient(false)

	var healthy bool
	var timestamp string
	for attempt := 0; attempt <= ep.retries; attempt++ {
		if attempt > 0 {
			time.Sleep(ep.retryInterval)
		}
		timestamp = time.Now().Format("15:04:05")
		if attempt > 0 {
			fmt.Printf("[%s]   -> Retry %d of %d\n", timestamp, attempt, ep.retries)
		}
		if ep.DNS != nil {
			healthy = checkDNS(ep, timestamp)
		} else if ep.TCP != nil {
			healthy = checkTCP(ep, timestamp)
		} else {
			healthy = checkHTTP(newHTTPClient(ep.InsecureSkipVerify), ep, timestamp)
		}
		if healthy {
			break
		}
	}

	if ep.HealthCheckCallbackURL == "" {
		return
	}
	if healthy {
		pingCallback(client, ep.HealthCheckCallbackURL, ep.label(), "ping")
		return
	}
	failURL, err := failCallbackURL(ep.HealthCheckCallbackURL)
	if err != nil {
		fmt.Printf("[%s]   -> WARNING: Failed to build fail callback URL: %v\n", timestamp, err)
		return
	}
	pingCallback(client, failURL, ep.label(), "fail")
}

func nowStamp() string {
	return time.Now().Format("15:04:05")
}
