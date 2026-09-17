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

func deliveryOriginDial(origin string, dial deliveryDial) deliveryDial {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		candidate, err := deliveryAddress(address)
		if err != nil || candidate != origin || (network != "tcp" && network != "tcp4" && network != "tcp6") {
			return nil, errors.New("delivery transport denied an unapproved gateway address")
		}
		return dial(ctx, network, address)
	}
}

func newDeliveryClient(endpoint, caFile string) (*http.Client, error) {
	origin, err := deliveryOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	roots, err := deliveryRoots(caFile)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DialContext: deliveryOriginDial(origin, dialer.DialContext),
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
	endpoint, err := app.ValidateDeliveryGateway(config.SlackEndpoint)
	if err != nil {
		return nil, err
	}
	if err = validateDeliveryClient(config.DeliveryClient, endpoint); err != nil {
		return nil, err
	}
	origin, err := deliveryOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	transport := config.DeliveryClient.Transport.(*http.Transport).Clone()
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
	}
	if config.DeliveryCAFile != "" {
		roots, err := deliveryRoots(config.DeliveryCAFile)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	transport.DialContext = deliveryOriginDial(origin, dial)
	client := *config.DeliveryClient
	client.Transport = transport
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil
	return &client, nil
}
