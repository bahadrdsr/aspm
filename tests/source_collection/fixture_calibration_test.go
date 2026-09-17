//go:build integration && source_collection_fixture_calibration

package source_collection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/bahadrdsr/aspm/internal/app"
)

func calibrationClient(t *testing.T, address string) *http.Client {
	t.Helper()
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: false,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			if target != address {
				t.Error("calibration tried to dial a non-owned address")
				return nil, errors.New("owned calibration boundary")
			}
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
		}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("calibration redirect denied") }}
}

func calibrationTap(t *testing.T, receiver string, readOnly bool) (*fixture, *storageTap) {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), allowed: map[string]bool{},
		selected: app.StorageConfig{Endpoint: receiver, Bucket: "owned-calibration",
			Prefix: "source-evidence/owned/", Region: "us-east-1", Timeout: 4 * time.Second}}
	return f, f.tap(readOnly)
}

func TestSourceCollectionPUTFixtureCalibrationA1(t *testing.T) {
	t.Run("old-unread-HTTP1-hold-is-not-cancellation-observation", func(t *testing.T) {
		arrived, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(arrived)
			select {
			case <-r.Context().Done():
				close(canceled)
			case <-release:
			}
		}))
		defer server.Close()
		defer close(release)
		target, err := url.Parse(server.URL)
		must(t, "parse owned old-behavior calibration", err)
		client := calibrationClient(t, target.Host)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, "PUT", server.URL, bytes.NewReader([]byte("owned unread body")))
		must(t, "create old HTTP1 calibration request", err)
		done := make(chan error, 1)
		go func() {
			response, err := client.Do(request)
			if response != nil {
				response.Body.Close()
			}
			done <- err
		}()
		await(t, arrived, "old unread HTTP1 handler entry")
		cancel()
		select {
		case err := <-done:
			require(t, errors.Is(err, context.Canceled), "old-hold client did not actually cancel")
		case <-time.After(time.Second):
			t.Fatal("old-hold client cancellation exceeded its bound")
		}
		select {
		case <-canceled:
			t.Fatal("old unread HTTP1 hold unexpectedly observed cancellation before consuming its body")
		case <-time.After(200 * time.Millisecond):
		}
		t.Log("fixture calibration only: canceled HTTP1 client, unread server body, no server cancellation observation before release")
	})

	t.Run("amended-actual-gate-observes-HTTP1-cancellation", func(t *testing.T) {
		var forwarded atomic.Int32
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			forwarded.Add(1)
			w.WriteHeader(204)
		}))
		defer receiver.Close()
		_, tap := calibrationTap(t, receiver.URL, false)
		gate := tap.holdPut()
		t.Cleanup(gate.allow)
		client := calibrationClient(t, tap.server.Listener.Addr().String())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		body := []byte(" \t{\"owned\":\"exact\"}\r\n\x00\xff")
		request, err := http.NewRequestWithContext(ctx, "PUT", tap.server.URL+"/owned-calibration/source-evidence/owned/workspace/object?x-id=PutObject", bytes.NewReader(body))
		must(t, "create actual-gate cancellation calibration", err)
		hash := sha256.Sum256(body)
		payloadHash := hex.EncodeToString(hash[:])
		request.Header.Set("X-Amz-Content-Sha256", payloadHash)
		must(t, "sign owned cancellation request", v4.NewSigner().SignHTTP(ctx,
			aws.Credentials{AccessKeyID: "SYNTHETIC" + nonce(t), SecretAccessKey: secret(t)}, request,
			payloadHash, "s3", "us-east-1", time.Now().UTC()))
		done := make(chan error, 1)
		go func() {
			response, err := client.Do(request)
			if response != nil {
				response.Body.Close()
			}
			done <- err
		}()
		await(t, gate.arrived, "actual gate after bounded body consumption")
		cancel()
		select {
		case err := <-done:
			require(t, errors.Is(err, context.Canceled), "actual-gate client did not cancel")
		case <-time.After(time.Second):
			t.Fatal("actual-gate client cancellation exceeded its bound")
		}
		await(t, gate.done, "actual HTTP1 gate server cancellation")
		require(t, forwarded.Load() == 0, "canceled held PUT reached the forwarding receiver")
		t.Log("fixture calibration only: actual amended gate observed context cancellation without forwarding")
	})

	t.Run("released-signed-PUT-forwards-exact-body-and-original-headers", func(t *testing.T) {
		type received struct {
			Method, Host, URI string
			ContentLength     int64
			Header            http.Header
			Body              []byte
		}
		got := make(chan received, 1)
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, err := io.ReadAll(io.LimitReader(r.Body, 4097))
			if err != nil || len(data) > 4096 {
				t.Error("owned exact receiver could not read its bounded body")
				http.Error(w, "invalid owned body", 400)
				return
			}
			got <- received{r.Method, r.Host, r.URL.RequestURI(), r.ContentLength, r.Header.Clone(), data}
			w.WriteHeader(204)
		}))
		defer receiver.Close()
		f, tap := calibrationTap(t, receiver.URL, false)
		gate := tap.holdPut()
		t.Cleanup(gate.allow)
		client := calibrationClient(t, tap.server.Listener.Addr().String())
		body := []byte(" \t{\"native\":\"signed exact bytes\"}\r\n\x00\xff")
		request, err := http.NewRequest("PUT", tap.server.URL+"/owned-calibration/source-evidence/owned/workspace/signed-object?x-id=PutObject", bytes.NewReader(body))
		must(t, "create owned signed forwarding request", err)
		sum := sha256.Sum256(body)
		payloadHash := hex.EncodeToString(sum[:])
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("X-Amz-Content-Sha256", payloadHash)
		request.Header.Set("X-Amz-Meta-Owned", "exact signed metadata")
		must(t, "sign with generated synthetic credentials", v4.NewSigner().SignHTTP(context.Background(),
			aws.Credentials{AccessKeyID: "SYNTHETIC" + nonce(t), SecretAccessKey: secret(t), SessionToken: secret(t)},
			request, payloadHash, "s3", "us-east-1", time.Now().UTC()))
		expectedHeaders := request.Header.Clone()
		expectedHost, expectedURI, expectedLength := request.URL.Host, request.URL.RequestURI(), request.ContentLength
		response := make(chan *http.Response, 1)
		failure := make(chan error, 1)
		go func() {
			r, err := client.Do(request)
			if err != nil {
				failure <- err
				return
			}
			response <- r
		}()
		await(t, gate.arrived, "signed body preserved before hold")
		select {
		case <-got:
			t.Fatal("held PUT forwarded before explicit release")
		default:
		}
		gate.allow()
		var result *http.Response
		select {
		case result = <-response:
			defer result.Body.Close()
		case <-failure:
			t.Fatal("released actual signed PUT failed; private details withheld")
		case <-time.After(time.Second):
			t.Fatal("released actual signed PUT did not return")
		}
		require(t, result.StatusCode == 204, "proxy fabricated/replaced the exact receiver's status")
		var actual received
		select {
		case actual = <-got:
		case <-time.After(time.Second):
			t.Fatal("released PUT did not reach the actual owned receiver")
		}
		require(t, actual.Method == "PUT" && actual.Host == expectedHost && actual.URI == expectedURI &&
			actual.ContentLength == expectedLength && bytes.Equal(actual.Body, body),
			"actual gate changed signed body/Host/path/query/ContentLength")
		for name, values := range expectedHeaders {
			require(t, reflect.DeepEqual(actual.Header.Values(name), values), "original signed/request header was changed by body preservation")
		}
		require(t, len(f.violations) == 0, "valid signed forwarding generated a fixture violation")
		t.Log("fixture calibration only: released signed body, Host, path, query, ContentLength and original headers stayed byte-exact; receiver status relayed")
	})

	t.Run("denied-capability-does-not-consume-body-or-forward", func(t *testing.T) {
		var forwarded atomic.Int32
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			forwarded.Add(1)
			w.WriteHeader(204)
		}))
		defer receiver.Close()
		for _, row := range []struct {
			name, method, path string
			readOnly           bool
		}{
			{"read-only-PUT", "PUT", "/owned-calibration/source-evidence/owned/workspace/object", true},
			{"foreign-prefix", "PUT", "/owned-calibration/not-owned/object", false},
			{"unapproved-query", "PUT", "/owned-calibration/source-evidence/owned/workspace/object?not-authorized=1", false},
			{"unapproved-method", "DELETE", "/owned-calibration/source-evidence/owned/workspace/object", false},
		} {
			t.Run(row.name, func(t *testing.T) {
				f, tap := calibrationTap(t, receiver.URL, row.readOnly)
				body := &calibrationBody{data: []byte("must not be consumed")}
				request := httptest.NewRequest(row.method, tap.server.URL+row.path, nil)
				request.Body, request.ContentLength = body, int64(len(body.data))
				reply := httptest.NewRecorder()
				tap.server.Config.Handler.ServeHTTP(reply, request)
				require(t, reply.Code == 403 && body.reads == 0 && forwarded.Load() == 0 && len(f.violations) == 1,
					"capability denial reached body consumption or forwarding")
			})
		}
	})

	t.Run("read-error-and-oversize-are-explicit-fixture-failures", func(t *testing.T) {
		var forwarded atomic.Int32
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			forwarded.Add(1)
			w.WriteHeader(204)
		}))
		defer receiver.Close()
		for _, oversized := range []bool{false, true} {
			f, tap := calibrationTap(t, receiver.URL, false)
			gate := tap.holdPut()
			t.Cleanup(gate.allow)
			body := &calibrationBody{err: errors.New("owned read failure")}
			request := httptest.NewRequest("PUT", tap.server.URL+"/owned-calibration/source-evidence/owned/workspace/object", nil)
			request.Body, request.ContentLength = body, 1
			status := 400
			if oversized {
				request.ContentLength = (32 << 20) + 1
				status = 413
			}
			response := httptest.NewRecorder()
			tap.server.Config.Handler.ServeHTTP(response, request)
			require(t, response.Code == status && len(f.violations) == 1 && forwarded.Load() == 0,
				"read error/oversize produced a success-shaped fixture fallback")
			select {
			case <-gate.arrived:
				t.Fatal("invalid body signaled a ready valid PUT")
			default:
			}
			await(t, gate.done, "invalid-body fixture completion")
			if oversized {
				require(t, body.reads == 0, "declared oversized body was consumed")
			}
		}
	})
}

type calibrationBody struct {
	data  []byte
	err   error
	reads int
}

func (r *calibrationBody) Read(buffer []byte) (int, error) {
	r.reads++
	if r.err != nil {
		return 0, r.err
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, r.data)
	r.data = r.data[n:]
	return n, nil
}
func (*calibrationBody) Close() error { return nil }
