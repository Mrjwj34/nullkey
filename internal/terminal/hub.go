// Package terminal owns sandbox PTY attachment: one-time tickets, a single
// writable WebSocket client per PTY, attach/detach, and resize/SIGWINCH.
//
// No server-side scrollback is kept. When no client is attached output is
// drained and discarded so the harness never blocks on a full PTY buffer.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	nkid "github.com/Mrjwj34/nullkey/internal/id"
)

// ErrNotReady is returned when a sandbox has no writable PTY yet.
var ErrNotReady = errors.New("terminal is not ready")

// ErrTicketInvalid is returned for missing, expired, reused or unknown tickets.
var ErrTicketInvalid = errors.New("terminal ticket is invalid")

// Source is a PTY managed by the runtime.
type Source interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Close() error
	Resize(cols, rows int) error
	Size() (cols, rows int)
	Done() <-chan struct{}
	ExitCode() int
}

type ticket struct {
	sandboxID string
	expires   time.Time
}

// Hub tracks terminal sessions and one-time tickets.
type Hub struct {
	log      *slog.Logger
	mu       sync.Mutex
	sessions map[string]*Session
	tickets  map[string]ticket
	ttl      time.Duration
}

// NewHub builds a terminal hub.
func NewHub(logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		log:      logger,
		sessions: make(map[string]*Session),
		tickets:  make(map[string]ticket),
		ttl:      30 * time.Second,
	}
}

// Open registers a PTY for a sandbox and starts its output pump. A terminal id
// is generated once and remains stable across reconnects.
func (h *Hub) Open(sandboxID string, source Source) string {
	terminalID := nkid.New(nkid.Terminal)
	s := &Session{
		ID:         terminalID,
		SandboxID:  sandboxID,
		terminalID: terminalID,
		hub:        h,
		source:     source,
		exited:     make(chan struct{}),
	}
	h.mu.Lock()
	old := h.sessions[sandboxID]
	// Install the new session before shutting down the old one, and never call
	// shutdown while holding h.mu: finish() also takes h.mu, so doing both would
	// deadlock when the old PTY exits concurrently (Open waits exitOnce, finish
	// waits h.mu).
	h.sessions[sandboxID] = s
	h.mu.Unlock()
	if old != nil {
		old.shutdown()
	}
	go s.pump()
	go s.wait()
	return terminalID
}

// Close removes a session, closes the PTY and disconnects any client.
func (h *Hub) Close(sandboxID string) {
	h.mu.Lock()
	s := h.sessions[sandboxID]
	delete(h.sessions, sandboxID)
	h.mu.Unlock()
	if s != nil {
		s.shutdown()
	}
}

// Has reports whether a sandbox has a live terminal.
func (h *Hub) Has(sandboxID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.sessions[sandboxID]
	return ok
}

// TerminalID returns the stable terminal id for a sandbox.
func (h *Hub) TerminalID(sandboxID string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sandboxID]
	if !ok {
		return "", false
	}
	return s.terminalID, true
}

// Mint creates a one-time ticket bound to a sandbox.
func (h *Hub) Mint(sandboxID string) (string, time.Time, error) {
	h.mu.Lock()
	_, ok := h.sessions[sandboxID]
	h.mu.Unlock()
	if !ok {
		return "", time.Time{}, ErrNotReady
	}
	raw, err := nkid.Random(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(h.ttl)
	h.mu.Lock()
	now := time.Now().UTC()
	for key, existing := range h.tickets {
		if now.After(existing.expires) {
			delete(h.tickets, key)
		}
	}
	h.tickets[raw] = ticket{sandboxID: sandboxID, expires: expires}
	h.mu.Unlock()
	return raw, expires, nil
}

// Consume validates and invalidates a ticket.
func (h *Hub) Consume(raw string) (sandboxID string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrTicketInvalid
	}
	h.mu.Lock()
	t, ok := h.tickets[raw]
	delete(h.tickets, raw)
	h.mu.Unlock()
	if !ok {
		return "", ErrTicketInvalid
	}
	if time.Now().UTC().After(t.expires) {
		return "", ErrTicketInvalid
	}
	return t.sandboxID, nil
}

