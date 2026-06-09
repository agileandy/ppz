package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestTerminalSubsAlertStateMachineDefersWhileUserActive(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	sm := newTerminalSubsAlertStateMachine(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Message:   terminalSubsAlertMessage,
	})

	sm.ObserveUserInput(now, []byte("partial prompt"))
	sm.ObserveSubsUnread(now.Add(time.Second))

	if got := sm.ReadyAlert(now.Add(14 * time.Second)); got != "" {
		t.Fatalf("ReadyAlert while user active = %q, want empty", got)
	}
}

func TestTerminalSubsAlertStateMachineInjectsAfterIdle(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	sm := newTerminalSubsAlertStateMachine(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Message:   terminalSubsAlertMessage,
	})

	sm.ObserveUserInput(now, []byte("partial prompt"))
	sm.ObserveSubsUnread(now.Add(time.Second))

	got := sm.ReadyAlert(now.Add(16 * time.Second))
	if !strings.Contains(got, "Please run 'ppz subs read' and action messages") {
		t.Fatalf("ReadyAlert after idle = %q, want subs alert text", got)
	}
	if !strings.Contains(got, "ppz subs read") {
		t.Fatalf("ReadyAlert after idle = %q, want ppz subs read guidance", got)
	}
}

func TestTerminalSubsAlertStateMachineCoalescesMultipleUnreadObservations(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	sm := newTerminalSubsAlertStateMachine(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Message:   terminalSubsAlertMessage,
	})

	sm.ObserveSubsUnread(now)
	sm.ObserveSubsUnread(now.Add(time.Second))
	sm.ObserveSubsUnread(now.Add(2 * time.Second))

	first := sm.ReadyAlert(now.Add(16 * time.Second))
	second := sm.ReadyAlert(now.Add(17 * time.Second))

	if first == "" {
		t.Fatal("first ReadyAlert is empty, want one coalesced alert")
	}
	if second != "" {
		t.Fatalf("second ReadyAlert = %q, want empty after coalescing", second)
	}
}

func TestTerminalSubsAlertPumpWritesClaudeSubmittedSubsAlertToPTYStdinAfterIdle(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	var ptyStdin bytes.Buffer
	pump := newTerminalSubsAlertPump(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Message:   terminalSubsAlertMessage,
		Harness:   "claude",
	}, &ptyStdin)

	pump.ObserveUserInput(now, []byte("half typed command"))
	pump.ObserveSubsUnread(now.Add(time.Second))

	if wrote := pump.Flush(now.Add(14 * time.Second)); wrote {
		t.Fatalf("Flush before idle wrote alert to PTY stdin: %q", ptyStdin.String())
	}
	if ptyStdin.Len() != 0 {
		t.Fatalf("PTY stdin before idle = %q, want empty", ptyStdin.String())
	}

	if wrote := pump.Flush(now.Add(16 * time.Second)); !wrote {
		t.Fatal("Flush after idle did not write alert to PTY stdin")
	}
	got := ptyStdin.String()
	if !strings.HasPrefix(got, "Please run 'ppz subs read' and action messages") {
		t.Fatalf("PTY stdin alert = %q, want plain Claude instruction", got)
	}
	if !strings.HasSuffix(got, "\x1b[13u") {
		t.Fatalf("PTY stdin alert = %q, want Claude CSI-Enter-terminated instruction", got)
	}
	if strings.Contains(got, "ppz alert") {
		t.Fatalf("PTY stdin alert = %q, should not include ppz alert wrapper", got)
	}
	if wrote := pump.Flush(now.Add(17 * time.Second)); wrote {
		t.Fatalf("second Flush wrote duplicate alert to PTY stdin: %q", ptyStdin.String())
	}
}

