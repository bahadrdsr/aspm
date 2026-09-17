//go:build integration

package source_collection

import (
	"context"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

type collectionTraceKey struct{}
type commitGate struct {
	table            string
	arrived, release chan struct{}
	once             sync.Once
	releaseOnce      sync.Once
	mu               sync.Mutex
	pid              uint32
	transaction      bool
}

func newCommitGate(table string) *commitGate {
	return &commitGate{table: strings.ToLower(table), arrived: make(chan struct{}), release: make(chan struct{})}
}
func (g *commitGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := strings.ToLower(strings.Join(strings.Fields(data.SQL), " "))
	if strings.Contains(sql, g.table) && strings.HasPrefix(sql, "insert ") {
		return context.WithValue(ctx, collectionTraceKey{}, true)
	}
	return ctx
}
func (g *commitGate) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	marked, _ := ctx.Value(collectionTraceKey{}).(bool)
	if !marked || data.Err != nil || data.CommandTag.RowsAffected() != 1 {
		return
	}
	g.once.Do(func() {
		g.mu.Lock()
		g.pid, g.transaction = conn.PgConn().PID(), conn.PgConn().TxStatus() == 'T'
		g.mu.Unlock()
		close(g.arrived)
		select {
		case <-g.release:
		case <-ctx.Done():
		}
	})
}
func (g *commitGate) liveTransaction() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pid != 0 && g.transaction
}
func (g *commitGate) allow() { g.releaseOnce.Do(func() { close(g.release) }) }