// Session is a PTY attachment. Exactly one client may be writable at a time.
type Session struct {
	ID         string
	SandboxID  string
	terminalID string
	hub        *Hub
	source     Source

	mu     sync.Mutex
	client *client
	closed bool

	exited   chan struct{}
	exitOnce sync.Once
}

func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.source.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			c := s.client
			s.mu.Unlock()
			if c != nil && !c.sendOutput(serverMessage{Type: "output", Data: string(chunk)}) {
				// The client is stuck, not merely busy: detach it without
				// killing the PTY and go back to draining the output.
				s.hub.log.Warn("terminal_client_detached", "sandbox_id", s.SandboxID, "terminal_id", s.terminalID)
				c.close()
				s.detach(c)
			}
		}
		if err != nil {
			// PTY EIO means the child is gone even if cmd.Wait has not returned
			// yet (a known macOS PTY behavior while the master stays open).
			s.finish(-1)
			return
		}
	}
}

func (s *Session) wait() {
	<-s.source.Done()
	s.finish(s.source.ExitCode())
}

// finish closes the terminal session exactly once, notifies the current client
// and removes the session from the hub so no further tickets are minted.
func (s *Session) finish(code int) {
	s.exitOnce.Do(func() {
		s.mu.Lock()
		c := s.client
		s.client = nil
		s.closed = true
		s.mu.Unlock()
		if c != nil {
			c.trySend(serverMessage{Type: "exit", Code: &code})
			c.close()
		}
		close(s.exited)
		s.hub.mu.Lock()
		if current := s.hub.sessions[s.SandboxID]; current == s {
			delete(s.hub.sessions, s.SandboxID)
		}
		s.hub.mu.Unlock()
	})
}

func (s *Session) attach(c *client) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrNotReady
	}
	old := s.client
	s.client = c
	s.mu.Unlock()
	if old != nil && old != c {
		old.trySend(serverMessage{Type: "error", Error: &protocolError{Code: "terminal_replaced", Message: "terminal attached by another client"}})
		old.close()
	}
	cols, rows := s.source.Size()
	if cols <= 0 {
		cols, rows = 120, 40
	}
	// Always resize on attach, even to the same size, to deliver SIGWINCH and
	// make the TUI redraw. No scrollback is replayed.
	_ = s.source.Resize(cols, rows)
	return nil
}

func (s *Session) detach(c *client) {
	s.mu.Lock()
	if s.client == c {
		s.client = nil
	}
	s.mu.Unlock()
}

func (s *Session) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	c := s.client
	s.client = nil
	s.mu.Unlock()
	_ = s.source.Close()
	if c != nil {
		c.close()
	}
	s.exitOnce.Do(func() { close(s.exited) })
}

// ServeWS upgrades an HTTP request to a terminal WebSocket. Authentication is
// the one-time ticket, not the daemon token.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	ticket := bearerOrQuery(r)
	sandboxID, err := h.Consume(ticket)
	if err != nil {
		http.Error(w, `{"error":{"code":"ticket_invalid","message":"terminal ticket is invalid"}}`, http.StatusUnauthorized)
		return
	}
	h.mu.Lock()
	s := h.sessions[sandboxID]
	h.mu.Unlock()
	if s == nil {
		http.Error(w, `{"error":{"code":"ticket_invalid","message":"terminal is not available"}}`, http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"127.0.0.1:*", "localhost:*", "http://127.0.0.1:*", "http://localhost:*"},
	})
	if err != nil {
		return
	}
	c := newClient(conn)
	if err := s.attach(c); err != nil {
		// No writer loop exists yet, so close the hijacked connection directly;
		// otherwise the client waits forever on a socket nobody serves.
		_ = conn.Close(websocket.StatusGoingAway, "terminal_not_ready")
		return
	}
	// Send ready synchronously so the client can never observe output before
	// the terminal id and readiness contract.
	if err := writeMessage(context.Background(), conn, serverMessage{Type: "ready", TerminalID: s.terminalID}); err != nil {
		s.detach(c)
		c.close()
		return
	}
	go c.writeLoop()
	defer func() {
		s.detach(c)
		c.close()
	}()
	for {
		typ, raw, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg clientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			if msg.Data != "" {
				s.writeInput(c, []byte(msg.Data))
			}
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = s.source.Resize(msg.Cols, msg.Rows)
			}
		case "ping":
			c.trySend(serverMessage{Type: "pong"})
		}
	}
}

