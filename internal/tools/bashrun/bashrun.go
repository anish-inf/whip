// Package bashrun executes bash commands for the agent, with optional PTY
// support for interactive programs (sudo, ssh, gpg) that prompt on the
// controlling terminal.
//
// The default (non-interactive) path runs the command in a new session with no
// controlling terminal, so a program that wants to read a password from /dev/tty
// fails fast ("a terminal is required") instead of hanging indefinitely on
// loopy's terminal — which is what used to lock up the whole agent.
//
// The interactive path runs the command in a PTY. Keystrokes the user types are
// forwarded to the PTY and PTY output streams back to the caller. If the child
// goes quiet for a while (likely waiting for input), the caller is told to show
// a countdown; if input is still absent after the inactivity timeout, the
// command is killed so loopy never hangs forever.
package bashrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Result is the outcome of one command run.
type Result struct {
	// Output is the combined stdout+stderr captured for the model.
	Output string
	// Exit is the human-readable exit status fed back to the model. It is
	// empty for a clean exit 0.
	Exit string
	// TimedOut reports the command exceeded its wall-clock timeout.
	TimedOut bool
	// Killed reports the command was killed by us (timeout, inactivity
	// timeout, or cancellation) rather than exiting on its own.
	Killed bool
	// Interactive reports whether the interactive PTY path was used.
	Interactive bool
}

// Options configure a single run.
type Options struct {
	Command string
	// Timeout is the hard wall-clock cap. <=0 means 120s.
	Timeout time.Duration
	// Interactive runs the command in a PTY so sudo/ssh-like password prompts
	// work. Requires Keys, OnOutput, OnAwaitInput to be wired by the caller.
	Interactive bool
	// InactivityTimeout is the interactive-mode cap: if the child produces no
	// output and receives no forwarded keystroke for this long, the command is
	// killed as "timed out waiting for input". <=0 means 15s.
	InactivityTimeout time.Duration
	// OnOutput streams PTY stdout/stderr deltas back to the caller (live
	// transcript). Interactive only; safe to call from the run goroutine.
	OnOutput func(chunk string)
	// OnAwaitInput is called once per second while the child is quiet and
	// likely waiting for input; secLeft is the seconds remaining before the
	// inactivity timeout fires. Interactive only.
	OnAwaitInput func(secLeft int)
	// Keys is the channel the caller pushes keystrokes into for forwarding to
	// the PTY. The runner drains it until the command ends, then closes it.
	// Interactive only; may be nil for a fire-and-forget interactive run.
	Keys <-chan []byte
}

