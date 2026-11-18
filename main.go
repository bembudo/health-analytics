package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	configPath           = "config.yaml"
	configPollInterval   = 5 * time.Second
	attemptTimeout       = 10 * time.Second
	callbackRetryDelay   = 5 * time.Second
	defaultRetryInterval = 5 * time.Second
)

type EndpointConfig struct {
	URL                    string        `yaml:"url"`
	Alias                  string        `yaml:"alias"`
	HealthCheckCallbackURL string        `yaml:"healthcheck callback url"`
	RefreshRate            time.Duration `yaml:"refresh rate"`
	ExpectedStatus         []int         `yaml:"expected status"`
	InsecureSkipVerify     bool          `yaml:"insecure skip verify"`
	DNS                    *DNSConfig    `yaml:"dns"`
	retries                int
	retryInterval          time.Duration
}

type DNSConfig struct {
	QueryName     string `yaml:"query-name"`
	QueryType     string `yaml:"query-type"`
	ExpectedRcode string `yaml:"expected-rcode"`
}

var dnsQueryTypes = map[string]uint16{"A": 1, "NS": 2, "CNAME": 5, "MX": 15, "TXT": 16, "AAAA": 28}

var dnsRcodes = []string{"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED"}

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

func (dc DNSConfig) queryType() string {
	if dc.QueryType == "" {
		return "A"
	}
	return strings.ToUpper(dc.QueryType)
}

func (dc DNSConfig) expectedRcode() string {
	if dc.ExpectedRcode == "" {
		return "NOERROR"
	}
	return strings.ToUpper(dc.ExpectedRcode)
}

func (dc DNSConfig) validate() error {
	if dc.QueryName == "" {
		return errors.New("dns query name is required")
	}
	if _, ok := dnsQueryTypes[dc.queryType()]; !ok {
		return fmt.Errorf("invalid dns query type %q", dc.QueryType)
	}
	if !slices.Contains(dnsRcodes, dc.expectedRcode()) {
		return fmt.Errorf("invalid dns expected rcode %q", dc.ExpectedRcode)
	}
	return nil
}

type Config struct {
	Retries       int              `yaml:"retries"`
	RetryInterval time.Duration    `yaml:"retry interval"`
	Endpoints     []EndpointConfig `yaml:"endpoints"`
}

type worker struct {
	endpoint EndpointConfig
	cancel   context.CancelFunc
}

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
		if ep.HealthCheckCallbackURL != "" {
			parsed, err := url.Parse(ep.HealthCheckCallbackURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return Config{}, fmt.Errorf("endpoint %s has invalid healthcheck callback url %q", ep.URL, ep.HealthCheckCallbackURL)
			}
		}
		if config.Retries > 0 {
			worstCase := time.Duration(config.Retries+1)*2*attemptTimeout + time.Duration(config.Retries)*config.RetryInterval
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

func checkDNS(ep EndpointConfig, timestamp string) bool {
	start := time.Now()
	rcode, err := queryDNS(ep.URL, ep.DNS.QueryName, dnsQueryTypes[ep.DNS.queryType()])
	if err != nil {
		fmt.Printf("[%s] ERROR: Failed to query %s for %s: %v\n", timestamp, ep.label(), ep.DNS.QueryName, err)
		return false
	}

	got := rcodeName(rcode)
	if got != ep.DNS.expectedRcode() {
		fmt.Printf("[%s] FAILURE: %s returned rcode %s for %s, expected %s\n", timestamp, ep.label(), got, ep.DNS.QueryName, ep.DNS.expectedRcode())
		return false
	}
	fmt.Printf("[%s] SUCCESS: %s resolved %s with rcode %s (%dms)\n", timestamp, ep.label(), ep.DNS.QueryName, got, time.Since(start).Milliseconds())
	return true
}

func failCallbackURL(callbackURL string) (string, error) {
	parsed, err := url.Parse(callbackURL)
	if err != nil {
		return "", err
	}
	return parsed.JoinPath("fail").String(), nil
}

func nowStamp() string {
	return time.Now().Format("15:04:05")
}

type callbackTrace struct {
	mu           sync.Mutex
	dnsStart     time.Time
	dnsDone      time.Time
	connStart    time.Time
	connDone     time.Time
	tlsStart     time.Time
	tlsDone      time.Time
	wroteRequest time.Time
	firstByte    time.Time
}

func (trace *callbackTrace) mark(field *time.Time) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	*field = time.Now()
}

func (trace *callbackTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { trace.mark(&trace.dnsStart) },
		DNSDone:              func(httptrace.DNSDoneInfo) { trace.mark(&trace.dnsDone) },
		ConnectStart:         func(string, string) { trace.mark(&trace.connStart) },
		ConnectDone:          func(string, string, error) { trace.mark(&trace.connDone) },
		TLSHandshakeStart:    func() { trace.mark(&trace.tlsStart) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { trace.mark(&trace.tlsDone) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { trace.mark(&trace.wroteRequest) },
		GotFirstResponseByte: func() { trace.mark(&trace.firstByte) },
	}
}

func (trace *callbackTrace) summary() string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	var phases []string
	addPhase := func(name string, start, done time.Time) {
		if start.IsZero() {
			return
		}
		if done.IsZero() {
			phases = append(phases, name+" stalled")
			return
		}
		phases = append(phases, fmt.Sprintf("%s %dms", name, done.Sub(start).Milliseconds()))
	}
	addPhase("dns", trace.dnsStart, trace.dnsDone)
	addPhase("connect", trace.connStart, trace.connDone)
	addPhase("tls", trace.tlsStart, trace.tlsDone)
	if !trace.wroteRequest.IsZero() && trace.firstByte.IsZero() {
		phases = append(phases, "waiting for response")
	}
	if len(phases) == 0 {
		return "no connection attempted"
	}
	return strings.Join(phases, ", ")
}

