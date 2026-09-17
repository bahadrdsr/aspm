package app

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

const sourceEvidenceLimit int64 = 32 << 20

type sourceEvidenceReader struct {
	reader *evidence.Reader
	config StorageConfig
}

func sourceOrigin(endpoint string) (string, error) {
	target, err := url.Parse(endpoint)
	if err != nil || target.Hostname() == "" {
		return "", errors.New("invalid source endpoint")
	}
	port := target.Port()
	if port == "" {
		port = "443"
		if target.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(target.Hostname()), port), nil
}

func sourceDirectTransport(endpoint string) (*http.Transport, error) {
	origin, err := sourceOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 5 * time.Second, IdleConnTimeout: 30 * time.Second,
		MaxIdleConns: 2, MaxIdleConnsPerHost: 2,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if !strings.EqualFold(address, origin) {
				return nil, errors.New("source storage destination is outside the selected origin")
			}
			return dialer.DialContext(ctx, network, address)
		},
	}, nil
}

func openSourceEvidenceReader(ctx context.Context, config StorageConfig) (*sourceEvidenceReader, error) {
	config, err := normalizedStorageConfig(config)
	if err != nil {
		return nil, err
	}
	transport, err := sourceDirectTransport(config.Endpoint)
	if err != nil {
		return nil, err
	}
	defer transport.CloseIdleConnections()
	reader, err := evidence.OpenReaderWithTransport(ctx, evidenceConfig(config), transport)
	if err != nil {
		return nil, err
	}
	config.AccessKey, config.SecretKey = "", ""
	return &sourceEvidenceReader{reader: reader, config: config}, nil
}

func (reader *sourceEvidenceReader) read(ctx context.Context, workspace string, ref evidence.Ref) ([]byte, error) {
	if ref.SizeBytes < 0 || ref.SizeBytes > sourceEvidenceLimit {
		return nil, errUnavailable
	}
	body, err := reader.reader.Open(ctx, workspace, ref)
	if err != nil {
		return nil, errUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(body, ref.SizeBytes+1))
	if err = errors.Join(err, body.Close()); err != nil {
		return nil, errUnavailable
	}
	return data, nil
}

func (reader *sourceEvidenceReader) close() {
	if reader != nil {
		_ = reader.reader.Close()
	}
}