func TestTerminalSubsAlertPumpCoalescesSubsObservationsIntoOnePTYAlert(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	var ptyStdin bytes.Buffer
	pump := newTerminalSubsAlertPump(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Message:   terminalSubsAlertMessage,
	}, &ptyStdin)

	pump.ObserveSubsUnread(now)
	pump.ObserveSubsUnread(now.Add(time.Second))
	pump.ObserveSubsUnread(now.Add(2 * time.Second))

	if wrote := pump.Flush(now.Add(16 * time.Second)); !wrote {
		t.Fatal("Flush after idle did not write coalesced alert")
	}
	first := ptyStdin.String()
	if strings.Count(first, "Please run 'ppz subs read' and action messages") != 1 {
		t.Fatalf("PTY stdin after coalesced alert = %q, want exactly one alert", first)
	}
	if wrote := pump.Flush(now.Add(17 * time.Second)); wrote {
		t.Fatalf("second Flush wrote duplicate coalesced alert: %q", ptyStdin.String())
	}
}

func TestTerminalSubsAlertPumpBuffersUserInputDuringAlertMode(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	var ptyStdin bytes.Buffer
	pump := newTerminalSubsAlertPump(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Message:   terminalSubsAlertMessage,
	}, &ptyStdin)

	pump.ObserveSubsUnread(now)
	pump.BeginAlertMode(now.Add(16 * time.Second))

	if forwarded := pump.ForwardUserInput(now.Add(16*time.Second), []byte("typed during alert")); forwarded {
		t.Fatalf("ForwardUserInput returned true during alert mode; user input should be buffered")
	}
	if strings.Contains(ptyStdin.String(), "typed during alert") {
		t.Fatalf("PTY stdin received user input during alert mode: %q", ptyStdin.String())
	}

	pump.EndAlertMode(now.Add(17 * time.Second))
	if !strings.Contains(ptyStdin.String(), "typed during alert") {
		t.Fatalf("PTY stdin after alert mode = %q, want buffered user input flushed", ptyStdin.String())
	}
}

// TestSubmitAlertToPTY_Claude pins the claude path: kitty keyboard
// protocol Enter (`\x1b[13u`) is a single key-event escape, so
// claude's REPL submits whatever input is on the line when it sees
// the escape regardless of whether the bytes arrived in one write
// or several. No pause is needed — and adding one would slow every
// claude alert by 100ms for zero benefit. The test injects a
// recording sleeper to verify it's never called on the claude path.
func TestSubmitAlertToPTY_Claude(t *testing.T) {
	var buf bytes.Buffer
	var sleeps []time.Duration
	if err := submitAlertToPTY(&buf, "claude", "hello\n", func(d time.Duration) {
		sleeps = append(sleeps, d)
	}); err != nil {
		t.Fatalf("submitAlertToPTY: %v", err)
	}
	if len(sleeps) != 0 {
		t.Errorf("claude path called sleep %d time(s) (%v); kitty Enter is a single key event, no pause needed", len(sleeps), sleeps)
	}
	if buf.String() != "hello\x1b[13u" {
		t.Errorf("claude buf=%q; want \"hello\\x1b[13u\" (message + kitty Enter, no trailing CR/LF before terminator)", buf.String())
	}
}

