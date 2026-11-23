package main

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
)

type TCPConfig struct {
	Timeout time.Duration `yaml:"timeout"`
	Network string        `yaml:"network"`
}

var tcpNetworks = []string{"tcp", "tcp4", "tcp6"}

func (tc TCPConfig) network() string {
	if tc.Network == "" {
		return "tcp"
	}
	return strings.ToLower(tc.Network)
}

func (tc TCPConfig) timeout() time.Duration {
	if tc.Timeout == 0 {
		return attemptTimeout
	}
	return tc.Timeout
}

func (tc TCPConfig) validate(address string) error {
	if strings.Contains(address, "://") {
		return fmt.Errorf("tcp url must be host:port without a scheme, got %q", address)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return fmt.Errorf("tcp url must be host:port, got %q", address)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid tcp port %q", port)
	}
	if !slices.Contains(tcpNetworks, tc.network()) {
		return fmt.Errorf("invalid tcp network %q", tc.Network)
	}
	if tc.Timeout < 0 {
		return fmt.Errorf("tcp timeout must not be negative, got %v", tc.Timeout)
	}
	return nil
}

func checkTCP(ep EndpointConfig, timestamp string) bool {
	start := time.Now()
	conn, err := net.DialTimeout(ep.TCP.network(), ep.URL, ep.TCP.timeout())
	if err != nil {
		fmt.Printf("[%s] ERROR: Failed to connect to %s (%s): %v\n", timestamp, ep.label(), classifyNetError(err), err)
		return false
	}
	conn.Close()
	fmt.Printf("[%s] SUCCESS: %s accepted TCP connection (%dms)\n", timestamp, ep.label(), time.Since(start).Milliseconds())
	return true
}