func classifyCallbackError(err error) string {
	var dnsErr *net.DNSError
	var certErr *tls.CertificateVerificationError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.As(err, &certErr):
		return "tls"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	default:
		return "error"
	}
}

func attemptCallback(client *http.Client, callbackURL string) (*http.Response, *callbackTrace, error) {
	trace := &callbackTrace{}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace.clientTrace()), http.MethodGet, callbackURL, nil)
	if err != nil {
		return nil, trace, err
	}
	resp, err := client.Do(req)
	return resp, trace, err
}

func pingCallback(client *http.Client, callbackURL string, label string, kind string) {
	start := time.Now()
	cbResp, trace, err := attemptCallback(client, callbackURL)
	retried := false
	if err != nil || cbResp.StatusCode >= http.StatusInternalServerError {
		if err != nil {
			fmt.Printf("[%s]   -> WARNING: %s callback for %s failed after %dms (%s) [%s], retrying in %v: %v\n", nowStamp(), kind, label, time.Since(start).Milliseconds(), classifyCallbackError(err), trace.summary(), callbackRetryDelay, err)
		} else {
			fmt.Printf("[%s]   -> WARNING: %s callback for %s returned status %d after %dms, retrying in %v\n", nowStamp(), kind, label, cbResp.StatusCode, time.Since(start).Milliseconds(), callbackRetryDelay)
			cbResp.Body.Close()
		}
		time.Sleep(callbackRetryDelay)
		start = time.Now()
		cbResp, trace, err = attemptCallback(client, callbackURL)
		if err != nil {
			fmt.Printf("[%s]   -> WARNING: %s callback for %s retry failed after %dms (%s) [%s]: %v\n", nowStamp(), kind, label, time.Since(start).Milliseconds(), classifyCallbackError(err), trace.summary(), err)
			return
		}
		retried = true
	}
	defer cbResp.Body.Close()
	suffix := ""
	if retried {
		suffix = " on retry"
	}
	switch {
	case cbResp.StatusCode >= http.StatusOK && cbResp.StatusCode < http.StatusMultipleChoices:
		fmt.Printf("[%s]   -> Callback %s for %s succeeded%s (Status: %d, %dms)\n", nowStamp(), kind, label, suffix, cbResp.StatusCode, time.Since(start).Milliseconds())
	case cbResp.StatusCode >= http.StatusBadRequest && cbResp.StatusCode < http.StatusInternalServerError:
		fmt.Printf("[%s]   -> ERROR: %s callback for %s rejected%s (Status: %d, %dms)\n", nowStamp(), kind, label, suffix, cbResp.StatusCode, time.Since(start).Milliseconds())
	case cbResp.StatusCode >= http.StatusInternalServerError:
		fmt.Printf("[%s]   -> WARNING: %s callback for %s hit remote service error%s (Status: %d, %dms)\n", nowStamp(), kind, label, suffix, cbResp.StatusCode, time.Since(start).Milliseconds())
	default:
		fmt.Printf("[%s]   -> WARNING: %s callback for %s returned unexpected status%s (Status: %d, %dms)\n", nowStamp(), kind, label, suffix, cbResp.StatusCode, time.Since(start).Milliseconds())
	}
}

func queryDNS(resolver, name string, qtype uint16) (uint16, error) {
	const timeout = attemptTimeout

	id := uint16(rand.UintN(1 << 16))
	query, err := buildDNSQuery(id, name, qtype)
	if err != nil {
		return 0, err
	}

	var conn net.Conn
	framed := false
	if server, ok := strings.CutPrefix(resolver, "tls://"); ok {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", dnsServerAddress(server, "853"), &tls.Config{})
		framed = true
	} else {
		conn, err = net.DialTimeout("udp", dnsServerAddress(resolver, "53"), timeout)
	}
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return 0, err
	}

	resp, err := exchangeDNS(conn, query, framed)
	if err != nil {
		return 0, err
	}
	return parseDNSRcode(id, resp)
}

func exchangeDNS(conn io.ReadWriter, query []byte, framed bool) ([]byte, error) {
	if !framed {
		if _, err := conn.Write(query); err != nil {
			return nil, err
		}
		resp := make([]byte, 512)
		n, err := conn.Read(resp)
		if err != nil {
			return nil, err
		}
		return resp[:n], nil
	}

	msg := binary.BigEndian.AppendUint16(nil, uint16(len(query)))
	msg = append(msg, query...)
	if _, err := conn.Write(msg); err != nil {
		return nil, err
	}

	var size [2]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func parseDNSRcode(id uint16, resp []byte) (uint16, error) {
	if len(resp) < 12 || binary.BigEndian.Uint16(resp[0:2]) != id || resp[2]&0x80 == 0 {
		return 0, errors.New("malformed or mismatched DNS response")
	}
	return binary.BigEndian.Uint16(resp[2:4]) & 0x000F, nil
}

func dnsServerAddress(server, defaultPort string) string {
	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}
	return net.JoinHostPort(strings.Trim(server, "[]"), defaultPort)
}

func buildDNSQuery(id uint16, name string, qtype uint16) ([]byte, error) {
	msg := make([]byte, 12, 512)
	binary.BigEndian.PutUint16(msg[0:2], id)
	binary.BigEndian.PutUint16(msg[2:4], 0x0100)
	binary.BigEndian.PutUint16(msg[4:6], 1)

	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" || len(label) > 63 {
			return nil, fmt.Errorf("invalid DNS name %q", name)
		}
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, 1)

	return msg, nil
}

func rcodeName(code uint16) string {
	if int(code) < len(dnsRcodes) {
		return dnsRcodes[code]
	}
	return strconv.Itoa(int(code))
}
