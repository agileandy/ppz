package daemon

import (
	"testing"

	"github.com/pipescloud/ppz/internal/cliproto"
)

// msgs builds a synthetic drained window "m0".."m{n-1}" with stream seqs
// 10,11,... so tests can assert both the delivered payloads and the seq the
// cursor should advance to.
func pagingFixture(n int) (retained []cliproto.ReadMessage, seqs []uint64) {
	for i := 0; i < n; i++ {
		retained = append(retained, cliproto.ReadMessage{Payload: string(rune('a' + i))})
		seqs = append(seqs, uint64(10+i))
	}
	return retained, seqs
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// trimHead is the forward-paging primitive: deliver the FIRST `head` messages
// (oldest first) and report the seq the session cursor should advance to — the
// last DELIVERED message's seq, so the unread remainder surfaces next read.
func TestTrimHead_ForwardPageAndPartialAdvance(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		head    int
		want    []string
		wantSeq uint64
	}{
		{"head-disabled-delivers-all", 5, 0, []string{"a", "b", "c", "d", "e"}, 14},
		{"head-2-of-5-first-two", 5, 2, []string{"a", "b"}, 11},
		{"head-1-of-5", 5, 1, []string{"a"}, 10},
		{"head-ge-len-delivers-all", 3, 9, []string{"a", "b", "c"}, 12},
		{"head-eq-len", 3, 3, []string{"a", "b", "c"}, 12},
		{"empty-window", 0, 5, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retained, seqs := pagingFixture(tc.n)
			got, advSeq := trimHead(retained, seqs, tc.head)
			if !eq(payloads(got), tc.want) {
				t.Fatalf("deliver = %v, want %v", payloads(got), tc.want)
			}
			if advSeq != tc.wantSeq {
				t.Fatalf("advanceSeq = %d, want %d (cursor must advance only past the delivered head)", advSeq, tc.wantSeq)
			}
		})
	}
}

// trimTail is the reread forensic filter (skip then tail-N). This is a
// REGRESSION guard: the semantics must exactly match the pre-refactor inline
// logic (read.go: skip first, then keep the most-recent `limit`).
func TestTrimTail_RereadRegression(t *testing.T) {
	cases := []struct {
		name  string
		n     int
		skip  int
		limit int
		want  []string
	}{
		{"no-filters-all", 5, 0, 0, []string{"a", "b", "c", "d", "e"}},
		{"tail-2", 5, 0, 2, []string{"d", "e"}},
		{"skip-2", 5, 2, 0, []string{"c", "d", "e"}},
		{"skip-1-tail-2", 5, 1, 2, []string{"d", "e"}},
		{"skip-ge-len-empty", 3, 3, 0, nil},
		{"limit-ge-len-all", 3, 0, 9, []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retained, _ := pagingFixture(tc.n)
			got := trimTail(retained, tc.skip, tc.limit)
			if !eq(payloads(got), tc.want) {
				t.Fatalf("trimTail = %v, want %v", payloads(got), tc.want)
			}
		})
	}
}
