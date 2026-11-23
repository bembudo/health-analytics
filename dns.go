package main

import (
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
)

type DNSConfig struct {
	QueryName     string `yaml:"query-name"`
	QueryType     string `yaml:"query-type"`
	ExpectedRcode string `yaml:"expected-rcode"`
}

var dnsQueryTypes = map[string]uint16{"A": 1, "NS": 2, "CNAME": 5, "MX": 15, "TXT": 16, "AAAA": 28}

var dnsRcodes = []string{"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED"}

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
