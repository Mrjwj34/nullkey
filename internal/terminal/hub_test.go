package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ---------------------------------------------------------------- fake PTY

type resizeCall struct {
	cols int
	rows int
}

// fakeSource is a PTY stand-in backed by an io.Pipe. Tests write to the pipe
// writer to produce process output and read Write calls as terminal input.
type fakeSource struct {
	mu       sync.Mutex
	pr       *io.PipeReader
	pw       *io.PipeWriter
	input    strings.Builder
	resizes  []resizeCall
	cols     int
	rows     int
	closed   int
	exitCode int
	// writeLimit caps how many bytes one Write accepts, like a tty input queue.
	writeLimit int
	writeErr   error

	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
}

func newFakeSource(cols, rows int) *fakeSource {
	pr, pw := io.Pipe()
	return &fakeSource{
		pr:   pr,
		pw:   pw,
		cols: cols,
		rows: rows,
		done: make(chan struct{}),
	}
}

func (f *fakeSource) Read(p []byte) (int, error) { return f.pr.Read(p) }

func (f *fakeSource) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.writeLimit > 0 && f.writeLimit < len(p) {
		p = p[:f.writeLimit]
	}
	f.input.Write(p)
	return len(p), nil
}

func (f *fakeSource) setWriteLimit(limit int) {
	f.mu.Lock()
	f.writeLimit = limit
	f.mu.Unlock()
}

func (f *fakeSource) setWriteErr(err error) {
	f.mu.Lock()
	f.writeErr = err
	f.mu.Unlock()
}

func (f *fakeSource) Close() error {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closed++
		f.mu.Unlock()
		f.doneOnce.Do(func() { close(f.done) })
		_ = f.pr.Close()
		_ = f.pw.Close()
	})
	return nil
}

func (f *fakeSource) Resize(cols, rows int) error {
	f.mu.Lock()
	f.resizes = append(f.resizes, resizeCall{cols: cols, rows: rows})
	f.cols, f.rows = cols, rows
	f.mu.Unlock()
	return nil
}

func (f *fakeSource) Size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cols, f.rows
}

func (f *fakeSource) Done() <-chan struct{} { return f.done }

func (f *fakeSource) ExitCode() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exitCode
}

// finish reports process exit without closing the pipe, mirroring a PTY whose
// child has exited but whose file descriptor is still open.
func (f *fakeSource) finish(code int) {
	f.mu.Lock()
	f.exitCode = code
	f.mu.Unlock()
	f.doneOnce.Do(func() { close(f.done) })
}

// feed writes process output into the PTY, failing the test if the pump stops
// draining (which would block the pipe write forever).
func (f *fakeSource) feed(t *testing.T, data string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := f.pw.Write([]byte(data))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("feed PTY output: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PTY pump stopped draining output")
	}
}

func (f *fakeSource) inputText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.input.String()
}

func (f *fakeSource) resizeCalls() []resizeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]resizeCall(nil), f.resizes...)
}

func (f *fakeSource) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// ------------------------------------------------------------- test helpers

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func newTestClient(capacity int) *client {
	return &client{
		send:   make(chan serverMessage, capacity),
		closed: make(chan struct{}),
		// Short enough to keep tests fast; the production value is seconds.
		sendTimeout: 200 * time.Millisecond,
	}
}

func sessionFor(t *testing.T, h *Hub, sandboxID string) *Session {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sandboxID]
	if s == nil {
		t.Fatalf("no session for sandbox %q", sandboxID)
	}
	return s
}

func attachedClient(s *Session) *client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func attachClient(t *testing.T, s *Session, c *client) {
	t.Helper()
	if err := s.attach(c); err != nil {
		t.Fatalf("attach client: %v", err)
	}
}

func recvClientMessage(t *testing.T, c *client) serverMessage {
	t.Helper()
	select {
	case msg := <-c.send:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server message")
		return serverMessage{}
	}
}

// ------------------------------------------------------------- hub behavior

