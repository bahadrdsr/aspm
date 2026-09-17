package evidence

import (
	"context"
	"io"
	"net/http"
)

// Reader exposes remote evidence reads without a write or bucket-probe API.
type Reader struct {
	store *Store
}

// OpenReader validates explicit configuration and builds a static-credential
// client without network access. Authorization is checked by actual object
// reads, not by a whole-bucket startup probe.
func OpenReader(ctx context.Context, config Config) (*Reader, error) {
	store, err := newStore(config)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return &Reader{store: store}, nil
}

// OpenReaderWithTransport owns a copy of an explicit transport, without probes or ambient fallback.
func OpenReaderWithTransport(ctx context.Context, config Config, transport *http.Transport) (*Reader, error) {
	if transport == nil {
		return nil, ErrInvalid
	}
	store, err := newStoreWithTransport(config, transport)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return &Reader{store: store}, nil
}

func (r *Reader) Open(ctx context.Context, workspace string, ref Ref) (io.ReadCloser, error) {
	return r.store.Open(ctx, workspace, ref)
}

func (r *Reader) Close() error { return r.store.Close() }
