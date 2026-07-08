package cli

import (
	"flag"
	"os"
	"time"
)

// cmdReread: ppz reread <handle>.<pipe> [-l N --skip N --since DUR --json --tty --raw]
//
// `reread` is the forensic / replay verb: it ignores the session
// cursor entirely (delivers every retained message in the pipe) and
// never advances it. Use cases: audit a pipe's history, rerun an
// agent against a known input window, dump a wrapped pty session
// from the beginning. The cursor-driven inbox poll is `ppz read`.
//
// All filter flags live here:
//
//	-l N         tail-N: only the most recent N retained messages.
//	--skip N     drop the first N retained messages.
//	--since DUR  only messages newer than (now − DUR), e.g. 5m, 1h.
//
// Output modes are shared with `read` (--json / --tty / --raw); see
// cmdRead for the full description.
//
// `--tail` is intentionally not supported: a forensic dump that then
// follows live messages is incoherent (it would either drift the
// cursor or accumulate unbounded unread). Use `ppz read --tail`.
func cmdReread(args []string) error {
	if wantsHelp(args) {
		printHelp(os.Stdout, "reread")
		return nil
	}
	fs := flag.NewFlagSet("reread", flag.ExitOnError)
	limit := fs.Int("l", 0, "limit to the N most recent messages (tail-N)")
	skip := fs.Int("skip", 0, "skip the first N retained messages")
	since := fs.Duration("since", 0, "only messages newer than this duration ago (e.g. 5m, 1h)")
	asJSON := fs.Bool("json", false, "emit JSON envelopes instead of payload text")
	tty := fs.Bool("tty", false, "render concatenated payloads through a virtual terminal (vt10x)")
	raw := fs.Bool("raw", false, "write payload bytes verbatim with no message separator")
	bare := fs.Bool("bare", false, "force legacy payload-only output (script-stable opt-out from the v0.23 tabular default on inbox-shaped pipes)")
	target, flagArgs, err := splitReadArgs(args, true)
	if err != nil || target == "" {
		usageExit("reread")
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	var sinceMS int64
	if *since > 0 {
		sinceMS = int64(*since / time.Millisecond)
	}
	return runRead(target, *asJSON, false /* follow */, *tty, *raw, *bare, true /* all */, *limit, *skip, sinceMS, 0 /* head: reread never forward-pages */)
}