func TestNewHubDefaults(t *testing.T) {
	h := NewHub(nil)
	if h.log == nil {
		t.Fatal("NewHub(nil) left logger unset")
	}
	if h.ttl != 30*time.Second {
		t.Fatalf("ttl = %v, want 30s", h.ttl)
	}
	if h.sessions == nil || h.tickets == nil {
		t.Fatal("NewHub left a nil map")
	}
	if h.Has("sbx_missing") {
		t.Fatal("Has() true for unknown sandbox")
	}
	if _, ok := h.TerminalID("sbx_missing"); ok {
		t.Fatal("TerminalID() ok for unknown sandbox")
	}
}

func TestMintNoSession(t *testing.T) {
	h := NewHub(nil)
	raw, expires, err := h.Mint("sbx_missing")
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Mint error = %v, want ErrNotReady", err)
	}
	if raw != "" || !expires.IsZero() {
		t.Fatalf("Mint returned %q, %v on error", raw, expires)
	}
}

func TestMintAndConsume(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)

	raw, expires, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if raw == "" {
		t.Fatal("Mint returned an empty ticket")
	}
	ttl := time.Until(expires)
	if ttl < 25*time.Second || ttl > 31*time.Second {
		t.Fatalf("ticket lifetime = %v, want ~30s", ttl)
	}

	h.mu.Lock()
	tk, ok := h.tickets[raw]
	h.mu.Unlock()
	if !ok {
		t.Fatal("ticket was not stored")
	}
	if tk.sandboxID != "sbx_1" {
		t.Fatalf("ticket sandbox = %q, want sbx_1", tk.sandboxID)
	}

	// Tickets are trimmed before lookup and consumed exactly once.
	sandboxID, err := h.Consume("  " + raw + "\n\t")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if sandboxID != "sbx_1" {
		t.Fatalf("Consume sandbox = %q, want sbx_1", sandboxID)
	}
	if _, err := h.Consume(raw); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("second Consume error = %v, want ErrTicketInvalid", err)
	}
}

func TestConsumeRejectsBadTickets(t *testing.T) {
	h := NewHub(nil)
	for _, raw := range []string{"", "   ", "\t\n", "unknown-ticket"} {
		if _, err := h.Consume(raw); !errors.Is(err, ErrTicketInvalid) {
			t.Fatalf("Consume(%q) error = %v, want ErrTicketInvalid", raw, err)
		}
	}

	h.mu.Lock()
	h.tickets["expired-ticket"] = ticket{sandboxID: "sbx_1", expires: time.Now().UTC().Add(-time.Second)}
	h.mu.Unlock()
	if _, err := h.Consume("expired-ticket"); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("expired Consume error = %v, want ErrTicketInvalid", err)
	}
	h.mu.Lock()
	_, stillThere := h.tickets["expired-ticket"]
	h.mu.Unlock()
	if stillThere {
		t.Fatal("expired ticket was not consumed")
	}
}

func TestOpenHasTerminalIDAndReplacesSession(t *testing.T) {
	h := NewHub(nil)
	src1 := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })

	id1 := h.Open("sbx_1", src1)
	if !strings.HasPrefix(id1, "term_") {
		t.Fatalf("terminal id = %q, want term_ prefix", id1)
	}
	if !h.Has("sbx_1") {
		t.Fatal("Has() false after Open")
	}
	got, ok := h.TerminalID("sbx_1")
	if !ok || got != id1 {
		t.Fatalf("TerminalID() = %q, %v; want %q, true", got, ok, id1)
	}
	s1 := sessionFor(t, h, "sbx_1")
	if s1.ID != id1 || s1.terminalID != id1 {
		t.Fatalf("session ids = %q/%q, want %q", s1.ID, s1.terminalID, id1)
	}
	if h.Has("sbx_other") {
		t.Fatal("Has() true for a sandbox that was never opened")
	}

	// Opening a second PTY for the same sandbox shuts down the old one.
	src2 := newFakeSource(100, 30)
	id2 := h.Open("sbx_1", src2)
	if id2 == id1 {
		t.Fatal("reopened terminal reused the old id")
	}
	waitFor(t, "old source closed", func() bool { return src1.closeCount() == 1 })
	if !isClosed(s1.exited) {
		t.Fatal("old session exited channel not closed")
	}
	if got, _ := h.TerminalID("sbx_1"); got != id2 {
		t.Fatalf("TerminalID after reopen = %q, want %q", got, id2)
	}
	s1.mu.Lock()
	oldClosed := s1.closed
	s1.mu.Unlock()
	if !oldClosed {
		t.Fatal("old session not marked closed")
	}
}

