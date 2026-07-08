package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pipescloud/ppz/internal/cliproto"
)

// cmdRead: ppz read <handle>.<pipe> [--tail --json --tty --raw]
//
// `read` is the cursor-driven verb: it delivers only what's *new* since
// the caller's session cursor and advances the cursor as it goes. Like
// `git log` showing only new commits, it's the agent inbox poll. The
// retrospective / forensic verb is `ppz reread`, which carries the
// historical filter flags (-l / --skip / --since) and never touches
// the cursor.
//
// Output modes (mutually exclusive; default = line-delimited payloads):
//
//	(default)   Fprintln(payload) per message — line-delimited messages,
//	            best-fit for line-oriented broadcast / stdin pipes.
//	--raw       byte-faithful: write payload bytes verbatim, no separator
//	            added. Concatenates the full message stream byte-for-byte
//	            (best for forensics, hex dumps, replay).
//	--tty       collect all payloads, run the concatenated bytes through
//	            a virtual VT100 terminal, print the rendered screen
//	            state. Best-fit for `<h>.stdout` from a wrapped pty
//	            session. Mutually exclusive with --tail.
//	--json      emit each ReadEvent's full envelope as a JSON line.
//
// `--tail` keeps streaming new messages until SIGINT, advancing the
// cursor as live messages arrive so the unread count stays truthful.
func cmdRead(args []string) error {
	if wantsHelp(args) {
		printHelp(os.Stdout, "read")
		return nil
	}
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON envelopes instead of payload text")
	follow := fs.Bool("tail", false, "drain unread messages then keep streaming live until SIGINT")
	tty := fs.Bool("tty", false, "render concatenated payloads through a virtual terminal (vt10x); best for <handle>.stdout from a wrapped pty")
	raw := fs.Bool("raw", false, "write payload bytes verbatim with no message separator; concatenates the full byte stream")
	bare := fs.Bool("bare", false, "force legacy payload-only output (script-stable opt-out from the v0.23 tabular default on inbox-shaped pipes)")
	// -l/--limit forward-pages the unread window: deliver the first N
	// (oldest) unread and advance the cursor only past them, so a flooded
	// inbox is read a page at a time. Default -1 = flag absent → 0 =
	// unlimited (backward compatible: a bare `ppz read` still drains).
	limitLong := fs.Int("limit", -1, "forward-page: read only the first N (oldest) unread, then stop; 0 = unlimited (default)")
	limitShort := fs.Int("l", -1, "shorthand for --limit")
	target, flagArgs, err := splitReadArgs(args, false)
	if err != nil || target == "" {
		usageExit("read")
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	head := readHeadFromLimit(*limitLong, *limitShort, 0 /* default: unlimited */)
	return runRead(target, *asJSON, *follow, *tty, *raw, *bare, false /* all */, 0, 0, 0, head)
}

// readHeadFromLimit reconciles the --limit / -l pair (default -1 = absent)
// into a Head page size. When the flag is absent, the caller's default
// applies (0 for `read` = unlimited; 20 for `subs read`). An explicit
// --limit 0 always means "drain all" (Head 0), overriding a non-zero
// default.
func readHeadFromLimit(long, short, def int) int {
	v := long
	if short != -1 {
		v = short
	}
	if v < 0 { // flag absent
		return def
	}
	return v // includes explicit 0 = drain-all
}

// runRead is the shared engine for `ppz read` and `ppz reread`. The two
// verbs differ only in flag surface and the `all` toggle (which the
// daemon uses to skip cursor consultation + advance).
func runRead(target string, asJSON, follow, tty, raw, bare, all bool, limit, skip int, sinceMS int64, head int) error {
	if tty && follow {
		fmt.Fprintln(os.Stderr, "ppz read: --tty and --tail are mutually exclusive (use 'ppz terminal watch' for live render)")
		os.Exit(2)
	}
	if tty && asJSON {
		fmt.Fprintln(os.Stderr, "ppz read: --tty and --json are mutually exclusive")
		os.Exit(2)
	}
	if raw && asJSON {
		fmt.Fprintln(os.Stderr, "ppz read: --raw and --json are mutually exclusive")
		os.Exit(2)
	}
	if tty && raw {
		fmt.Fprintln(os.Stderr, "ppz read: --tty and --raw are mutually exclusive")
		os.Exit(2)
	}
	if bare && (asJSON || tty || raw) {
		fmt.Fprintln(os.Stderr, "ppz read: --bare is mutually exclusive with --json/--tty/--raw")
		os.Exit(2)
	}
	if target == "inbox" {
		resolved, err := currentInboxTarget()
		if err != nil {
			return err
		}
		target = resolved
	}
	// Phase 1.5: bare names ("room", "team1.room"... wait, the second has
	// a dot) — bare = NO dot. Sent as BareTarget; daemon resolves as
	// uncollared pipe at current_namespace. Dotted form keeps today's
	// handle.pipe semantics.
	var handle, channel, bareTarget string
	if !strings.Contains(target, ".") {
		bareTarget = target
	} else {
		idx := strings.LastIndex(target, ".")
		if idx <= 0 || idx == len(target)-1 {
			return cliproto.New(cliproto.EInvalidPipe)
		}
		handle, channel = target[:idx], target[idx+1:]
	}

	// Phase 1.5.2 smart-default render mode. Only apply when the user
	// didn't pick a mode explicitly and isn't using --tail (follow mode
	// needs byte-faithful streaming).
	//   - <handle>.stdout → --tty (vt10x render so ANSI escapes resolve
	//     instead of leaking into the user's terminal as junk). Mirrors
	//     `ppz terminal read` which has long auto-injected --tty.
	if !asJSON && !tty && !raw && !bare && !follow && channel == "stdout" {
		tty = true
	}

	req := cliproto.ReadRequest{
		Handle:     handle,
		Channel:    channel,
		BareTarget: bareTarget,
		Limit:      limit,
		Head:       head,
		Skip:       skip,
		SinceMS:    sinceMS,
		JSON:       asJSON,
		Follow:     follow,
		Session:    sessionID(),
		All:        all,
		// Forward PPZ_CURRENT_HANDLE as the reader's identity hint so
		// the daemon's ack:read auto-emitter can stamp envelope.sender
		// when its own per-session State.Current is empty (the shared-
		// pty case: terminalShareEnv exports the env var but IPCCreate
		// skips SetCurrent for PTY-kind sources). Same mechanism as
		// SendRequest.Sender (PR #92); senderForRequest centralises
		// the precedence. Empty hint preserves today's behaviour.
		Sender: os.Getenv("PPZ_CURRENT_HANDLE"),
	}

	conn, err := net.Dial("unix", ipcSocket())
	if err != nil {
		return cliproto.New(cliproto.EDaemonNotRunning)
	}
	defer conn.Close()

	body, _ := json.Marshal(req)
	if err := json.NewEncoder(conn).Encode(map[string]any{"method": cliproto.IPCRead, "params": json.RawMessage(body)}); err != nil {
		return err
	}

	// SIGINT during follow → close the socket so the daemon stops sending,
	// then exit 0.
	if follow {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		go func() {
			<-ctx.Done()
			_ = conn.Close()
		}()
	}

	// In --tty mode we collect all payloads, then render once at the end
	// (vt10x.Write is UTF-8-boundary-sensitive). --tty is incompatible
	// with --follow, so we know the stream will end.
	var collected []byte
	// Render dimensions: default to the hardcoded grid, but prefer the
	// source pty's actual size when the daemon emits a Meta event (it
	// does for `<h>.stdout` reads when stdctrl has a resize on file).
	// Without this, bytes laid out at 212 cols garble when vt10x wraps
	// at our default 200.
	renderCols := cliproto.DefaultRenderCols
	renderRows := cliproto.DefaultRenderRows
	var trunc *cliproto.ReadMeta // set when forward paging held messages back

	dec := bufio.NewScanner(conn)
	dec.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for dec.Scan() {
		var evt cliproto.ReadEvent
		if err := json.Unmarshal(dec.Bytes(), &evt); err != nil {
			continue
		}
		if evt.Error != nil {
			return evt.Error
		}
		if evt.Meta != nil {
			if evt.Meta.Cols > 0 {
				renderCols = evt.Meta.Cols
			}
			if evt.Meta.Rows > 0 {
				renderRows = evt.Meta.Rows
			}
			if evt.Meta.Truncated {
				m := evt.Meta
				trunc = m
			}
			continue
		}
		if evt.Message == nil {
			continue
		}
		switch {
		case asJSON:
			line, _ := json.Marshal(evt.Message)
			fmt.Fprintln(os.Stdout, string(line))
		case tty:
			collected = append(collected, evt.Message.Payload...)
		case raw:
			fmt.Fprint(os.Stdout, evt.Message.Payload)
		case !bare && (cliproto.IsTabularReadPipe(channel) || bareTarget != ""):
			// Phase 1.5.2: uncollared pipes (bareTarget != "") render
			// tabular by default — they're the messaging primitive and
			// share the inbox/broadcast shape semantically.
			renderReadMessageTabular(*evt.Message)
		default:
			fmt.Fprintln(os.Stdout, evt.Message.Payload)
		}
	}
	if tty && len(collected) > 0 {
		fmt.Fprint(os.Stdout, cliproto.RenderTerminal(collected, renderCols, renderRows))
	}
	// Forward-paging notice on stderr (kept off stdout so --json/--raw
	// streams stay clean and scripts piping payloads are unaffected). Tells
	// the agent more is waiting and how to get it.
	if trunc != nil && !asJSON && !raw {
		fmt.Fprintf(os.Stderr,
			"ppz: showing %d of %d unread on %s; run again for the next page, or --limit 0 to drain all\n",
			trunc.Shown, trunc.Unread, target)
	}
	if err := dec.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		// EOF / closed-by-server during follow on SIGINT is expected.
		return nil
	}
	return nil
}

