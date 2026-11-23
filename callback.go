package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

func failCallbackURL(callbackURL string) (string, error) {
	parsed, err := url.Parse(callbackURL)
	if err != nil {
		return "", err
	}
	return parsed.JoinPath("fail").String(), nil
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

func classifyNetError(err error) string {
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
			fmt.Printf("[%s]   -> WARNING: %s callback for %s failed after %dms (%s) [%s], retrying in %v: %v\n", nowStamp(), kind, label, time.Since(start).Milliseconds(), classifyNetError(err), trace.summary(), callbackRetryDelay, err)
		} else {
			fmt.Printf("[%s]   -> WARNING: %s callback for %s returned status %d after %dms, retrying in %v\n", nowStamp(), kind, label, cbResp.StatusCode, time.Since(start).Milliseconds(), callbackRetryDelay)
			cbResp.Body.Close()
		}
		time.Sleep(callbackRetryDelay)
		start = time.Now()
		cbResp, trace, err = attemptCallback(client, callbackURL)
		if err != nil {
			fmt.Printf("[%s]   -> WARNING: %s callback for %s retry failed after %dms (%s) [%s]: %v\n", nowStamp(), kind, label, time.Since(start).Milliseconds(), classifyNetError(err), trace.summary(), err)
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