func TestCloseSessionAndUnknown(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")

	h.Close("sbx_1")
	if h.Has("sbx_1") {
		t.Fatal("Has() true after Close")
	}
	if _, ok := h.TerminalID("sbx_1"); ok {
		t.Fatal("TerminalID() ok after Close")
	}
	waitFor(t, "source closed", func() bool { return src.closeCount() == 1 })
	if !isClosed(s.exited) {
		t.Fatal("session exited channel not closed by Close")
	}

	// Closing unknown sandboxes and double-closing is a no-op.
	h.Close("sbx_1")
	h.Close("sbx_missing")
	if src.closeCount() != 1 {
		t.Fatalf("source closed %d times, want 1", src.closeCount())
	}
}

func TestPumpForwardsOutputToAttachedClient(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")

	c := newTestClient(64)
	attachClient(t, s, c)

	src.feed(t, "hello from the pty")
	msg := recvClientMessage(t, c)
	if msg.Type != "output" {
		t.Fatalf("message type = %q, want output", msg.Type)
	}
	if msg.Data != "hello from the pty" {
		t.Fatalf("output = %q", msg.Data)
	}
}

func TestPumpDrainsWhileDetached(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)

	// No client is attached: the pump must keep draining instead of blocking
	// the PTY once the kernel/pipe buffers are full.
	src.feed(t, strings.Repeat("x", 256*1024))

	c := newTestClient(64)
	attachClient(t, sessionFor(t, h, "sbx_1"), c)
	src.feed(t, "after-attach")
	waitFor(t, "post-attach output", func() bool {
		for {
			select {
			case msg := <-c.send:
				if msg.Type == "output" && strings.Contains(msg.Data, "after-attach") {
					return true
				}
			default:
				return false
			}
		}
	})
	if strings.Contains(src.inputText(), "x") {
		t.Fatal("discarded PTY output was echoed back as input")
	}
}

func TestPumpDetachesSlowClient(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")

	slow := newTestClient(1)
	slow.send <- serverMessage{Type: "pending"} // fill the buffer; nothing drains it
	attachClient(t, s, slow)

	// Nothing ever drains this client, so the pump waits out sendTimeout and
	// then detaches it.
	src.feed(t, "first chunk")
	waitFor(t, "slow client close", func() bool { return isClosed(slow.closed) })
	waitFor(t, "slow client detach", func() bool { return attachedClient(s) == nil })

	// Output after the slow client was dropped must still be drained.
	src.feed(t, "second chunk")
	if got := attachedClient(s); got != nil {
		t.Fatalf("session still has client %p", got)
	}
}

func TestPumpWaitsForBusyClient(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")

	busy := newTestClient(1)
	busy.sendTimeout = 5 * time.Second
	busy.send <- serverMessage{Type: "pending"}
	attachClient(t, s, busy)

	src.feed(t, "burst")
	// A client that is merely behind must not lose output: freeing a slot
	// lets the queued chunk through.
	time.Sleep(50 * time.Millisecond)
	if msg := <-busy.send; msg.Type != "pending" {
		t.Fatalf("first queued message = %+v, want pending", msg)
	}
	if msg := recvClientMessage(t, busy); msg.Type != "output" || msg.Data != "burst" {
		t.Fatalf("second queued message = %+v, want the burst output", msg)
	}
	if isClosed(busy.closed) {
		t.Fatal("a client that caught up was closed")
	}
	if got := attachedClient(s); got != busy {
		t.Fatalf("attached client = %p, want the busy client", got)
	}
}

