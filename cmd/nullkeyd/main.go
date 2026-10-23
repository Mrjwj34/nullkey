// Command nullkeyd is the Nullkey host daemon.
//
// It exposes a loopback-only control-plane API consumed by the GUI and, as
// milestones land, the data-plane gateways used by sandboxes. Real credentials
// live in the host Keychain and are never passed to sandbox processes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Mrjwj34/nullkey/internal/kernel"
	"github.com/Mrjwj34/nullkey/internal/runtime/native"
	"github.com/Mrjwj34/nullkey/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "nullkeyd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "__sandbox_exec" {
		os.Exit(native.RunSandboxHelper(os.Args[2:]))
	}
	cfg, err := kernel.DefaultConfig()
	if err != nil {
		return err
	}

	var showVersion bool
	flag.StringVar(&cfg.Listen, "listen", cfg.Listen, "control-plane listen address (loopback only)")
	flag.StringVar(&cfg.GatewayListen, "gateway-listen", cfg.GatewayListen, "sandbox gateway listen address (loopback only)")
	flag.StringVar(&cfg.ProxyListen, "proxy-listen", cfg.ProxyListen, "egress proxy listen address (loopback only)")
	flag.StringVar(&cfg.Home, "home", cfg.Home, "Nullkey home directory (workspace/state/cache/token)")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn, error")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(version.String())
		return nil
	}

	logger, err := kernel.NewLogger(cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("logger: %w", err)
	}

	app, err := kernel.New(cfg, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx)
}
