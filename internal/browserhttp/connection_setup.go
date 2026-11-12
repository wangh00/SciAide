package browserhttp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

// connectionSetup bounds only connection establishment (TCP, proxy, TLS).
// Once handed to a request, the connection has no setup deadline: SSE bodies
// and reused connections remain governed by the normal transport semantics.
type connectionSetup struct {
	mu      sync.Mutex
	timeout time.Duration
	pending map[*setupConn]struct{}
}
type setupRequest struct {
	manager   *connectionSetup
	ctx       context.Context
	cancelled bool
}
type setupKey struct{}
type setupConn struct {
	net.Conn
	manager *connectionSetup
	owner   *setupRequest
}

func setupTimeout(base *http.Transport) time.Duration {
	if base.TLSHandshakeTimeout > 0 {
		return base.TLSHandshakeTimeout
	}
	if base.ResponseHeaderTimeout > 0 {
		return base.ResponseHeaderTimeout
	}
	return 30 * time.Second
}
func newConnectionSetup(timeout time.Duration) *connectionSetup {
	return &connectionSetup{timeout: timeout, pending: make(map[*setupConn]struct{})}
}
func (c *setupConn) Close() error {
	c.manager.mu.Lock()
	delete(c.manager.pending, c)
	c.manager.mu.Unlock()
	return c.Conn.Close()
}

func (m *connectionSetup) dial(original func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	if original == nil {
		original = (&net.Dialer{Timeout: m.timeout, KeepAlive: 30 * time.Second}).DialContext
	}
	return func(ctx context.Context, n, addr string) (net.Conn, error) {
		owner, _ := ctx.Value(setupKey{}).(*setupRequest)
		parent := ctx
		if owner != nil {
			parent = owner.ctx
		}
		start := time.Now()
		dialCtx, cancel := context.WithTimeout(parent, m.timeout)
		defer cancel()
		conn, err := original(dialCtx, n, addr)
		if err != nil {
			return nil, err
		}
		if err = dialCtx.Err(); err != nil {
			conn.Close()
			return nil, err
		}
		if err = conn.SetDeadline(start.Add(m.timeout)); err != nil {
			conn.Close()
			return nil, err
		}
		c := &setupConn{Conn: conn, manager: m, owner: owner}
		m.mu.Lock()
		denied := owner != nil && owner.cancelled
		if !denied {
			m.pending[c] = struct{}{}
		}
		m.mu.Unlock()
		if denied {
			conn.Close()
			if err := parent.Err(); err != nil {
				return nil, err
			}
			return nil, context.Canceled
		}
		return c, nil
	}
}
func (m *connectionSetup) finish(conn net.Conn) {
	// crypto/tls and req/v3's uTLS both expose the underlying socket this way.
	for i := 0; i < 16 && conn != nil; i++ {
		if c, ok := conn.(*setupConn); ok {
			m.mu.Lock()
			if _, pending := m.pending[c]; pending {
				_ = c.Conn.SetDeadline(time.Time{})
				delete(m.pending, c)
			}
			m.mu.Unlock()
			return
		}
		wrapped, ok := conn.(interface{ NetConn() net.Conn })
		if !ok {
			return
		}
		conn = wrapped.NetConn()
	}
}
func (s *setupRequest) cancelPending() {
	m := s.manager
	m.mu.Lock()
	s.cancelled = true
	for c := range m.pending {
		if c.owner == s {
			// Serialize physical close with GotConn claim. A different request
			// must not claim a removed entry before its socket is closed.
			_ = c.Conn.Close()
			delete(m.pending, c)
		}
	}
	m.mu.Unlock()
}
func (m *connectionSetup) roundTrip(r *http.Request, base http.RoundTripper) (*http.Response, error) {
	state := &setupRequest{manager: m}
	ctx := context.WithValue(r.Context(), setupKey{}, state)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { m.finish(info.Conn) }})
	state.ctx = ctx
	stop := context.AfterFunc(r.Context(), state.cancelPending)
	defer func() { stop(); state.cancelPending() }()
	return base.RoundTrip(r.WithContext(ctx))
}