func TestClientSendOutputTimesOut(t *testing.T) {
	c := newTestClient(0)
	start := time.Now()
	if c.sendOutput(serverMessage{Type: "output", Data: "x"}) {
		t.Fatal("sendOutput succeeded with no reader")
	}
	if waited := time.Since(start); waited < c.sendTimeout {
		t.Fatalf("sendOutput gave up after %s, want at least %s", waited, c.sendTimeout)
	}
	c.close()
	if c.sendOutput(serverMessage{Type: "output", Data: "x"}) {
		t.Fatal("sendOutput succeeded after close")
	}
}

func TestWaitSendsExitMessage(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() {
		h.Close("sbx_1")
		_ = src.Close()
	})
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")

	c := newTestClient(8)
	attachClient(t, s, c)
	src.finish(7)

	msg := recvClientMessage(t, c)
	if msg.Type != "exit" {
		t.Fatalf("message type = %q, want exit", msg.Type)
	}
	if msg.Code == nil || *msg.Code != 7 {
		t.Fatalf("exit code = %v, want 7", msg.Code)
	}
	waitFor(t, "client closed after exit", func() bool { return isClosed(c.closed) })
	waitFor(t, "session marked closed", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closed && s.client == nil
	})
	if !isClosed(s.exited) {
		t.Fatal("exited channel not closed after exit")
	}
}

func TestAttachReplacesOldClientAndResizes(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")

	c1 := newTestClient(8)
	attachClient(t, s, c1)
	calls := src.resizeCalls()
	if len(calls) != 1 || calls[0] != (resizeCall{cols: 80, rows: 24}) {
		t.Fatalf("resize on first attach = %+v", calls)
	}

	c2 := newTestClient(8)
	attachClient(t, s, c2)

	msg := recvClientMessage(t, c1)
	if msg.Type != "error" || msg.Error == nil || msg.Error.Code != "terminal_replaced" {
		t.Fatalf("old client message = %+v, want terminal_replaced", msg)
	}
	waitFor(t, "old client closed", func() bool { return isClosed(c1.closed) })
	if got := attachedClient(s); got != c2 {
		t.Fatalf("attached client = %p, want %p", got, c2)
	}

	// Attaching at the same size still issues a resize so the TUI redraws.
	calls = src.resizeCalls()
	if len(calls) != 2 || calls[1] != (resizeCall{cols: 80, rows: 24}) {
		t.Fatalf("resize calls after same-size attach = %+v", calls)
	}
}

func TestAttachDefaultsSizeWhenUnknown(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(0, 0)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)

	attachClient(t, sessionFor(t, h, "sbx_1"), newTestClient(8))
	calls := src.resizeCalls()
	if len(calls) != 1 || calls[0] != (resizeCall{cols: 120, rows: 40}) {
		t.Fatalf("default resize = %+v, want 120x40", calls)
	}
}

func TestAttachToClosedSessionFails(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")
	s.shutdown()

	if err := s.attach(newTestClient(1)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("attach after shutdown error = %v, want ErrNotReady", err)
	}
}

