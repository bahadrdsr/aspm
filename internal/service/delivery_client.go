package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

const deliveryCALimit = 1 << 20
const jiraAPIOriginLimit = 16

func deliveryRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("delivery CA file could not be opened")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > deliveryCALimit {
		return nil, errors.New("delivery CA file must be a bounded regular PEM file")
	}
	data, err := io.ReadAll(io.LimitReader(file, deliveryCALimit+1))
	if err != nil || len(data) > deliveryCALimit {
		return nil, errors.New("delivery CA file could not be read within its size bound")
	}
	pool := x509.NewCertPool()
	count := 0
	for rest := bytes.TrimSpace(data); len(rest) > 0; {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("delivery CA file contains invalid PEM")
		}
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("delivery CA file contains invalid certificate PEM")
		}
		certificate, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil {
			return nil, errors.New("delivery CA file contains an invalid certificate")
		}
		pool.AddCert(certificate)
		count++
		rest = bytes.TrimSpace(remaining)
	}
	if count == 0 {
		return nil, errors.New("delivery CA file contains no certificates")
	}
	return pool, nil
}

func deliveryAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || host == "" || number < 1 || number > 65535 {
		return "", errors.New("delivery gateway address is invalid")
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	}
	return net.JoinHostPort(host, strconv.Itoa(number)), nil
}

func deliveryOrigin(endpoint string) (string, error) {
	value, err := app.ValidateDeliveryGateway(endpoint)
	if err != nil {
		return "", err
	}
	target, err := url.Parse(value)
	if err != nil {
		return "", errors.New("delivery gateway could not be parsed")
	}
	port := target.Port()
	if port == "" {
		port = "443"
	}
	return deliveryAddress(net.JoinHostPort(target.Hostname(), port))
}

type deliveryDial func(context.Context, string, string) (net.Conn, error)

func jiraOriginAddresses(origins []string) ([]string, error) {
	return providerOriginAddresses(origins, "ASPM_JIRA_API_ORIGINS/JiraAPIOrigins")
}

func teamsOriginAddresses(origins []string) ([]string, error) {
	return providerOriginAddresses(origins, "ASPM_TEAMS_WORKFLOW_ORIGINS/TeamsWorkflowOrigins")
}

func providerOriginAddresses(origins []string, field string) ([]string, error) {
	invalid := errors.New(field + " requires at most 16 unique canonical HTTPS origins")
	if len(origins) > jiraAPIOriginLimit {
		return nil, invalid
	}
	addresses := make([]string, 0, len(origins))
	seen := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		if !strings.HasPrefix(origin, "https://") ||
			strings.ContainsAny(strings.TrimPrefix(origin, "https://"), "/?#%@\\*") ||
			strings.ContainsFunc(origin, func(c rune) bool { return c <= ' ' || c >= 0x7f }) {
			return nil, invalid
		}
		target, err := url.Parse(origin)
		if err != nil || target.Host == "" || target.User != nil || target.Opaque != "" {
			return nil, invalid
		}
		host := target.Hostname()
		canonical := host
		if ip, err := netip.ParseAddr(host); err == nil {
			if ip.Is4In6() || ip.Zone() != "" {
				return nil, invalid
			}
			canonical = ip.String()
			if ip.Is6() {
				canonical = "[" + canonical + "]"
			}
		} else {
			if len(host) == 0 || len(host) > 253 {
				return nil, invalid
			}
			numeric := true
			for _, c := range host {
				if c != '.' && (c < '0' || c > '9') {
					numeric = false
				}
			}
			if numeric {
				return nil, invalid
			}
			for _, label := range strings.Split(host, ".") {
				if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
					return nil, invalid
				}
				for _, c := range label {
					if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
						return nil, invalid
					}
				}
			}
		}
		if port := target.Port(); port != "" {
			number, err := strconv.Atoi(port)
			if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
				return nil, invalid
			}
			canonical += ":" + port
		}
		if target.Host != canonical {
			return nil, invalid
		}
		address, err := deliveryOrigin(origin)
		if err != nil {
			return nil, invalid
		}
		if _, duplicate := seen[address]; duplicate {
			return nil, invalid
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	return addresses, nil
}

func deliveryOrigins(endpoint string, jiraOrigins []string) ([]string, error) {
	return deliveryOriginUnion(endpoint, jiraOrigins, nil)
}

func deliveryOriginUnion(endpoint string, jiraOrigins, teamsOrigins []string) ([]string, error) {
	origin, err := deliveryOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	additional, err := jiraOriginAddresses(jiraOrigins)
	if err != nil {
		return nil, err
	}
	teams, err := teamsOriginAddresses(teamsOrigins)
	if err != nil {
		return nil, err
	}
	all := append([]string{origin}, additional...)
	all = append(all, teams...)
	union := make([]string, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, address := range all {
		if _, duplicate := seen[address]; !duplicate {
			seen[address] = struct{}{}
			union = append(union, address)
		}
	}
	return union, nil
}

func deliveryOriginDial(origins []string, dial deliveryDial) deliveryDial {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowed[origin] = struct{}{}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		candidate, err := deliveryAddress(address)
		_, approved := allowed[candidate]
		if err != nil || !approved || (network != "tcp" && network != "tcp4" && network != "tcp6") {
			return nil, errors.New("delivery transport denied an unapproved gateway address")
		}
		return dial(ctx, network, address)
	}
}