// TestSubmitAlertToPTY_NonClaude_PausesBeforeCarriageReturn pins
// the fix for the user-observed bug: with the message + `\r` in a
// single write burst, copilot and codex were treating the CR as a
// literal newline inside the line rather than as a submit. The
// working pattern in `ppz command -cr` (cmdCommand at
// command.go:93) writes the message, waits 100ms, then writes the
// CR — two writes with a pause between them. Mirror that here.
//
// The test injects a sleeper that snapshots the buffer at the
// moment sleep is called, so we can prove three things atomically:
//   1. The pause happens exactly once, at 100ms (matches cmdCommand).
//   2. The CR has NOT been written yet when sleep is called — the
//      message is on the wire alone, giving the REPL time to flush
//      it before the submit byte arrives.
//   3. The final buffer is message + `\r` (sequence preserved).
//
// Runs over every harness that takes the `\r` arm: known
// non-claude harnesses, plus empty (non-agent share) and a bogus
// string (forward-compat default).
func TestSubmitAlertToPTY_NonClaude_PausesBeforeCarriageReturn(t *testing.T) {
	for _, h := range []string{"copilot", "codex", "agy", "pi", "", "bogus"} {
		t.Run(h, func(t *testing.T) {
			var buf bytes.Buffer
			var snapshot string
			var sleeps []time.Duration
			if err := submitAlertToPTY(&buf, h, "hello\n", func(d time.Duration) {
				snapshot = buf.String()
				sleeps = append(sleeps, d)
			}); err != nil {
				t.Fatalf("submitAlertToPTY(%q): %v", h, err)
			}
			if len(sleeps) != 1 {
				t.Fatalf("harness %q: sleep called %d time(s); want exactly 1 (cmdCommand -cr uses one 100ms pause between message and CR; same pattern)", h, len(sleeps))
			}
			if sleeps[0] != 100*time.Millisecond {
				t.Errorf("harness %q: sleep duration=%v; want 100ms (matches cmdCommand at command.go:93)", h, sleeps[0])
			}
			if snapshot != "hello" {
				t.Errorf("harness %q: buffer at pause = %q; want \"hello\" (message only; CR must not be written until after the pause — otherwise copilot/codex bundle it with the message and treat it as a literal newline)", h, snapshot)
			}
			if buf.String() != "hello\r" {
				t.Errorf("harness %q: final buf=%q; want \"hello\\r\"", h, buf.String())
			}
		})
	}
}

// TestTerminalSubsAlertPump_CopilotHarness_UsesCarriageReturnSubmit
// pins the integration: the pump must thread cfg.Harness through
// to its write callback so the on-PTY bytes carry the
// harness-appropriate submit terminator. Without the wiring,
// configuring Harness: "copilot" would still produce the kitty
// escape and copilot's input buffer keeps showing the literal
// alert message — exactly the user-observed bug.
func TestTerminalSubsAlertPump_CopilotHarness_UsesCarriageReturnSubmit(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	var ptyStdin bytes.Buffer
	pump := newTerminalSubsAlertPump(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Message:   terminalSubsAlertMessage,
		Harness:   "copilot",
	}, &ptyStdin)

	pump.ObserveSubsUnread(now)
	if wrote := pump.Flush(now.Add(16 * time.Second)); !wrote {
		t.Fatal("Flush after idle did not write alert")
	}
	got := ptyStdin.String()
	if !strings.HasPrefix(got, "Please run 'ppz subs read' and action messages") {
		t.Errorf("PTY stdin alert = %q, want plain alert text prefix", got)
	}
	if !strings.HasSuffix(got, "\r") {
		t.Errorf("PTY stdin alert = %q, want trailing `\\r` (copilot's REPL submits on carriage return; kitty Enter would leave the alert literal in the input buffer)", got)
	}
	if strings.Contains(got, "\x1b[13u") {
		t.Errorf("PTY stdin alert = %q, must not contain claude's kitty Enter escape on copilot harness", got)
	}
}

// TestTerminalSubsAlertStateMachineClearCancelsPending and
// TestTerminalSubsAlertPumpSilentAfterCursorAdvanced pin the fix for
// the double-delivery bug: after the first alert fires, the
// level-triggered subs wait loop re-sets pending within 250ms (the
// message is still unread). Without a way to cancel that pending bit
// when the cursor IS eventually advanced, the pump fires a second alert
// once the cooldown expires — even though the agent already handled the
// message. ObserveSubsClear is the missing cancellation: the pump calls
// it when subs wait returns an empty reply (unread=0), meaning the
// agent ran ppz subs read and advanced the cursor. After a clear, no
// further alert should fire for that delivery.

