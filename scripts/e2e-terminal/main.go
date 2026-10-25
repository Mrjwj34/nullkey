// Command e2e-terminal attaches to a sandbox PTY, optionally sends input after
// the ready frame, and prints server messages for manual end-to-end checks.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/coder/websocket"
)

func main() {
	wsURL := flag.String("url", "", "terminal ws_url from POST /sandboxes/{id}/terminal")
	ticket := flag.String("ticket", "", "one-time terminal ticket")
	send := flag.String("send", "", "input to send after ready")
	wait := flag.Duration("wait", 5*time.Second, "how long to read output")
	flag.Parse()
	if *wsURL == "" || *ticket == "" {
		fmt.Fprintln(os.Stderr, "usage: e2e-terminal -url ws://... -ticket ... [-send text]")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *wait+10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, *wsURL+"?ticket="+url.QueryEscape(*ticket), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	write := func(payload any) error {
		raw, _ := json.Marshal(payload)
		return conn.Write(ctx, websocket.MessageText, raw)
	}
	read := func() (map[string]any, []byte, error) {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return nil, nil, err
		}
		var msg map[string]any
		_ = json.Unmarshal(raw, &msg)
		return msg, raw, nil
	}

	ready, raw, err := read()
	if err != nil {
		fmt.Fprintln(os.Stderr, "read ready:", err)
		os.Exit(1)
	}
	fmt.Println(string(raw))
	if ready["type"] != "ready" {
		os.Exit(1)
	}
	_ = write(map[string]any{"type": "resize", "cols": 120, "rows": 40})
	if *send != "" {
		time.Sleep(1500 * time.Millisecond)
		if err := write(map[string]any{"type": "input", "data": *send}); err != nil {
			fmt.Fprintln(os.Stderr, "send:", err)
			os.Exit(1)
		}
	}

	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		msg, raw, err := read()
		if err != nil {
			break
		}
		fmt.Println(string(raw))
		if msg["type"] == "exit" {
			break
		}
	}
}