func newProviderClient(endpoint, caFile string, jiraOrigins ...string) (*http.Client, error) {
	return newGatewayProviderClient(endpoint, caFile, jiraOrigins, nil)
}

func newGatewayProviderClient(endpoint, caFile string, jiraOrigins, teamsOrigins []string) (*http.Client, error) {
	origins, err := deliveryOriginUnion(endpoint, jiraOrigins, teamsOrigins)
	if err != nil {
		return nil, err
	}
	client, err := newDirectProviderClient(caFile)
	if err != nil {
		return nil, err
	}
	transport := client.Transport.(*http.Transport)
	transport.DialContext = deliveryOriginDial(origins, transport.DialContext)
	return client, nil
}

func newDirectProviderClient(caFile string) (*http.Client, error) {
	roots, err := deliveryRoots(caFile)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout: 30 * time.Second, MaxIdleConns: 2, MaxIdleConnsPerHost: 1,
		ForceAttemptHTTP2: true,
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func validateDeliveryClient(client *http.Client, endpoint string) error {
	if client == nil {
		return errors.New("delivery client is required")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		return errors.New("delivery client requires an explicit HTTP transport")
	}
	if transport.Proxy != nil {
		return errors.New("delivery proxy transports are not permitted")
	}
	if transport.DialTLS != nil || transport.DialTLSContext != nil {
		return errors.New("delivery TLS must use normal transport certificate verification")
	}
	if policy := transport.TLSClientConfig; policy != nil {
		if policy.InsecureSkipVerify || (policy.MinVersion != 0 && policy.MinVersion < tls.VersionTLS12) ||
			(policy.MaxVersion != 0 && policy.MaxVersion < tls.VersionTLS12) {
			return errors.New("delivery TLS must verify certificates and require TLS 1.2 or newer")
		}
		minimum, maximum := policy.MinVersion, policy.MaxVersion
		if minimum == 0 {
			minimum = tls.VersionTLS12
		}
		if maximum == 0 {
			maximum = tls.VersionTLS13
		}
		if minimum > maximum || minimum > tls.VersionTLS13 {
			return errors.New("delivery TLS version range has no supported protocol")
		}
		if policy.ServerName != "" {
			target, err := url.Parse(endpoint)
			if err != nil || !strings.EqualFold(strings.TrimSuffix(policy.ServerName, "."), strings.TrimSuffix(target.Hostname(), ".")) {
				return errors.New("delivery TLS hostname must match the configured gateway")
			}
		}
	}
	if client.Timeout <= 0 || client.Timeout > 30*time.Second || client.Jar != nil {
		return errors.New("delivery client requires a bounded timeout and no cookie jar")
	}
	return nil
}

func ownDeliveryClient(config Config) (*http.Client, error) {
	return ownGatewayProviderClient(config.DeliveryClient, config.SlackEndpoint, config.DeliveryCAFile,
		config.JiraAPIOrigins, config.TeamsWorkflowOrigins)
}

func ownProviderClient(supplied *http.Client, gateway, caFile string, jiraOrigins ...string) (*http.Client, error) {
	return ownGatewayProviderClient(supplied, gateway, caFile, jiraOrigins, nil)
}

func ownGatewayProviderClient(supplied *http.Client, gateway, caFile string, jiraOrigins, teamsOrigins []string) (*http.Client, error) {
	endpoint, err := app.ValidateDeliveryGateway(gateway)
	if err != nil {
		return nil, err
	}
	if err = validateDeliveryClient(supplied, endpoint); err != nil {
		return nil, err
	}
	origins, err := deliveryOriginUnion(endpoint, jiraOrigins, teamsOrigins)
	if err != nil {
		return nil, err
	}
	client, err := ownDirectProviderClient(supplied, caFile)
	if err != nil {
		return nil, err
	}
	transport := client.Transport.(*http.Transport)
	transport.DialContext = deliveryOriginDial(origins, transport.DialContext)
	return client, nil
}

func ownDirectProviderClient(supplied *http.Client, caFile string) (*http.Client, error) {
	transport := supplied.Transport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		if transport.TLSClientConfig.MinVersion == 0 {
			transport.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
		if transport.TLSClientConfig.RootCAs != nil {
			transport.TLSClientConfig.RootCAs = transport.TLSClientConfig.RootCAs.Clone()
		}
		if transport.TLSClientConfig.ClientCAs != nil {
			transport.TLSClientConfig.ClientCAs = transport.TLSClientConfig.ClientCAs.Clone()
		}
	}
	if caFile != "" {
		roots, err := deliveryRoots(caFile)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	if transport.DialContext == nil {
		transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	client := *supplied
	client.Transport = transport
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil
	return &client, nil
}
