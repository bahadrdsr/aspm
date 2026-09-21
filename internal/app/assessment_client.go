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
	"sync"
	"sync/atomic"
	"time"
)

func assessmentNativeClient(client *http.Client) (*http.Client, error) {
	if err := validateSourceNativeClient(client); err != nil {
		return nil, errors.New("assessment worker requires an explicit bounded direct verified-TLS client without cookies")
	}
	transport := client.Transport.(*http.Transport)
	if transport.Dial != nil && transport.DialContext == nil {
		return nil, errors.New("assessment worker requires a context-aware dialer")
	}
	if policy := transport.TLSClientConfig; policy != nil && policy.MaxVersion > tls.VersionTLS13 {
		return nil, errors.New("assessment worker requires a supported TLS version range")
	}
	owned := transport.Clone()
	if owned.TLSClientConfig == nil {
		owned.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		owned.TLSClientConfig = owned.TLSClientConfig.Clone()
		if owned.TLSClientConfig.RootCAs != nil {
			owned.TLSClientConfig.RootCAs = owned.TLSClientConfig.RootCAs.Clone()
		}
		if owned.TLSClientConfig.ClientCAs != nil {
			owned.TLSClientConfig.ClientCAs = owned.TLSClientConfig.ClientCAs.Clone()
		}
		if owned.TLSClientConfig.MinVersion == 0 {
			owned.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
	}
	copy := *client
	copy.Transport, copy.Jar = owned, nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy, nil
}

type assessmentHTTPState struct {
	called, received, interrupted, timedOut atomic.Bool
}

type assessmentTransport struct {
	base        *url.URL
	inner       *http.Transport
	state       *assessmentHTTPState
	ctx         context.Context
	deadline    time.Time
	cancelDials context.CancelFunc
	mu          sync.Mutex
	closed      bool
	closeFailed bool
	connections map[*assessmentConn]struct{}
	dials       sync.WaitGroup
	closeOnce   sync.Once
}

func (t *assessmentTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	if err := t.ioError(); err != nil {
		return nil, err
	}
	basePath := strings.TrimRight(t.base.Path, "/")
	if request.Method != http.MethodPost || request.URL.Scheme != t.base.Scheme ||
		request.URL.Host != t.base.Host || request.URL.User != nil ||
		request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.Fragment != "" ||
		(request.URL.Path != basePath && !strings.HasPrefix(request.URL.Path, basePath+"/")) ||
		request.Body == nil || request.Body == http.NoBody ||
		!t.state.called.CompareAndSwap(false, true) {
		return nil, errors.New("assessment dispatch denied a different destination or another attempt")
	}
	// The inner HTTP/2 transport can retry inside ONE RoundTrip call. Remove
	// rewind authority on our copy, not the caller's request or provider client.
	single := request.Clone(request.Context())
	single.GetBody = nil
	response, err := t.inner.RoundTrip(single)
	if err != nil {
		t.state.noteError(err)
	}
	if response != nil {
		t.state.received.Store(true)
		response.Body = &assessmentBody{ReadCloser: response.Body, state: t.state}
	}
	return response, err
}

func (t *assessmentTransport) ioError() error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(t.deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

type assessmentConn struct {
	net.Conn
	owner *assessmentTransport
	once  sync.Once
	err   error
}

func (c *assessmentConn) Read(data []byte) (int, error) {
	if err := c.owner.ioError(); err != nil {
		return 0, err
	}
	return c.Conn.Read(data)
}

func (c *assessmentConn) Write(data []byte) (int, error) {
	if err := c.owner.ioError(); err != nil {
		return 0, err
	}
	return c.Conn.Write(data)
}

func (c *assessmentConn) boundedDeadline(deadline time.Time) time.Time {
	if deadline.IsZero() || deadline.After(c.owner.deadline) {
		return c.owner.deadline
	}
	return deadline
}

func (c *assessmentConn) SetDeadline(deadline time.Time) error {
	return c.Conn.SetDeadline(c.boundedDeadline(deadline))
}

func (c *assessmentConn) SetReadDeadline(deadline time.Time) error {
	return c.Conn.SetReadDeadline(c.boundedDeadline(deadline))
}

func (c *assessmentConn) SetWriteDeadline(deadline time.Time) error {
	return c.Conn.SetWriteDeadline(c.boundedDeadline(deadline))
}

func (c *assessmentConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.connections, c)
		if c.err != nil && !errors.Is(c.err, net.ErrClosed) {
			c.owner.closeFailed = true
		}
		c.owner.mu.Unlock()
	})
	return c.err
}

