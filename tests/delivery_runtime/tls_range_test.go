//go:build integration

package delivery_runtime

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeliveryRuntimeEffectiveTLSVersionRangePreflightA1(t *testing.T) {
	require(t, Production.Run != nil, "real service.Run delivery binding missing")
	for _, row := range []struct {
		name        string
		min, max    uint16
		allowClient bool
	}{
		{"contradictory-supported-bounds", tls.VersionTLS13, tls.VersionTLS12, false},
		{"future-minimum-default-maximum", tls.VersionTLS13 + 1, 0, false},
		{"future-only-explicit-range", tls.VersionTLS13 + 1, tls.VersionTLS13 + 2, false},
		{"existing-lower-floor-denial", tls.VersionTLS11, tls.VersionTLS13, false},
		{"existing-upper-floor-denial", 0, tls.VersionTLS11, false},
		{"effective-default-bounds", 0, 0, true},
		{"TLS12-only", tls.VersionTLS12, tls.VersionTLS12, true},
		{"TLS13-only", tls.VersionTLS13, tls.VersionTLS13, true},
		{"TLS12-through-TLS13", tls.VersionTLS12, tls.VersionTLS13, true},
		{"default-minimum-TLS12-maximum", 0, tls.VersionTLS12, true},
		{"TLS13-minimum-default-maximum", tls.VersionTLS13, 0, true},
		{"supported-overlap-with-future-upper", tls.VersionTLS12, tls.VersionTLS13 + 1, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			database, err := net.Listen("tcp", "127.0.0.1:0")
			must(t, "open owned DB progression tripwire", err)
			var databaseCalls atomic.Int32
			databaseDone := make(chan struct{})
			go func() {
				defer close(databaseDone)
				for {
					connection, acceptErr := database.Accept()
					if acceptErr != nil {
						return
					}
					databaseCalls.Add(1)
					connection.Close()
				}
			}()
			t.Cleanup(func() { database.Close(); <-databaseDone })
			reserved, err := net.Listen("tcp", "127.0.0.1:0")
			must(t, "reserve owned listen-conflict tripwire", err)
			t.Cleanup(func() { reserved.Close() })
			token := secret(t)
			native := newNative(t, token, false)
			transport := native.client.Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.MinVersion = row.min
			transport.TLSClientConfig.MaxVersion = row.max
			t.Cleanup(transport.CloseIdleConnections)
			client := *native.client
			client.Transport = transport
			key := random(t, 32)
			config := roleConfig{
				Database: databaseConfig{
					URL:    "postgres://synthetic:" + id(t) + "@" + database.Addr().String() + "/fixture?sslmode=disable",
					Schema: "delivery_tls_range_a1", ApplicationName: "delivery-tls-range-a1", MaxConnections: 1,
				},
				Listen: reserved.Addr().String(), WorkerID: "owned-tls-range-a1", EncryptionKey: key,
				LeaseDuration: 15 * time.Second, SlackEndpoint: native.server.URL, Client: &client,
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			runErr := Production.Run(ctx, config)
			database.Close()
			<-databaseDone
			t.Logf("A1 effective TLS preflight: min=0x%04x max=0x%04x allow=%t DB-connects=%d provider-requests=%d",
				row.min, row.max, row.allowClient, databaseCalls.Load(), native.count.Load())
			require(t, runErr != nil && !errors.Is(runErr, context.DeadlineExceeded) && ctx.Err() == nil,
				"range preflight/progression did not finish within the owned bound")
			message := strings.ToLower(runErr.Error())
			require(t, !strings.Contains(runErr.Error(), config.Database.URL) &&
				!strings.Contains(runErr.Error(), token) &&
				!strings.Contains(runErr.Error(), base64.StdEncoding.EncodeToString(key)) &&
				!strings.Contains(message, hex.EncodeToString(key)),
				"effective TLS range failure exposed supplied private input")
			require(t, native.count.Load() == 0, "preflight-only TLS range test reached native HTTP")
			if row.allowClient {
				require(t, databaseCalls.Load() > 0 && !strings.Contains(message, "tls") && !strings.Contains(message, "listen"),
					"supported effective TLS range was rejected before legitimate DB preflight progression")
			} else {
				if !strings.Contains(message, "tls") {
					t.Error("invalid effective TLS version range needs a TLS-specific safe preflight error")
				}
				if databaseCalls.Load() != 0 {
					t.Error("invalid effective TLS version range reached the owned DB tripwire before rejection")
				}
			}
		})
	}
}
