package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	probeTimeout  = 15 * time.Second
	slowAfter     = 5 * time.Second
	tlsWarnWithin = 14 * 24 * time.Hour
)

// newProbeClient makes a client that does a fresh connection per probe — we
// want to measure the real thing every time, not a kept-alive socket.
func newProbeClient() *http.Client {
	return &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}

// probe runs the three cheap checks for one URL — the name resolves, the page
// answers, the certificate is healthy — and returns one lens per check. A
// single GET serves both the http and tls lenses: the certificate is read off
// the connection that served the page.
func probe(ctx context.Context, client *http.Client, rawURL string, now time.Time) []Lens {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return []Lens{{Kind: KindHTTP, Status: StatusError, Value: "url?", Detail: "unparseable url: " + rawURL}}
	}
	host := u.Hostname()

	dns := Lens{Kind: KindDNS}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		dns.Status, dns.Value, dns.Detail = StatusError, "fail", "lookup failed: "+shortErr(err)
	} else {
		dns.Status, dns.Value, dns.Detail = StatusGood, "ok", host+" → "+joinAddrs(addrs, 3)
	}
	out := []Lens{dns}

	h := Lens{Kind: KindHTTP, Link: rawURL}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	req.Header.Set("User-Agent", userAgent)
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		h.Status, h.Value, h.Detail = StatusError, "down", shortErr(err)
		out = append(out, h)
		if u.Scheme == "https" {
			t := Lens{Kind: KindTLS, Status: StatusNeutral, Value: "?", Detail: "no connection, certificate not seen"}
			var cve *tls.CertificateVerificationError
			if errors.As(err, &cve) {
				t.Status, t.Value, t.Detail = StatusError, "bad", shortErr(cve.Err)
			}
			out = append(out, t)
		}
		return out
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	h.Value = strconv.Itoa(resp.StatusCode)
	h.Detail = fmt.Sprintf("HTTP %d in %s", resp.StatusCode, latency.Round(time.Millisecond))
	if final := resp.Request.URL.String(); final != rawURL {
		h.Detail += " via " + final
	}
	switch {
	case resp.StatusCode >= 400:
		h.Status = StatusError
	case latency > slowAfter:
		h.Status, h.Detail = StatusWarn, h.Detail+" (slow)"
	default:
		h.Status = StatusGood
	}
	out = append(out, h)

	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		out = append(out, tlsLens(resp.TLS.PeerCertificates[0], now))
	}
	return out
}

func tlsLens(cert *x509.Certificate, now time.Time) Lens {
	left := cert.NotAfter.Sub(now)
	issuer := cert.Issuer.CommonName
	if issuer == "" && len(cert.Issuer.Organization) > 0 {
		issuer = cert.Issuer.Organization[0]
	}
	l := Lens{
		Kind:   KindTLS,
		Value:  dayValue(left),
		Detail: fmt.Sprintf("certificate expires %s (%dd), issuer %s", cert.NotAfter.Format("2006-01-02"), int(left.Hours()/24), issuer),
	}
	switch {
	case left <= 0:
		l.Status = StatusError
	case left < tlsWarnWithin:
		l.Status = StatusWarn
	default:
		l.Status = StatusGood
	}
	return l
}

func joinAddrs(addrs []net.IPAddr, max int) string {
	var ss []string
	for i, a := range addrs {
		if i == max {
			ss = append(ss, fmt.Sprintf("+%d", len(addrs)-max))
			break
		}
		ss = append(ss, a.IP.String())
	}
	return strings.Join(ss, ", ")
}
