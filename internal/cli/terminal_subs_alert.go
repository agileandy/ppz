package cli

import (
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const terminalSubsAlertMessage = "Please run 'ppz subs read' and action messages\n"

type terminalSubsAlertConfig struct {
	IdleAfter time.Duration
	Cooldown  time.Duration
	Message   string
	// Harness identifies which agent harness the wrapped PTY is
	// running (one of "claude" / "copilot" / "codex" / "agy" /
	// "pi", or empty for non-agent shares). Used by
	// submitAlertToPTY to pick the right submit-key byte
	// sequence — claude reads `\x1b[13u` (kitty keyboard protocol
	// Enter), other harnesses' REPLs treat that escape as literal
	// bytes and need a plain `\r` to submit.
	Harness string
}

type terminalSubsAlertStateMachine struct {
	mu            sync.Mutex
	cfg           terminalSubsAlertConfig
	pending       bool
	pendingSince  time.Time
	lastUserInput time.Time
	lastAlert     time.Time
}

func newTerminalSubsAlertStateMachine(cfg terminalSubsAlertConfig) *terminalSubsAlertStateMachine {
	if cfg.IdleAfter <= 0 {
		cfg.IdleAfter = 15 * time.Second
	}
	if cfg.Message == "" {
		cfg.Message = terminalSubsAlertMessage
	}
	return &terminalSubsAlertStateMachine{cfg: cfg}
}

func (s *terminalSubsAlertStateMachine) ObserveUserInput(now time.Time, input []byte) {
	if len(input) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUserInput = now
}

// ObserveSubsUnread flips the pending bit (idempotent on repeat
// observations within a single pending window). The first call
// after a clear stamps pendingSince so the idle-after gate can
// measure how long the unread has been outstanding without user
// input.
func (s *terminalSubsAlertStateMachine) ObserveSubsUnread(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending {
		s.pendingSince = now
	}
	s.pending = true
}

// ObserveSubsClear cancels any pending alert. Called when subs wait
// returns an empty reply (unread=0), meaning the agent advanced the
// cursor via ppz subs read — the message was handled.
func (s *terminalSubsAlertStateMachine) ObserveSubsClear(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = false
}

func (s *terminalSubsAlertStateMachine) ReadyAlert(now time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready(now) {
		return ""
	}
	s.pending = false
	s.lastAlert = now
	return s.cfg.Message
}

func (s *terminalSubsAlertStateMachine) ready(now time.Time) bool {
	if !s.pending {
		return false
	}
	if !s.lastUserInput.IsZero() && now.Sub(s.lastUserInput) < s.cfg.IdleAfter {
		return false
	}
	if s.lastUserInput.IsZero() && !s.pendingSince.IsZero() && now.Sub(s.pendingSince) < s.cfg.IdleAfter {
		return false
	}
	if s.cfg.Cooldown > 0 && !s.lastAlert.IsZero() && now.Sub(s.lastAlert) < s.cfg.Cooldown {
		return false
	}
	return true
}

type terminalSubsAlertPump struct {
	mu     sync.Mutex
	sm     *terminalSubsAlertStateMachine
	pty    io.Writer
	write  func(string)
	alert  bool
	buffer []byte
}

func newTerminalSubsAlertPump(cfg terminalSubsAlertConfig, pty io.Writer) *terminalSubsAlertPump {
	if cfg.Message == "" {
		cfg.Message = terminalSubsAlertMessage
	}
	harness := cfg.Harness
	return &terminalSubsAlertPump{
		sm:  newTerminalSubsAlertStateMachine(cfg),
		pty: pty,
		write: func(message string) {
			_ = submitAlertToPTY(pty, harness, message, time.Sleep)
		},
	}
}

func newTerminalSubsAlertPumpForPTY(cfg terminalSubsAlertConfig, pty *os.File) *terminalSubsAlertPump {
	pump := newTerminalSubsAlertPump(cfg, pty)
	harness := cfg.Harness
	pump.write = func(message string) {
		restore := setPTYInputEcho(pty.Fd(), false)
		defer restore()
		_ = submitAlertToPTY(pty, harness, message, time.Sleep)
	}
	return pump
}

func (p *terminalSubsAlertPump) ObserveUserInput(now time.Time, input []byte) {
	p.sm.ObserveUserInput(now, input)
}

// ObserveSubsUnread is the source-side handle the forwardSubsAlerts
// loop calls each time SubsWait returns a reply with unread rows.
// The pump only needs to know "something is unread" — the row
// detail is for the agent's subsequent `ppz subs read`, not for
// the alert text — so this takes only `now`.
func (p *terminalSubsAlertPump) ObserveSubsUnread(now time.Time) {
	p.sm.ObserveSubsUnread(now)
}

// ObserveSubsClear cancels any pending alert. Called when subs wait
// returns an empty reply, meaning the agent advanced the cursor.
func (p *terminalSubsAlertPump) ObserveSubsClear(now time.Time) {
	p.sm.ObserveSubsClear(now)
}

func (p *terminalSubsAlertPump) Flush(now time.Time) bool {
	alert := p.sm.ReadyAlert(now)
	if alert == "" {
		return false
	}
	p.BeginAlertMode(now)
	p.write(alert)
	p.EndAlertMode(now)
	return true
}

// submitAlertToPTY writes the alert message followed by a
// harness-appropriate Enter-equivalent into w. Claude Code reads
// `\x1b[13u` (kitty keyboard protocol Enter) as a single key event,
// so we send the message and terminator in one write. Every other
// harness's REPL needs a plain `\r` to submit — but only when the CR
// arrives slightly after the message bytes: copilot and codex were
// observed treating the CR as a literal newline inside the line
// rather than a submit when it shipped in the same write burst as
// the message. `ppz command -cr` already uses a 100ms pause between
// the message and the CR (see cmdCommand at command.go:93) and works
// reliably on both harnesses, so we mirror that pattern.
//
// sleep is injected so tests can verify the pause happened without
// blocking the test process. Production callers pass time.Sleep.
//
// Empty/unknown harness — non-agent `ppz terminal share` calls
// where PPZ_AGENT_HARNESS is unset, or a harness we haven't yet
// confirmed — falls into the `\r`+pause arm: a plain carriage return
// is the lowest-risk default since most line-discipline REPLs accept
// it as Enter, and the pause never hurts a REPL that would have
// accepted CR in the same burst.
func submitAlertToPTY(w io.Writer, harness, message string, sleep func(time.Duration)) error {
	trimmed := strings.TrimRight(message, "\r\n")
	if harness == "claude" {
		_, err := io.WriteString(w, trimmed+"\x1b[13u")
		return err
	}
	if _, err := io.WriteString(w, trimmed); err != nil {
		return err
	}
	sleep(100 * time.Millisecond)
	_, err := io.WriteString(w, "\r")
	return err
}

func (p *terminalSubsAlertPump) BeginAlertMode(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.alert = true
}

func (p *terminalSubsAlertPump) EndAlertMode(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.alert = false
	if len(p.buffer) > 0 {
		_, _ = p.pty.Write(p.buffer)
		p.buffer = nil
	}
}

func (p *terminalSubsAlertPump) ForwardUserInput(now time.Time, input []byte) bool {
	if len(input) == 0 {
		return true
	}
	p.ObserveUserInput(now, input)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.alert {
		p.buffer = append(p.buffer, input...)
		return false
	}
	_, _ = p.pty.Write(input)
	return true
}