// Run executes the command and returns its result.
//
// In non-interactive mode Run blocks until the command finishes or its timeout
// fires. In interactive mode Run blocks until the command finishes, the hard
// timeout fires, or the inactivity timeout fires.
func Run(ctx context.Context, opts Options) Result {
	if opts.Timeout <= 0 {
		opts.Timeout = 120 * time.Second
	}
	if opts.Interactive && opts.InactivityTimeout <= 0 {
		opts.InactivityTimeout = 15 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", opts.Command)
	cmd.Env = os.Environ()

	if opts.Interactive {
		return runInteractive(ctx, cmd, opts)
	}
	return runPiped(ctx, cmd)
}

// runPiped runs the command with stdout/stderr captured, stdin wired to
// /dev/null, and a fresh session with no controlling terminal. A program that
// tries to open /dev/tty for a password fails fast rather than hanging on
// loopy's terminal.
func runPiped(ctx context.Context, cmd *exec.Cmd) Result {
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if devNull := openDevNull(); devNull != nil {
		cmd.Stdin = devNull
		defer devNull.Close()
	}
	// Setsid gives the child a new session with no controlling terminal, so a
	// program that insists on /dev/tty fails immediately instead of grabbing
	// loopy's terminal and blocking its input loop.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	err := cmd.Run()
	res := Result{Output: out.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.Killed = true
		res.Exit = "timed out"
		return res
	}
	if isCancelled(ctx, err) {
		res.Killed = true
		res.Exit = "cancelled"
		return res
	}
	res.Exit = exitString(err)
	if err != nil {
		res.Killed = isKilledBySignal(err)
	}
	return res
}

// runInteractive runs the command in a PTY. sudo, ssh, gpg and friends detect a
// real terminal and prompt normally; the password is never echoed into the
// transcript because the PTY slave's ECHO is off for the master and the runner
// forwards raw bytes, not display text.
func runInteractive(ctx context.Context, cmd *exec.Cmd, opts Options) Result {
	// Setsid + Setctty make pty.Start give the child a controlling terminal
	// that is the pty slave — exactly what sudo wants.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		// Fall back to the safe non-interactive path; an interactive failure
		// must never hang the agent.
		return runPiped(ctx, cmd)
	}
	defer ptmx.Close()

	// Kill the whole process group (bash + any children) on timeout/cancel so
	// nothing outlives the run.
	stop := sync.OnceFunc(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	go func() {
		<-ctx.Done()
		stop()
	}()

	var buf bytes.Buffer
	outCh := make(chan []byte, 16)

	// Output pump: copy PTY -> caller + buffer; on read error the child has
	// exited (or the PTY closed), so we signal end-of-stream with a nil chunk.
	// Every send guards on ctx.Done so the pump can never block forever after
	// the main loop has returned (deferred ptmx.Close fires ctx cancel via
	// Run's deferred cancel, unblocking any in-flight send too).
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(tmp)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, tmp[:n])
				select {
				case outCh <- cp:
				case <-ctx.Done():
					return
				}
			}
			if rerr != nil {
				select {
				case outCh <- nil:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	// Quiet clock: any output or forwarded keystroke resets it. When the clock
	// exceeds InactivityTimeout we kill the command.
	quiet := time.Now()
	var quietMu sync.Mutex
	touch := func() {
		quietMu.Lock()
		quiet = time.Now()
		quietMu.Unlock()
	}

	// Key forwarder: write bytes to the PTY master; any keystroke counts as
	// activity and disarms the inactivity timer.
	keyStop := make(chan struct{})
	defer close(keyStop)
	if opts.Keys != nil {
		go func() {
			for {
				select {
				case b, ok := <-opts.Keys:
					if !ok {
						return
					}
					if len(b) > 0 {
						_, _ = ptmx.Write(b)
					}
					touch()
				case <-keyStop:
					return
				}
			}
		}()
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case chunk, ok := <-outCh:
			// ok==false OR nil chunk => the output pump ended (PTY closed,
			// command exited). Wait for the child and return.
			if !ok || chunk == nil {
				waitErr := cmd.Wait()
				res := Result{Output: buf.String(), Interactive: true}
				if ctx.Err() == context.DeadlineExceeded {
					res.TimedOut = true
					res.Killed = true
					res.Exit = "timed out"
					return res
				}
				if isCancelled(ctx, waitErr) {
					res.Killed = true
					res.Exit = "cancelled"
					return res
				}
				res.Exit = exitString(waitErr)
				return res
			}
			buf.Write(chunk)
			if opts.OnOutput != nil {
				opts.OnOutput(string(chunk))
			}
			touch()
		case <-ticker.C:
			quietMu.Lock()
			idle := time.Since(quiet)
			quietMu.Unlock()
			if idle >= opts.InactivityTimeout {
				stop()
				_ = cmd.Wait()
				res := Result{
					Output:      buf.String(),
					Exit:        "timed out waiting for input",
					Killed:      true,
					Interactive: true,
				}
				res.Output += fmt.Sprintf(
					"\n[loopy: interactive command killed after %s with no input]",
					opts.InactivityTimeout.Round(time.Second),
				)
				return res
			}
			if opts.OnAwaitInput != nil {
				secs := int((opts.InactivityTimeout - idle + time.Second - 1) / time.Second)
				if secs < 0 {
					secs = 0
				}
				opts.OnAwaitInput(secs)
			}
		}
	}
}

// exitString renders the exit status the way the existing bash tool did: empty
// for a clean exit 0, "(exit: N)" or "(exit: signal X)" otherwise.
func exitString(err error) string {
	if err == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("(exit: %s)", exitErr)
	}
	return fmt.Sprintf("(exit: %v)", err)
}

// isKilledBySignal reports whether the error was a kill-by-signal.
func isKilledBySignal(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		return ws.Signaled()
	}
	return false
}

// isCancelled reports whether the context was cancelled (user interrupt) and
// the error reflects that.
func isCancelled(ctx context.Context, _ error) bool {
	return errors.Is(ctx.Err(), context.Canceled)
}

// openDevNull returns /dev/null for a child's stdin, or nil on failure.
func openDevNull() *os.File {
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	return f
}

// KeyBytes converts a small set of named special keys to their terminal byte
// sequences. Plain text (KeyRunes) should be forwarded as the raw UTF-8 bytes
// of the runes, not via this helper.
const (
	KeyEnter = "\r"
	KeyEsc   = "\x1b"
	KeyTab   = "\t"
	KeyBS    = "\x7f"
	KeyUp    = "\x1b[A"
	KeyDown  = "\x1b[B"
	KeyRight = "\x1b[C"
	KeyLeft  = "\x1b[D"
)

func KeyBytes(name string) string {
	switch name {
	case "enter":
		return KeyEnter
	case "esc":
		return KeyEsc
	case "tab":
		return KeyTab
	case "backspace", "delete":
		return KeyBS
	case "up":
		return KeyUp
	case "down":
		return KeyDown
	case "right":
		return KeyRight
	case "left":
		return KeyLeft
	}
	return ""
}