func TestDetachOnlyClearsCurrentClient(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")

	c1 := newTestClient(8)
	c2 := newTestClient(8)
	attachClient(t, s, c1)

	s.detach(c2)
	if got := attachedClient(s); got != c1 {
		t.Fatalf("detach(non-current) cleared client: %p", got)
	}
	s.detach(c1)
	if got := attachedClient(s); got != nil {
		t.Fatalf("detach(current) left client: %p", got)
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	s := sessionFor(t, h, "sbx_1")

	c := newTestClient(8)
	attachClient(t, s, c)

	s.shutdown()
	waitFor(t, "client closed", func() bool { return isClosed(c.closed) })
	if src.closeCount() != 1 {
		t.Fatalf("source closed %d times, want 1", src.closeCount())
	}
	if !isClosed(s.exited) {
		t.Fatal("exited channel not closed")
	}
	s.shutdown()
	if src.closeCount() != 1 {
		t.Fatalf("second shutdown closed source %d times", src.closeCount())
	}
}

// ------------------------------------------------------------ client details

func TestClientTrySendAndClose(t *testing.T) {
	c := newClient(nil)
	if !c.trySend(serverMessage{Type: "one"}) {
		t.Fatal("trySend failed on empty channel")
	}
	if msg := <-c.send; msg.Type != "one" {
		t.Fatalf("queued message = %+v", msg)
	}

	for i := 0; i < cap(c.send); i++ {
		if !c.trySend(serverMessage{Type: "fill"}) {
			t.Fatalf("trySend failed at %d/%d", i, cap(c.send))
		}
	}
	if c.trySend(serverMessage{Type: "overflow"}) {
		t.Fatal("trySend succeeded on a full channel")
	}

	c.close()
	if !isClosed(c.closed) {
		t.Fatal("close did not signal closed channel")
	}
	if c.trySend(serverMessage{Type: "after-close"}) {
		t.Fatal("trySend succeeded after close")
	}
	c.close() // idempotent
}

func TestBearerOrQuery(t *testing.T) {
	cases := []struct {
		name  string
		auth  string
		query string
		want  string
	}{
		{name: "bearer", auth: "Bearer abc123", want: "abc123"},
		{name: "bearer lowercase scheme", auth: "bearer abc123", want: "abc123"},
		{name: "query only", query: "q-ticket", want: "q-ticket"},
		{name: "basic falls back to query", auth: "Basic dXNlcjpwYXNz", query: "q-ticket", want: "q-ticket"},
		{name: "malformed bearer falls back to query", auth: "Bearer", query: "q-ticket", want: "q-ticket"},
		{name: "bearer with extra field falls back to query", auth: "Bearer one two", query: "q-ticket", want: "q-ticket"},
		{name: "other scheme falls back to query", auth: "Token abc123", query: "q-ticket", want: "q-ticket"},
		{name: "header wins over query", auth: "Bearer header-ticket", query: "q-ticket", want: "header-ticket"},
		{name: "empty", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/terminal"
			if tc.query != "" {
				target += "?" + url.Values{"ticket": {tc.query}}.Encode()
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			if got := bearerOrQuery(req); got != tc.want {
				t.Fatalf("bearerOrQuery() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --------------------------------------------------------- ServeWS behavior

type wsTestMessage struct {
	Type       string `json:"type"`
	Data       string `json:"data,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
	Code       *int   `json:"code,omitempty"`
	Error      *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func newTerminalServer(t *testing.T, h *Hub) (srv *httptest.Server, wsURL string) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(h.ServeWS))
	t.Cleanup(srv.Close)
	return srv, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dialTerminal(t *testing.T, rawURL string, header map[string][]string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, rawURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial terminal %s: %v (status %d)", rawURL, err, status)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func writeTerminal(t *testing.T, conn *websocket.Conn, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal ws payload: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("write ws: %v", err)
	}
}

func readTerminal(t *testing.T, conn *websocket.Conn) wsTestMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read ws: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("ws message type = %v, want text", typ)
	}
	var msg wsTestMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decode ws message %q: %v", raw, err)
	}
	return msg
}

func readTerminalUntil(t *testing.T, conn *websocket.Conn, pred func(wsTestMessage) bool) wsTestMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		typ, raw, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read ws waiting for message: %v", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg wsTestMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("decode ws message %q: %v", raw, err)
		}
		if pred(msg) {
			return msg
		}
	}
	t.Fatal("timed out waiting for expected ws message")
	return wsTestMessage{}
}

func waitResize(t *testing.T, src *fakeSource, cols, rows int) {
	t.Helper()
	waitFor(t, "resize call", func() bool {
		for _, call := range src.resizeCalls() {
			if call.cols == cols && call.rows == rows {
				return true
			}
		}
		return false
	})
}

func waitInput(t *testing.T, src *fakeSource, want string) {
	t.Helper()
	waitFor(t, "terminal input "+want, func() bool {
		return strings.Contains(src.inputText(), want)
	})
}

func TestServeWSNoTicket(t *testing.T) {
	h := NewHub(nil)
	srv, wsURL := newTerminalServer(t, h)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("dial without a ticket succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dial without ticket status = %v, want 401", resp)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("plain GET: %v", err)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read error body: %v", err)
	}
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("plain GET status = %d, want 401", r.StatusCode)
	}
	if !strings.Contains(string(body), "ticket_invalid") {
		t.Fatalf("error body = %q, want ticket_invalid", body)
	}
}

func TestServeWSWrongTicket(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	_, wsURL := newTerminalServer(t, h)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL+"?ticket=wrong", nil)
	if err == nil {
		t.Fatal("dial with a wrong ticket succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong ticket status = %v, want 401", resp)
	}
}

func TestServeWSPlainGETWithTicketRejected(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	_, wsURL := newTerminalServer(t, h)

	raw, _, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpURL := "http" + strings.TrimPrefix(wsURL, "ws")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+"?ticket="+url.QueryEscape(raw), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426 for non-websocket request", resp.StatusCode)
	}
}

func TestServeWSTicketWithoutSession(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	h.Open("sbx_1", src)
	_, wsURL := newTerminalServer(t, h)

	raw, _, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	h.Close("sbx_1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL+"?ticket="+url.QueryEscape(raw), nil)
	if err == nil {
		t.Fatal("dial for a removed session succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("removed session status = %v, want 401", resp)
	}
}

func TestServeWSValidTicketQuery(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	_, wsURL := newTerminalServer(t, h)

	raw, _, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	conn := dialTerminal(t, wsURL+"?ticket="+url.QueryEscape(raw), nil)
	ready := readTerminal(t, conn)
	if ready.Type != "ready" {
		t.Fatalf("first message = %+v, want ready", ready)
	}
	wantID, _ := h.TerminalID("sbx_1")
	if ready.TerminalID != wantID {
		t.Fatalf("ready terminal_id = %q, want %q", ready.TerminalID, wantID)
	}
	if !strings.HasPrefix(ready.TerminalID, "term_") {
		t.Fatalf("terminal_id = %q, want term_ prefix", ready.TerminalID)
	}
	// Attaching always resizes, even before the client asks.
	waitResize(t, src, 80, 24)

	writeTerminal(t, conn, map[string]any{"type": "input", "data": "echo hello"})
	waitInput(t, src, "echo hello")

	writeTerminal(t, conn, map[string]any{"type": "resize", "cols": 90, "rows": 30})
	waitResize(t, src, 90, 30)

	writeTerminal(t, conn, map[string]any{"type": "ping"})
	pong := readTerminalUntil(t, conn, func(m wsTestMessage) bool { return m.Type == "pong" })
	if pong.Type != "pong" {
		t.Fatalf("pong message = %+v", pong)
	}

	// PTY output is forwarded to the attached client.
	src.feed(t, "output-from-pty")
	output := readTerminalUntil(t, conn, func(m wsTestMessage) bool {
		return m.Type == "output" && strings.Contains(m.Data, "output-from-pty")
	})
	if !strings.Contains(output.Data, "output-from-pty") {
		t.Fatalf("output message = %q", output.Data)
	}

	// Malformed frames and no-op control messages must not kill the session.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{0x01, 0x02}); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte("not-json")); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}
	writeTerminal(t, conn, map[string]any{"type": "input", "data": ""})
	writeTerminal(t, conn, map[string]any{"type": "resize", "cols": 0, "rows": 0})
	writeTerminal(t, conn, map[string]any{"type": "resize", "cols": -1, "rows": 30})
	writeTerminal(t, conn, map[string]any{"type": "unknown"})
	writeTerminal(t, conn, map[string]any{"type": "ping"})
	readTerminalUntil(t, conn, func(m wsTestMessage) bool { return m.Type == "pong" })

	if got := src.inputText(); strings.Contains(got, "not-json") {
		t.Fatalf("malformed frame was written to PTY input: %q", got)
	}
	if calls := src.resizeCalls(); len(calls) == 0 || calls[len(calls)-1] != (resizeCall{cols: 90, rows: 30}) {
		t.Fatalf("invalid resize reached the PTY: %+v", calls)
	}
}

func TestServeWSInputSurvivesShortWrites(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	_, wsURL := newTerminalServer(t, h)
	raw, _, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	conn := dialTerminal(t, wsURL+"?ticket="+url.QueryEscape(raw), nil)
	if ready := readTerminal(t, conn); ready.Type != "ready" {
		t.Fatalf("first message = %+v, want ready", ready)
	}

	// A tty input queue accepts a few bytes at a time; a bulk paste must still
	// arrive whole.
	src.setWriteLimit(3)
	command := "echo this-is-a-long-command-line"
	writeTerminal(t, conn, map[string]any{"type": "input", "data": command})
	waitInput(t, src, command)
}

func TestServeWSInputFailureIsReported(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	_, wsURL := newTerminalServer(t, h)
	raw, _, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	conn := dialTerminal(t, wsURL+"?ticket="+url.QueryEscape(raw), nil)
	if ready := readTerminal(t, conn); ready.Type != "ready" {
		t.Fatalf("first message = %+v, want ready", ready)
	}

	src.setWriteErr(errors.New("pty is gone"))
	writeTerminal(t, conn, map[string]any{"type": "input", "data": "echo hello"})
	msg := readTerminalUntil(t, conn, func(m wsTestMessage) bool { return m.Type == "error" })
	if msg.Error == nil || msg.Error.Code != "input_failed" {
		t.Fatalf("error message = %+v, want input_failed", msg)
	}
	if got := src.inputText(); got != "" {
		t.Fatalf("input text = %q, want empty", got)
	}

	// A rejected write must not kill the session.
	src.setWriteErr(nil)
	writeTerminal(t, conn, map[string]any{"type": "input", "data": "retry"})
	waitInput(t, src, "retry")
}

func TestServeWSBearerAuth(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	_, wsURL := newTerminalServer(t, h)

	raw, _, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	conn := dialTerminal(t, wsURL, map[string][]string{"Authorization": {"Bearer " + raw}})
	ready := readTerminal(t, conn)
	if ready.Type != "ready" || ready.TerminalID == "" {
		t.Fatalf("first message = %+v, want ready", ready)
	}
}

func TestServeWSReconnectReplacesClient(t *testing.T) {
	h := NewHub(nil)
	src := newFakeSource(80, 24)
	t.Cleanup(func() { h.Close("sbx_1") })
	h.Open("sbx_1", src)
	_, wsURL := newTerminalServer(t, h)
	wantID, _ := h.TerminalID("sbx_1")

	ticket1, _, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("Mint first: %v", err)
	}
	c1 := dialTerminal(t, wsURL, map[string][]string{"Authorization": {"Bearer " + ticket1}})
	if ready := readTerminal(t, c1); ready.Type != "ready" || ready.TerminalID != wantID {
		t.Fatalf("first ready = %+v, want id %q", ready, wantID)
	}
	waitResize(t, src, 80, 24)

	ticket2, _, err := h.Mint("sbx_1")
	if err != nil {
		t.Fatalf("Mint second: %v", err)
	}
	c2 := dialTerminal(t, wsURL+"?ticket="+url.QueryEscape(ticket2), nil)
	if ready := readTerminal(t, c2); ready.Type != "ready" || ready.TerminalID != wantID {
		t.Fatalf("second ready = %+v, want same id %q", ready, wantID)
	}

	// The second attach must issue another SIGWINCH even at the same size.
	waitFor(t, "resize on second attach", func() bool { return len(src.resizeCalls()) >= 2 })
	for i, call := range src.resizeCalls() {
		if call != (resizeCall{cols: 80, rows: 24}) {
			t.Fatalf("resize call %d = %+v, want same size 80x24", i, call)
		}
	}

	replaced := readTerminalUntil(t, c1, func(m wsTestMessage) bool { return m.Type == "error" })
	if replaced.Error == nil || replaced.Error.Code != "terminal_replaced" {
		t.Fatalf("old client message = %+v, want terminal_replaced", replaced)
	}

	// The replacement client is the writable one.
	writeTerminal(t, c2, map[string]any{"type": "input", "data": "after-reconnect"})
	waitInput(t, src, "after-reconnect")

	// The old connection is closed after the replacement notification.
	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := c1.Read(readCtx)
		if err != nil {
			break
		}
	}

	c2.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "replacement detach", func() bool {
		s := sessionFor(t, h, "sbx_1")
		return attachedClient(s) == nil
	})
}