func (t *assessmentTransport) dial(ctx context.Context, network, address string, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, context.Canceled
	}
	t.dials.Add(1)
	t.mu.Unlock()
	defer t.dials.Done()
	if err := t.ioError(); err != nil {
		return nil, err
	}
	// Transport may detach dialing from a request's cancellation to populate
	// its pool. This attempt owns its dials and cannot extend its fixed budget.
	bounded, cancel := context.WithDeadline(ctx, t.deadline)
	stop := context.AfterFunc(t.ctx, cancel)
	defer func() { stop(); cancel() }()
	conn, err := dial(bounded, network, address)
	if conn == nil {
		if err == nil {
			err = errors.New("assessment transport returned no connection")
		}
		return nil, err
	}
	owned := &assessmentConn{Conn: conn, owner: t}
	if err != nil {
		_ = owned.Close()
		return nil, err
	}
	// This absolute socket deadline also fences a late/resumed TLS write,
	// rather than relying solely on cancellation callbacks being scheduled.
	if err = owned.SetDeadline(t.deadline); err != nil {
		_ = owned.Close()
		return nil, err
	}
	t.mu.Lock()
	if t.closed || t.ioError() != nil || bounded.Err() != nil {
		t.mu.Unlock()
		_ = owned.Close()
		return nil, context.Canceled
	}
	t.connections[owned] = struct{}{}
	t.mu.Unlock()
	return owned, nil
}

func (t *assessmentTransport) close() bool {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		connections := make([]*assessmentConn, 0, len(t.connections))
		for conn := range t.connections {
			connections = append(connections, conn)
		}
		t.mu.Unlock()
		t.cancelDials()
		t.inner.CloseIdleConnections()
		for _, conn := range connections {
			_ = conn.Close()
		}
		// A late dial is closed before returning to Transport. Wait for those
		// real Close calls too; no SQL connection is owned during this cleanup.
		t.dials.Wait()
	})
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closeFailed
}

func (s *assessmentHTTPState) noteError(err error) {
	if err != nil && !errors.Is(err, io.EOF) {
		s.interrupted.Store(true)
		var timeout net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
			s.timedOut.Store(true)
		}
	}
}

type assessmentBody struct {
	io.ReadCloser
	state *assessmentHTTPState
}

func (b *assessmentBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	b.state.noteError(err)
	return n, err
}

func (b *assessmentBody) Close() error {
	err := b.ReadCloser.Close()
	b.state.noteError(err)
	return err
}

func (w *AssessmentWorker) dispatchClient(ctx context.Context, destination string, state *assessmentHTTPState) (*http.Client, *assessmentTransport, error) {
	base, err := url.Parse(destination)
	if err != nil {
		return nil, nil, errUnavailable
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return nil, nil, errUnavailable
	}
	dials, cancel := context.WithCancel(ctx)
	transport := &assessmentTransport{base: base, inner: w.client.Transport.(*http.Transport).Clone(),
		state: state, ctx: dials, deadline: deadline, cancelDials: cancel,
		connections: make(map[*assessmentConn]struct{})}
	dial := transport.inner.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	transport.inner.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return transport.dial(ctx, network, address, dial)
	}
	context.AfterFunc(ctx, func() { transport.close() })
	copy := *w.client
	copy.Transport = transport
	return &copy, transport, nil
}
