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

	sockv1 "github.com/aeryx-ai/sock-contract/gen/go/sock/v1"

	client "github.com/guygrigsby/sock-client"
)

const (
	reconnectBase = time.Second
	// reconnectMax tops out the backoff between stream attempts. It is short
	// on purpose: a hub deploy or restart must not leave the listener dark
	// for a minute, and one reconnect attempt every few seconds is cheap.
	reconnectMax = 5 * time.Second
	// maxRetryAfter bounds a server-sent Retry-After, which is honored in
	// full otherwise: reconnecting sooner is what the header says not to do.
	maxRetryAfter = time.Hour
	// execRetryMax tops out the backoff between --exec retries of one message.
	execRetryMax = time.Minute
	// heartbeatInterval is how often the server sends a heartbeat on an
	// idle stream (sock.v1 Subscribe contract).
	heartbeatInterval = 30 * time.Second
	// defaultListenIdle tolerates one missed heartbeat.
	defaultListenIdle = 2 * heartbeatInterval
	// execAttempts is how many times a message's --exec command runs on the
	// current stream before the message is left pending for a reconnect.
	execAttempts = 5
)

// errStreamIdle marks a stream that went silent past the idle limit,
// typically a half-open connection. It is not a Connect error, so listen
// reconnects after it.
var errStreamIdle = errors.New("no message or heartbeat")

func newListenCmd(app *App) *cobra.Command {
	var (
		execCmd, project string
		asPuppet         bool
	)
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Stream messages as they arrive and acknowledge each one",
		Long: "Stream pending and new messages. Each is printed (or passed to --exec on stdin as JSON) and then acknowledged. " +
			"With --exec, a message is acknowledged only if the command exits 0; a failing command is retried up to 5 times with backoff, then the message stays pending for the next connection. Delivery is at-least-once, so the command may see a message twice.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project != "" && !asPuppet {
				return errors.New("--project needs --as-puppet")
			}
			var (
				c   Clients
				err error
			)
			if asPuppet {
				// checked at start, not trusted: a service manager restarts
				// listen, and a cached token of an erased puppet must be replaced
				c, _, err = app.puppetClients(cmd.Context(), project, false, cmd.ErrOrStderr())
			} else {
				c, err = app.clients()
			}
			if err != nil {
				return err
			}
			idle := app.ListenIdle
			if idle <= 0 {
				idle = defaultListenIdle
			}
			l := &listener{app: app, clients: c, printer: app.printer(), exec: execCmd, idle: idle, out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr()}
			// A puppet listener hands its own credential to the child: the
			// runner's owner gate and a Codex agent's sock-mcp need it. A human
			// session token never goes: a prompt-injected child could print it.
			if client.TokenKind(app.token) == client.KindPuppet {
				l.childEnv = []string{"SOCK_TOKEN=" + app.token, "SOCK_URL=" + app.url}
			}
			return l.Run(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&asPuppet, "as-puppet", false, "listen as the puppet sock-mcp would: SOCK_TOKEN, else the config file's machine_key (the project's puppet), else its puppet_token; never your login. The --exec child gets that puppet's SOCK_TOKEN and SOCK_URL")
	cmd.Flags().StringVar(&project, "project", "", "with --as-puppet, the directory sock-mcp starts in (default: the working directory; CLAUDE_PROJECT_DIR wins when set)")
	cmd.Flags().StringVar(&execCmd, "exec", "", "shell command run per message, message JSON on stdin; ack on exit 0")
	return cmd
}

type listener struct {
	app     *App
	clients Clients
	printer Printer
	exec    string
	idle    time.Duration
	out     io.Writer
	errOut  io.Writer
	// childEnv is the listener's own puppet credential, for the --exec child.
	childEnv []string
}

// logf writes a diagnostic line. A failing stderr has no recovery path.
func (l *listener) logf(format string, args ...any) {
	warnf(l.errOut, format, args...)
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
			return l.app.describe(client.Describe(err))
		}
		if received {
			// The connection worked; the next failure is a fresh one.
			backoff = reconnectBase
		}
		reason := "stream closed"
		wait := backoff
		if l.app.Jitter != nil {
			wait = l.app.Jitter(backoff)
		}
		if err != nil {
			reason = l.app.describe(client.Describe(err)).Error()
			if ra, ok := client.RetryAfter(err, time.Now()); ok {
				wait = min(max(wait, ra), maxRetryAfter)
			}
		}
		l.logf("%s; reconnecting in %s", reason, wait)
		if l.app.Sleep(ctx, wait) != nil {
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
			// One bad message never stops the stream; it stays pending and
			// comes back on the next connection. After Ctrl-C the failure is
			// just the cancel, so say nothing.
			if err := l.handle(ctx, m); err != nil && ctx.Err() == nil {
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
		env = append(env, l.childEnv...)
		if err := l.runExec(ctx, m.GetId(), payload, env); err != nil {
			return fmt.Errorf("--exec failed: %w (not acked)", err)
		}
	}
	return ack(ctx, l.app, l.clients, []string{m.GetId()})
}

// runExec runs the --exec command, retrying a failure up to execAttempts
// times with the reconnect backoff. Messages are handled in order, so a
// message that still fails holds the stream for the retries and is then left
// pending; it comes back on the next connection.
func (l *listener) runExec(ctx context.Context, id string, payload []byte, env []string) error {
	backoff := reconnectBase
	for attempt := 1; ; attempt++ {
		err := l.app.RunExec(ctx, l.exec, payload, env, l.out, l.errOut)
		if err == nil || attempt == execAttempts || ctx.Err() != nil {
			return err
		}
		wait := backoff
		if l.app.Jitter != nil {
			wait = l.app.Jitter(backoff)
		}
		l.logf("message %s: --exec failed: %v; retrying in %s", id, err, wait)
		if l.app.Sleep(ctx, wait) != nil {
			return err
		}
		backoff = min(backoff*2, execRetryMax)
	}
}

// reconnectable reports whether a stream failure is worth reconnecting
// after. Refusals about who the caller is or what it sent are final, and so
// is Unimplemented: a wrong URL never heals.
func reconnectable(err error) bool {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return true // network-level failure
	}
	switch ce.Code() {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeInvalidArgument, connect.CodeUnimplemented:
		return false
	}
	return true
}
