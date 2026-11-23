package main

import (
	"log"
	"time"
)

const (
	configPath           = "config.yaml"
	configPollInterval   = 5 * time.Second
	attemptTimeout       = 10 * time.Second
	callbackRetryDelay   = 5 * time.Second
	defaultRetryInterval = 5 * time.Second
)

func main() {
	config, err := readConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	lastModTime, lastSize, err := statConfig()
	if err != nil {
		log.Fatalf("Failed to stat config: %v", err)
	}

	workers := reconcileWorkers(nil, config.Endpoints)

	ticker := time.NewTicker(configPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		modTime, size, err := statConfig()
		if err != nil {
			log.Printf("WARNING: Failed to stat config, keeping current config: %v", err)
			continue
		}
		if modTime.Equal(lastModTime) && size == lastSize {
			continue
		}
		lastModTime, lastSize = modTime, size

		newConfig, err := readConfig()
		if err != nil {
			log.Printf("WARNING: Invalid config change ignored, keeping current config: %v", err)
			continue
		}
		workers = reconcileWorkers(workers, newConfig.Endpoints)
		log.Printf("Reloaded %s (%d endpoints)", configPath, len(newConfig.Endpoints))
	}
}
