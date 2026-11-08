package main

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Deikus-LXXVII/rubi/sdk/rubiplugin"
)

// request is a Claude Code permission request as the helper hands it out (home.ClaudeRequest).
type request struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	ToolName    string    `json:"tool_name"`
	ToolInput   string    `json:"tool_input"`
	Digest      string    `json:"digest"`
	Command     string    `json:"command"`
	Description string    `json:"description"`
	Cwd         string    `json:"cwd"`
	ExpiresAt   time.Time `json:"expires_at"`
	// Project is set for a request from a Rubi Home agent project (t3-agent); never by the hook helper.
	Project string `json:"project,omitempty"`
	// denyFailed: an approval that ends as "failed" denies (a batch whose execute step failed before it answered).
	denyFailed bool
}

// ticket is the approval's payload: which request on which computer the approval answers.
type ticket struct {
	Device     string `json:"device"`
	DeviceName string `json:"device_name,omitempty"`
	SessionID  string `json:"session_id"`
	Digest     string `json:"digest"`
	Cwd        string `json:"cwd,omitempty"` // the scope of a mandate given with this approval
	// AskID and AgentProject are set for a permission question of a Rubi Home agent project
	// (t3-agent.next): the answer goes to t3-agent.decide by AskID.
	AskID        string `json:"ask_id,omitempty"`
	AgentProject string `json:"agent_project,omitempty"`
}

type homeCaller interface {
	HomeCall(ctx context.Context, device, op string, args, out any) error
}

// host is what the watcher uses of *rubiplugin.Host (a fake in tests).
type host interface {
	sisterHost
	WaitApproval(ctx context.Context, id string, wait time.Duration) (rubiplugin.ApprovalState, error)
	Emit(typ string, data map[string]any) (string, error)
}

const (
	pollWait     = 25 * time.Second // the longest t3-agent.next wait
	refreshEvery = 30 * time.Second // how often the list of computers is read again
	maxBackoff   = time.Minute
)

// Denial reasons Claude is told (the helper puts "Rubi: " in front).
const (
	msgDenied    = "you denied this command in Rubi."
	msgCancelled = "the approval was cancelled in Rubi (or Rubi was locked)."
	msgExpired   = "the approval expired in Rubi."
	msgFailed    = "the approval failed"
)

type watcher struct {
	h       host
	s       *sisters // mandates, bindings and routing (nil: none)
	backoff func(n int) time.Duration
	mu      sync.Mutex
	polling map[string]bool
	wg      sync.WaitGroup
	// batchWindow is how long a session's first run approval waits for others of the same session (0: none, each ask
	// is its own approval); groups are the asks collected so far (agent_batch.go).
	batchWindow time.Duration
	bmu         sync.Mutex
	groups      map[string]*askGroup
}

func newWatcher(h host, s *sisters) *watcher {
	return &watcher{h: h, s: s, polling: map[string]bool{}, backoff: func(n int) time.Duration {
		return min(time.Duration(1<<min(n, 6))*time.Second, maxBackoff)
	}}
}

// run polls every Rubi Home computer until ctx ends, then waits for the requests in hand.
func (w *watcher) run(ctx context.Context) {
	defer w.wg.Wait()
	for {
		w.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(refreshEvery):
		}
	}
}

// refresh starts a poller for each computer that has none.
func (w *watcher) refresh(ctx context.Context) {
	devs, err := w.h.HomeDevices()
	if err != nil {
		w.h.Logf("listing Rubi Home computers: %v", err)
		return
	}
	// A computer whose Rubi Home reports the t3-agent capability is also polled for the agent's questions
	// and events (agent.go); the probe is cached and happens outside the lock.
	agent := map[string]bool{}
	if w.s != nil {
		for _, d := range devs {
			agent[d.ID] = w.s.agentCapable(ctx, w.h, d.ID)
		}
	}
	if w.s != nil {
		var ids []string
		for _, d := range devs {
			if agent[d.ID] {
				ids = append(ids, d.ID)
			}
		}
		for _, id := range ids {
			w.wg.Go(func() { w.s.nativeTaskTick(ctx, w.h, id) })
		}
		if len(ids) > 0 {
			w.wg.Go(func() { w.s.libraryTick(ctx, w.h, ids) })
			w.wg.Go(func() { w.s.sshAuditTick(ctx, w.h, ids) })
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, d := range devs {
		if agent[d.ID] && !w.polling[d.ID+"|"+opAgentNext] {
			key := d.ID + "|" + opAgentNext
			w.polling[key] = true
			w.wg.Go(func() {
				w.pollAgent(ctx, d)
				w.mu.Lock()
				delete(w.polling, key)
				w.mu.Unlock()
			})
		}
	}
}

// follow waits for your decision while Claude Code still waits for the answer.
func (w *watcher) follow(ctx context.Context, r request, id string, deny func(string)) {
	for ctx.Err() == nil {
		left := time.Until(r.ExpiresAt)
		if left <= 0 {
			return // Claude Code stopped waiting; the helper denied it
		}
		st, err := w.h.WaitApproval(ctx, id, min(left, pollWait))
		if err != nil {
			if ctx.Err() == nil {
				w.h.Logf("approval %s: %v", id, err)
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
				}
			}
			continue
		}
		switch st.State {
		case "pending", "executing":
			continue
		case "executed": // execute answered the helper (or found it no longer waiting)
		case "failed":
			if r.denyFailed {
				deny(msgFailed)
			}
		case "denied":
			deny(msgDenied)
		case "cancelled":
			deny(msgCancelled)
		case "expired":
			deny(msgExpired)
		default:
			deny("the approval ended as " + oneLine(st.State, 40) + ", so the command was denied.")
		}
		return
	}
}

// oneLine shortens s to n runes on one line, for summaries.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