// renderReadMessageTabular writes one message in the tabular default to
// stdout. Wrap width and colour are inferred from whether stdout is an
// interactive terminal via the TTY-gated helpers — non-interactive readers
// (pipes, agents, CI, redirects) MUST get reflow-free, uncoloured output so
// the rendered row stays byte-identical to the raw message newlines. This
// path only runs for non-JSON reads (the switch routes --json earlier), so
// colour keys off shouldUseColor(false). See
// TestRenderReadMessageTabular_NonTTYDoesNotWrap.
func renderReadMessageTabular(msg cliproto.ReadMessage) {
	cliproto.FormatReadMessage(os.Stdout, msg, time.Local, readBodyWidth(), shouldUseColor(false))
}

func currentInboxTarget() (string, error) {
	handle, err := effectiveCurrentHandle()
	if err != nil {
		return "", err
	}
	return handle + ".inbox", nil
}

// splitReadArgs lets `ppz read TGT --tail` and `ppz read --tail TGT` both
// work. Go's flag package stops at the first positional arg, so we pre-
// extract the single target. Flags that take a value absorb the next
// token unless written as --flag=value. `-l/--limit` (paging on `read`,
// tail-N on `reread`) always takes a value; `withFilters` widens the set
// further with --skip/--since for the `reread` verb; `read` rejects those
// two (the flagset will error on first encounter).
func splitReadArgs(args []string, withFilters bool) (target string, flagArgs []string, err error) {
	valueFlags := map[string]bool{
		"-l":      true,
		"-limit":  true,
		"--limit": true,
	}
	if withFilters {
		valueFlags["-skip"] = true
		valueFlags["--skip"] = true
		valueFlags["-since"] = true
		valueFlags["--since"] = true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			if strings.Contains(a, "=") || !valueFlags[a] {
				continue
			}
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("flag %s requires a value", a)
			}
			flagArgs = append(flagArgs, args[i+1])
			i++
			continue
		}
		if target != "" {
			return "", nil, fmt.Errorf("unexpected extra positional %q", a)
		}
		target = a
	}
	return target, flagArgs, nil
}
