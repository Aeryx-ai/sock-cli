package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"

	"github.com/guygrigsby/sock-cli/internal/client"
)

const (
	reconnectBase = time.Second
	reconnectMax  = time.Minute
	// heartbeatInterval is how often the server sends a heartbeat on an
	// idle stream (sock.v1 Subscribe contract).
	heartbeatInterval = 30 * time.Second
	// defaultListenIdle tolerates one missed heartbeat.
	defaultListenIdle = 2 * heartbeatInterval
)

// errStreamIdle marks a stream that went silent past the idle limit,
// typically a half-open connection. It is not a Connect error, so listen
// reconnects after it.
var errStreamIdle = errors.New("no message or heartbeat")

func newListenCmd(app *App) *cobra.Command {
	var execCmd string
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Stream messages as they arrive and acknowledge each one",
		Long: "Stream pending and new messages. Each is printed (or passed to --exec on stdin as JSON) and then acknowledged. " +
			"With --exec, a message is acknowledged only if the command exits 0; delivery is at-least-once, so the command may see a message twice.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := app.clients()
			if err != nil {
				return err
			}
			idle := app.ListenIdle
			if idle <= 0 {
				idle = defaultListenIdle
			}
			l := &listener{app: app, clients: c, printer: app.printer(), exec: execCmd, idle: idle, out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr()}
			return l.Run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&execCmd, "exec", "", "shell command run per message, message JSON on stdin; ack on exit 0")
	return cmd
}

type listener struct {
	app     *App
	clients client.Clients
	printer Printer
	exec    string
	idle    time.Duration
	out     io.Writer
	errOut  io.Writer
}

// logf writes a diagnostic line. A failing stderr has no recovery path.
func (l *listener) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(l.errOut, "sock: "+format+"\n", args...)
}

// Run subscribes until ctx ends or the server refuses the caller outright.
func (l *listener) Run(ctx context.Context) error {
	backoff := reconnectBase
	for {
		received, err := l.once(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && !reconnectable(err) {
			return client.Describe(err)
		}
		if received {
			// The connection worked; the next failure is a fresh one.
			backoff = reconnectBase
		}
		reason := "stream closed"
		if err != nil {
			reason = client.Describe(err).Error()
		}
		l.logf("%s; reconnecting in %s", reason, backoff)
		if l.app.Sleep(ctx, backoff) != nil {
			return nil
		}
		backoff = min(backoff*2, reconnectMax)
	}
}

// once holds one Subscribe stream. It reports whether anything (a message
// or a heartbeat) arrived.
// A watchdog cancels the stream after l.idle with nothing received, so a
// half-open connection cannot hang listen forever. Handlers run on ctx, not
// the stream's context, so a slow --exec is never cut off by the watchdog.
func (l *listener) once(ctx context.Context) (bool, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var idled atomic.Bool
	watchdog := time.AfterFunc(l.idle, func() {
		idled.Store(true)
		cancel()
	})
	defer watchdog.Stop()
	idleErr := func() error { return fmt.Errorf("%w in %s", errStreamIdle, l.idle) }

	stream, err := l.clients.Messaging.Subscribe(sctx, connect.NewRequest(&sockv1.SubscribeRequest{}))
	if err != nil {
		if idled.Load() {
			return false, idleErr()
		}
		return false, err
	}
	defer func() {
		if err := stream.Close(); err != nil && sctx.Err() == nil {
			l.logf("close stream: %v", err)
		}
	}()
	received := false
	for stream.Receive() {
		watchdog.Stop()
		received = true
		if m := stream.Msg().GetMessage(); m != nil {
			if err := l.handle(ctx, m); err != nil {
				// One bad message never stops the stream; it stays pending and
				// comes back on the next connection.
				l.logf("message %s: %v", m.GetId(), err)
			}
		}
		watchdog.Reset(l.idle)
	}
	if idled.Load() {
		return received, idleErr()
	}
	return received, stream.Err()
}

func (l *listener) handle(ctx context.Context, m *sockv1.Message) error {
	if l.exec == "" {
		if err := l.printer.Message(l.out, m); err != nil {
			return fmt.Errorf("print: %w (not acked)", err)
		}
	} else {
		payload, err := protojson.Marshal(m)
		if err != nil {
			return fmt.Errorf("encode: %w (not acked)", err)
		}
		// The message body goes only in stdin. Env carries ids, never text.
		env := []string{
			"SOCK_MESSAGE_ID=" + m.GetId(),
			"SOCK_CONVERSATION_ID=" + m.GetConversationId(),
			"SOCK_SENDER_ID=" + m.GetSenderId(),
		}
		if err := l.app.RunExec(ctx, l.exec, payload, env, l.out, l.errOut); err != nil {
			return fmt.Errorf("--exec failed: %w (not acked)", err)
		}
	}
	return ack(ctx, l.app, l.clients, []string{m.GetId()})
}

// reconnectable reports whether a stream failure is worth reconnecting
// after. Refusals about who the caller is or what it sent are final.
func reconnectable(err error) bool {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return true // network-level failure
	}
	switch ce.Code() {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeInvalidArgument:
		return false
	}
	return true
}
