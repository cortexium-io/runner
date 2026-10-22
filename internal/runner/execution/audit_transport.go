package execution

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
)

const auditResponseLimit = 16 << 20
const auditTotalLimit = 128 << 20
const auditRequestLimit = 1001

// No ambient proxy, cookie jar, credential provider, redirects or general URL
// input. Resolve and dial the validated address ourselves to avoid DNS rebinding.
func auditHTTPClient() *http.Client {
	t := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host != "registry.npmjs.org" || port != "443" {
			return nil, errors.New("unapproved npm audit destination")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("public npm registry resolution unavailable")
		}
		for _, ip := range ips {
			if !publicAuditIP(ip) {
				return nil, errors.New("npm audit registry resolved to a non-public destination")
			}
		}
		d := net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("npm audit redirects are unsupported") }}
}

func publicAuditIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/23", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}

type auditTransport struct {
	client   *http.Client
	packages map[string][]string
	requests int
	bytes    int
	bulk     bool
}

func (t *auditTransport) request(ctx context.Context, kind string, data json.RawMessage) ([]byte, error) {
	method, path := "GET", ""
	var body []byte
	switch kind {
	case "bulk":
		if t.bulk {
			return nil, errors.New("duplicate npm bulk request")
		}
		t.bulk = true
		var payload map[string][]string
		if json.Unmarshal(data, &payload) != nil {
			return nil, errors.New("invalid npm bulk metadata")
		}
		for name, versions := range payload {
			allowed, ok := t.packages[name]
			if !ok || len(versions) == 0 {
				return nil, errors.New("npm bulk metadata outside approved graph")
			}
			for _, version := range versions {
				if !containsVersion(allowed, version) {
					return nil, errors.New("npm bulk version outside approved graph")
				}
			}
		}
		body, _ = json.Marshal(payload)
		if len(body) > 1<<20 {
			return nil, errors.New("npm bulk payload exceeds limit")
		}
		method, path = "POST", "/-/npm/v1/security/advisories/bulk"
	case "package":
		var name string
		if !t.bulk || json.Unmarshal(data, &name) != nil || t.packages[name] == nil {
			return nil, errors.New("npm metadata request outside approved graph")
		}
		path = "/" + url.PathEscape(name)
	default:
		return nil, errors.New("unsupported npm audit request")
	}
	t.requests++
	if t.requests > auditRequestLimit {
		return nil, errors.New("npm audit request limit exceeded")
	}
	req, err := http.NewRequestWithContext(ctx, method, config.NPMRegistry+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cortexium-runner/"+config.NPMAuditAdapter)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := t.client.Do(req)
	if err != nil {
		return nil, errors.New("public npm audit request unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("public npm audit service returned HTTP %d", res.StatusCode)
	}
	result, err := io.ReadAll(io.LimitReader(res.Body, auditResponseLimit+1))
	t.bytes += len(result)
	if err != nil || len(result) > auditResponseLimit || t.bytes > auditTotalLimit || !json.Valid(result) {
		return nil, errors.New("npm audit response malformed or exceeds limit")
	}
	if kind == "bulk" {
		var advisories map[string][]json.RawMessage
		if json.Unmarshal(result, &advisories) != nil || advisories == nil {
			return nil, errors.New("invalid npm bulk response")
		}
		for name, entries := range advisories {
			if t.packages[name] == nil || len(entries) == 0 {
				return nil, errors.New("npm bulk response outside approved graph")
			}
			for _, entry := range entries {
				var a struct {
					ID                         int `json:"id"`
					Name, Severity, Title, URL string
					Vulnerable                 string `json:"vulnerable_versions"`
				}
				if json.Unmarshal(entry, &a) != nil || a.ID <= 0 || (a.Name != "" && a.Name != name) || !auditSeverity(a.Severity) || a.Vulnerable == "" || a.Title == "" || a.URL == "" {
					return nil, errors.New("malformed npm advisory")
				}
			}
		}
	} else {
		var p struct {
			Name     string
			Versions map[string]json.RawMessage
		}
		var name string
		_ = json.Unmarshal(data, &name)
		if json.Unmarshal(result, &p) != nil || p.Name != name || len(p.Versions) == 0 {
			return nil, errors.New("malformed npm package metadata")
		}
		for _, version := range t.packages[name] {
			if p.Versions[version] == nil {
				return nil, errors.New("npm metadata does not contain a locked version")
			}
		}
	}
	return result, nil
}

func auditSeverity(s string) bool {
	return s == "info" || s == "low" || s == "moderate" || s == "high" || s == "critical"
}
func containsVersion(versions []string, version string) bool {
	for _, v := range versions {
		if v == version {
			return true
		}
	}
	return false
}
