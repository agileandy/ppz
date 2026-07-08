package daemon

import (
	"reflect"
	"testing"

	"github.com/pipescloud/ppz/internal/cliproto"
)

func payloads(msgs []cliproto.ReadMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Payload
	}
	return out
}

// sortRetainedByPriority reorders the delivered window high-first
// (1 < 2 < 3 on EffectivePriority). Stable: FIFO stream-sequence order
// is preserved within a tier, and unset (0) messages interleave with
// explicit mediums exactly as they arrived.
func TestSortRetainedByPriority_HighFirst(t *testing.T) {
	retained := []cliproto.ReadMessage{
		{Payload: "low", Priority: 3},
		{Payload: "medium", Priority: 2},
		{Payload: "high", Priority: 1},
	}
	sortRetainedByPriority(retained)
	want := []string{"high", "medium", "low"}
	if got := payloads(retained); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestSortRetainedByPriority_UnsetInterleavesWithMediumFIFO(t *testing.T) {
	retained := []cliproto.ReadMessage{
		{Payload: "unset-a", Priority: 0},
		{Payload: "medium-b", Priority: 2},
		{Payload: "high", Priority: 1},
		{Payload: "unset-c", Priority: 0},
	}
	sortRetainedByPriority(retained)
	want := []string{"high", "unset-a", "medium-b", "unset-c"}
	if got := payloads(retained); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// All-equal input must come out byte-identical — the design invariant
// that a mesh where nobody sets priority behaves exactly like today.
func TestSortRetainedByPriority_AllEqualIsNoOp(t *testing.T) {
	retained := []cliproto.ReadMessage{
		{Payload: "first"},
		{Payload: "second"},
		{Payload: "third"},
	}
	sortRetainedByPriority(retained)
	want := []string{"first", "second", "third"}
	if got := payloads(retained); !reflect.DeepEqual(got, want) {
		t.Fatalf("all-equal input reordered: %v, want %v", got, want)
	}
}

// Same-tier messages keep arrival order even when other tiers sort
// around them (stability across a real mix).
func TestSortRetainedByPriority_StableWithinTier(t *testing.T) {
	retained := []cliproto.ReadMessage{
		{Payload: "high-a", Priority: 1},
		{Payload: "low-a", Priority: 3},
		{Payload: "high-b", Priority: 1},
		{Payload: "low-b", Priority: 3},
	}
	sortRetainedByPriority(retained)
	want := []string{"high-a", "high-b", "low-a", "low-b"}
	if got := payloads(retained); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// Garbage priorities (written straight onto NATS by a foreign publisher,
// bypassing handleSend) clamp to medium — no super-priority tier.
func TestSortRetainedByPriority_GarbageClampsToMedium(t *testing.T) {
	retained := []cliproto.ReadMessage{
		{Payload: "garbage-neg", Priority: -5},
		{Payload: "high", Priority: 1},
		{Payload: "garbage-big", Priority: 99},
	}
	sortRetainedByPriority(retained)
	want := []string{"high", "garbage-neg", "garbage-big"}
	if got := payloads(retained); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// shouldSortByPriority gates the sort to message-shaped reads only:
//   - never in follow mode (--tail must keep ONE ordering discipline for
//     the whole stream — the live half can't be reordered, so the drained
//     backlog isn't either);
//   - never for byte-faithful pipes (stdout / stdin / stdctrl / custom):
//     WIRE.md §8 promises those replay in arrival order, byte-for-byte;
//   - uncollared reads (BareTarget set) mirror the CLI's tabular default
//     (read.go render switch), so they do sort.
func TestShouldSortByPriority(t *testing.T) {
	cases := []struct {
		name       string
		follow     bool
		channel    string
		bareTarget string
		want       bool
	}{
		{"inbox drain", false, "inbox", "", true},
		{"broadcast drain", false, "broadcast", "", true},
		{"uncollared drain", false, "", "room", true},
		{"inbox follow", true, "inbox", "", false},
		{"uncollared follow", true, "", "room", false},
		{"stdout pipe", false, "stdout", "", false},
		{"stdin pipe", false, "stdin", "", false},
		{"stdctrl pipe", false, "stdctrl", "", false},
		{"custom pipe", false, "mylog", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldSortByPriority(c.follow, c.channel, c.bareTarget); got != c.want {
				t.Fatalf("shouldSortByPriority(%v, %q, %q) = %v, want %v",
					c.follow, c.channel, c.bareTarget, got, c.want)
			}
		})
	}
}
