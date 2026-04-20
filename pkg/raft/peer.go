package raft

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func NormalizePeerAddr(addr string) (string, error) {
	raw := strings.TrimSpace(addr)
	if raw == "" {
		return "", fmt.Errorf("peer addr is empty")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported peer scheme")
	}
	if u.Host == "" {
		return "", fmt.Errorf("peer host is empty")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("peer path is not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("peer query/fragment is not allowed")
	}

	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if host == "" || port == "" {
		return "", fmt.Errorf("peer host:port is required")
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", fmt.Errorf("invalid peer port")
	}

	u.Host = net.JoinHostPort(host, port)
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