// writeInput writes the whole payload. A PTY master accepts only what fits in
// the tty input queue, so a single Write can be short and lose the tail of a
// bulk paste. Input that cannot be delivered is reported instead of dropped:
// a half-written command is worse than a visible failure.
func (s *Session) writeInput(c *client, data []byte) {
	for len(data) > 0 {
		n, err := s.source.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err == nil && n > 0 {
			continue
		}
		s.hub.log.Warn("terminal_input_failed", "sandbox_id", s.SandboxID, "terminal_id", s.terminalID, "remaining", len(data), "error", err)
		c.trySend(serverMessage{Type: "error", Error: &protocolError{
			Code:    "input_failed",
			Message: "terminal did not accept the input",
		}})
		return
	}
}

type clientMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type serverMessage struct {
	Type       string         `json:"type"`
	Data       string         `json:"data,omitempty"`
	TerminalID string         `json:"terminal_id,omitempty"`
	Code       *int           `json:"code,omitempty"`
	Error      *protocolError `json:"error,omitempty"`
}

// clientSendTimeout bounds how long the PTY pump waits for an attached client
// to take a chunk of output. Waiting is the point: it backpressures the child
// process the way a real terminal does, instead of losing a burst of output
// that can never be recovered because no scrollback is kept.
const clientSendTimeout = 10 * time.Second

type client struct {
	conn        *websocket.Conn
	send        chan serverMessage
	closed      chan struct{}
	once        sync.Once
	sendTimeout time.Duration
}

func newClient(conn *websocket.Conn) *client {
	return &client{
		conn:        conn,
		send:        make(chan serverMessage, 64),
		closed:      make(chan struct{}),
		sendTimeout: clientSendTimeout,
	}
}

// sendOutput queues PTY output, waiting while the client catches up. It
// reports false only when the client is closed or stays stuck for
// sendTimeout, which is when the pump gives up on it.
func (c *client) sendOutput(msg serverMessage) bool {
	select {
	case <-c.closed:
		return false
	case c.send <- msg:
		return true
	default:
	}
	timer := time.NewTimer(c.sendTimeout)
	defer timer.Stop()
	select {
	case c.send <- msg:
		return true
	case <-c.closed:
		return false
	case <-timer.C:
		return false
	}
}

func (c *client) trySend(msg serverMessage) bool {
	select {
	case <-c.closed:
		return false
	default:
	}
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

func (c *client) close() {
	c.once.Do(func() { close(c.closed) })
}

func (c *client) writeLoop() {
	defer c.conn.Close(websocket.StatusNormalClosure, "")
	for {
		select {
		case msg := <-c.send:
			if err := writeMessage(context.Background(), c.conn, msg); err != nil {
				return
			}
		case <-c.closed:
			for {
				select {
				case msg := <-c.send:
					if err := writeMessage(context.Background(), c.conn, msg); err != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func writeMessage(ctx context.Context, conn *websocket.Conn, msg serverMessage) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, raw)
}

func bearerOrQuery(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.Fields(h)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1]
		}
	}
	return r.URL.Query().Get("ticket")
}
