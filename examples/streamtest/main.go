// Command streamtest subscribes to an input controller on a live machine and
// prints every event it receives.
//
// It answers one question that machine logs cannot: can a plain client
// register a callback on this controller and actually receive events? It uses
// the same RDK input client a consuming module uses, so if this works and a
// module does not, the difference is the module hosting, not the controller.
//
//	go run ./examples/streamtest \
//	  --host my-machine-main.abc123.viam.cloud \
//	  --key-id $VIAM_API_KEY_ID --key $VIAM_API_KEY
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"go.viam.com/rdk/components/input"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/robot/client"
	"go.viam.com/utils/rpc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	host := flag.String("host", "", "machine address, e.g. roarm-main.abc123.viam.cloud")
	keyID := flag.String("key-id", "", "API key ID")
	key := flag.String("key", "", "API key")
	name := flag.String("name", "keyboard-input", "input controller resource name")
	flag.Parse()
	if *host == "" || *keyID == "" || *key == "" {
		flag.Usage()
		return errors.New("--host, --key-id and --key are required")
	}

	logger := logging.NewLogger("streamtest")
	ctx := context.Background()

	machine, err := client.New(ctx, *host, logger,
		client.WithDialOptions(rpc.WithEntityCredentials(*keyID, rpc.Credentials{
			Type:    rpc.CredentialsTypeAPIKey,
			Payload: *key,
		})),
	)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", *host, err)
	}
	defer func() {
		if err := machine.Close(ctx); err != nil {
			logger.Errorw("closing machine", "err", err)
		}
	}()
	logger.Infof("connected; machine resources: %v", machine.ResourceNames())

	ctrl, err := input.FromProvider(machine, *name)
	if err != nil {
		return fmt.Errorf("resolve input controller %q: %w", *name, err)
	}
	logger.Infof("resolved %q as %T", *name, ctrl)

	// Unary calls first. If these work but no events arrive below, the
	// controller is reachable and only the streaming subscription is broken.
	controls, err := ctrl.Controls(ctx, nil)
	if err != nil {
		return fmt.Errorf("Controls: %w", err)
	}
	logger.Infof("Controls() -> %v", controls)

	snapshot, err := ctrl.Events(ctx, nil)
	if err != nil {
		return fmt.Errorf("Events: %w", err)
	}
	logger.Infof("Events() -> %d entries", len(snapshot))
	for _, c := range controls {
		if e, ok := snapshot[c]; ok {
			logger.Infof("  %-16s %-18s %v", c, e.Event, e.Value)
		}
	}

	var count atomic.Int64
	onEvent := func(_ context.Context, e input.Event) {
		count.Add(1)
		fmt.Printf("%s  %-16s %-18s %v\n",
			e.Time.Format("15:04:05.000"), e.Control, e.Event, e.Value)
	}

	// The same triggers a teleop consumer registers.
	triggers := []input.EventType{
		input.PositionChangeAbs,
		input.ButtonPress,
		input.ButtonRelease,
		input.Connect,
		input.Disconnect,
	}
	for _, c := range controls {
		if err := ctrl.RegisterControlCallback(ctx, c, triggers, onEvent, map[string]interface{}{}); err != nil {
			return fmt.Errorf("register callback for %s: %w", c, err)
		}
		logger.Infof("registered %s", c)
	}

	logger.Info("subscribed. press keys in the web page; ctrl-c to stop.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-sig:
			logger.Infof("stopping after %d events", count.Load())
			return nil
		case <-ticker.C:
			if n := count.Load(); n == 0 {
				logger.Warn("still zero events received")
			} else {
				logger.Infof("%d events received so far", n)
			}
		}
	}
}
