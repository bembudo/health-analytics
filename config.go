package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

type EndpointConfig struct {
	URL                    string        `yaml:"url"`
	Alias                  string        `yaml:"alias"`
	HealthCheckCallbackURL string        `yaml:"healthcheck callback url"`
	RefreshRate            time.Duration `yaml:"refresh rate"`
	ExpectedStatus         []int         `yaml:"expected status"`
	InsecureSkipVerify     bool          `yaml:"insecure skip verify"`
	DNS                    *DNSConfig    `yaml:"dns"`
	TCP                    *TCPConfig    `yaml:"tcp"`
	retries                int
	retryInterval          time.Duration
}

func (ep EndpointConfig) label() string {
	if ep.Alias == "" {
		return ep.URL
	}
	return fmt.Sprintf("%s (%s)", ep.Alias, ep.URL)
}

func (ep EndpointConfig) acceptsStatus(code int) bool {
	if len(ep.ExpectedStatus) == 0 {
		return code >= 200 && code < 300
	}
	return slices.Contains(ep.ExpectedStatus, code)
}

type Config struct {
	Retries       int              `yaml:"retries"`
	RetryInterval time.Duration    `yaml:"retry interval"`
	Endpoints     []EndpointConfig `yaml:"endpoints"`
}

func statConfig() (time.Time, int64, error) {
	info, err := os.Stat(configPath)
	if err != nil {
		return time.Time{}, 0, err
	}
	return info.ModTime(), info.Size(), nil
}

func readConfig() (Config, error) {
	fileData, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("failed to read %s: %w", configPath, err)
	}

	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(fileData))
	decoder.KnownFields(true)
	err = decoder.Decode(&config)
	if err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("failed to parse YAML: %w", err)
	}

	if config.Retries < 0 {
		return Config{}, fmt.Errorf("retries must not be negative, got %d", config.Retries)
	}
	if config.RetryInterval < 0 {
		return Config{}, fmt.Errorf("retry interval must not be negative, got %v", config.RetryInterval)
	}
	if config.RetryInterval == 0 {
		config.RetryInterval = defaultRetryInterval
	}

	for i, ep := range config.Endpoints {
		if ep.URL == "" {
			return Config{}, fmt.Errorf("endpoint #%d is missing url", i+1)
		}
		if ep.RefreshRate <= 0 {
			return Config{}, fmt.Errorf("endpoint %s has invalid refresh rate %v", ep.URL, ep.RefreshRate)
		}
		if ep.DNS != nil {
			if err := ep.DNS.validate(); err != nil {
				return Config{}, fmt.Errorf("endpoint %s has invalid dns config: %w", ep.URL, err)
			}
			if ep.InsecureSkipVerify {
				return Config{}, fmt.Errorf("endpoint %s cannot use insecure skip verify with dns", ep.URL)
			}
		}
		if ep.TCP != nil {
			if ep.DNS != nil {
				return Config{}, fmt.Errorf("endpoint %s cannot have both dns and tcp checks", ep.URL)
			}
			if err := ep.TCP.validate(ep.URL); err != nil {
				return Config{}, fmt.Errorf("endpoint %s has invalid tcp config: %w", ep.URL, err)
			}
			if ep.InsecureSkipVerify {
				return Config{}, fmt.Errorf("endpoint %s cannot use insecure skip verify with tcp", ep.URL)
			}
		}
		if ep.HealthCheckCallbackURL != "" {
			parsed, err := url.Parse(ep.HealthCheckCallbackURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return Config{}, fmt.Errorf("endpoint %s has invalid healthcheck callback url %q", ep.URL, ep.HealthCheckCallbackURL)
			}
		}
		if config.Retries > 0 {
			attempt := 2 * attemptTimeout
			if ep.TCP != nil {
				attempt = ep.TCP.timeout()
			}
			worstCase := time.Duration(config.Retries+1)*attempt + time.Duration(config.Retries)*config.RetryInterval
			if worstCase >= ep.RefreshRate {
				return Config{}, fmt.Errorf("endpoint %s may take up to %v per check with %d retries, which must be less than refresh rate %v", ep.URL, worstCase, config.Retries, ep.RefreshRate)
			}
		}
		for _, code := range ep.ExpectedStatus {
			if code < 100 || code > 599 {
				return Config{}, fmt.Errorf("endpoint %s has invalid expected status %d", ep.URL, code)
			}
		}
	}

	for i := range config.Endpoints {
		config.Endpoints[i].retries = config.Retries
		config.Endpoints[i].retryInterval = config.RetryInterval
	}

	return config, nil
}