func TestTerminalSubsAlertStateMachineClearCancelsPending(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	sm := newTerminalSubsAlertStateMachine(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Cooldown:  30 * time.Second,
		Message:   terminalSubsAlertMessage,
	})

	sm.ObserveSubsUnread(now)
	sm.ReadyAlert(now.Add(16 * time.Second)) // first alert fires

	// Level-triggered loop re-sets pending 250ms later (still unread).
	sm.ObserveSubsUnread(now.Add(16*time.Second + 250*time.Millisecond))
	// Agent runs ppz subs read — cursor advances, next subs wait empty.
	sm.ObserveSubsClear(now.Add(17 * time.Second))

	if got := sm.ReadyAlert(now.Add(48 * time.Second)); got != "" {
		t.Fatalf("ReadyAlert after cursor advanced = %q, want empty (message was handled)", got)
	}
}

func TestTerminalSubsAlertStateMachineFiringAfterClearThenNewUnread(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	sm := newTerminalSubsAlertStateMachine(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Message:   terminalSubsAlertMessage,
	})

	sm.ObserveSubsUnread(now)
	sm.ReadyAlert(now.Add(16 * time.Second)) // first alert fires

	sm.ObserveSubsUnread(now.Add(16*time.Second + 250*time.Millisecond)) // level-triggered re-set
	sm.ObserveSubsClear(now.Add(17 * time.Second))                        // cursor advanced

	// New message arrives after the clear.
	sm.ObserveSubsUnread(now.Add(18 * time.Second))

	if got := sm.ReadyAlert(now.Add(34 * time.Second)); got == "" {
		t.Fatal("ReadyAlert after clear + new unread = empty, want alert (new message must still fire)")
	}
}

func TestTerminalSubsAlertPumpSilentAfterCursorAdvanced(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	var ptyStdin bytes.Buffer
	pump := newTerminalSubsAlertPump(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Cooldown:  30 * time.Second,
		Message:   terminalSubsAlertMessage,
	}, &ptyStdin)

	pump.ObserveSubsUnread(now)
	if wrote := pump.Flush(now.Add(16 * time.Second)); !wrote {
		t.Fatal("first Flush did not write alert")
	}

	// Level-triggered subs wait re-sets pending 250ms after the alert
	// fires (still unread; agent hasn't read yet).
	pump.ObserveSubsUnread(now.Add(16*time.Second + 250*time.Millisecond))
	// Agent runs ppz subs read — cursor advances, pump observes empty reply.
	pump.ObserveSubsClear(now.Add(17 * time.Second))

	if wrote := pump.Flush(now.Add(48 * time.Second)); wrote {
		t.Fatalf("Flush after cursor advanced wrote second alert: %q, want silent", ptyStdin.String())
	}
}

func TestTerminalSubsAlertPumpCooldownSuppressesImmediateRepeatedAlerts(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	var ptyStdin bytes.Buffer
	pump := newTerminalSubsAlertPump(terminalSubsAlertConfig{
		IdleAfter: 15 * time.Second,
		Cooldown:  30 * time.Second,
		Message:   terminalSubsAlertMessage,
	}, &ptyStdin)

	pump.ObserveSubsUnread(now)
	if wrote := pump.Flush(now.Add(16 * time.Second)); !wrote {
		t.Fatal("first Flush did not write alert")
	}

	pump.ObserveSubsUnread(now.Add(17 * time.Second))
	if wrote := pump.Flush(now.Add(20 * time.Second)); wrote {
		t.Fatalf("Flush during cooldown wrote repeated alert: %q", ptyStdin.String())
	}

	if wrote := pump.Flush(now.Add(47 * time.Second)); !wrote {
		t.Fatal("Flush after cooldown did not write pending repeated alert")
	}
	if strings.Count(ptyStdin.String(), "Please run 'ppz subs read' and action messages") != 2 {
		t.Fatalf("PTY stdin after cooldown = %q, want two total alerts", ptyStdin.String())
	}
}
