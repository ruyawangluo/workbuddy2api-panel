package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func waitQueueDone(t *testing.T, p *Panel) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		q := p.queue()
		q.mu.Lock()
		running := q.running
		q.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("队列 5s 内未结束")
}

// TestGrowthQueueExecutesAllAccounts 复现「任务中心执行全部待办」多账号路径：
// 两个账号各有一项 not_accepted 的 Buddy_App，队列应各自 accept → 上报 → 领奖。
func TestGrowthQueueExecutesAllAccounts(t *testing.T) {
	oldGap, oldPoll := reportGap, claimPollGap
	reportGap, claimPollGap = 0, 0
	defer func() { reportGap, claimPollGap = oldGap, oldPoll }()

	type acctState struct {
		mu       sync.Mutex
		accepted bool
		current  int64
		reports  int
		claims   int
	}
	states := map[string]*acctState{"u1": {}, "u2": {}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		uid := strings.TrimPrefix(tok, "at-")
		st := states[uid]
		if st == nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Path == "/v2/activity/growth/tasks" && r.Method == http.MethodGet:
			st.mu.Lock()
			status := "not_accepted"
			if st.accepted {
				status = "accepted"
			}
			cur := st.current
			st.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{"tasks": []map[string]any{
				{"task_code": "Buddy_App", "title": "进入应用", "target": 1, "current": cur, "accept_status": status},
			}}})
		case r.URL.Path == "/v2/activity/growth/tasks/accept":
			st.mu.Lock()
			st.accepted = true
			st.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{}})
		case r.URL.Path == "/v2/report":
			st.mu.Lock()
			st.current = 1
			st.reports++
			st.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{}})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			st.mu.Lock()
			st.claims++
			st.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{"already_claimed": false, "credit": 100, "energy": 5}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	pl := pool.New("")
	pl.Add(&auth.Auth{UID: "u1", AccessToken: "at-u1", Nickname: "A"})
	pl.Add(&auth.Auth{UID: "u2", AccessToken: "at-u2", Nickname: "B"})
	p := &Panel{cfg: Config{Pool: pl, Upstream: &upstream.Client{
		HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
	}}}

	started, total, seq, msg := p.startGrowthQueue(2, true)
	if !started {
		t.Fatalf("queue not started: total=%d seq=%d msg=%s", total, seq, msg)
	}
	if total != 2 {
		t.Fatalf("total=%d want 2", total)
	}

	waitQueueDone(t, p)

	for uid, st := range states {
		st.mu.Lock()
		reports, claims, accepted := st.reports, st.claims, st.accepted
		st.mu.Unlock()
		if !accepted {
			t.Errorf("%s 未被 accept", uid)
		}
		if reports != 1 {
			t.Errorf("%s report=%d want 1", uid, reports)
		}
		if claims != 1 {
			t.Errorf("%s claim=%d want 1", uid, claims)
		}
	}

	q := p.queue()
	q.mu.Lock()
	items := append([]queueItem(nil), q.items...)
	q.mu.Unlock()
	for _, it := range items {
		if it.Status != "done" {
			t.Errorf("item %s/%s status=%s msg=%s", it.UID, it.Code, it.Status, it.Message)
		}
	}
}

// TestGrowthQueueClaimsDoneWithoutRerun 队列遇到「已达标未领」的任务时应直接补领，
// 不重跑行为链路（与 runAutoAll 同口径）。
func TestGrowthQueueClaimsDoneWithoutRerun(t *testing.T) {
	oldGap, oldPoll := reportGap, claimPollGap
	reportGap, claimPollGap = 0, 0
	defer func() { reportGap, claimPollGap = oldGap, oldPoll }()

	var reports, claims int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/activity/growth/tasks" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{"tasks": []map[string]any{
				{"task_code": "Buddy_App", "title": "进入应用", "target": 1, "current": 1, "accept_status": "accepted"},
			}}})
		case r.URL.Path == "/v2/report":
			atomic.AddInt32(&reports, 1)
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{}})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			atomic.AddInt32(&claims, 1)
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{"already_claimed": false, "credit": 100, "energy": 5}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "OK", "data": map[string]any{}})
		}
	}))
	defer srv.Close()

	pl := pool.New("")
	pl.Add(&auth.Auth{UID: "u1", AccessToken: "at-u1", Nickname: "A"})
	p := &Panel{cfg: Config{Pool: pl, Upstream: &upstream.Client{
		HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
	}}}

	started, total, _, msg := p.startGrowthQueue(1, true)
	if !started {
		t.Fatalf("queue not started: total=%d msg=%s", total, msg)
	}

	waitQueueDone(t, p)

	if got := atomic.LoadInt32(&reports); got != 0 {
		t.Errorf("report=%d want 0（已达标不应重跑行为链路）", got)
	}
	if got := atomic.LoadInt32(&claims); got != 1 {
		t.Errorf("claim=%d want 1", got)
	}

	q := p.queue()
	q.mu.Lock()
	items := append([]queueItem(nil), q.items...)
	q.mu.Unlock()
	if len(items) != 1 || items[0].Status != "done" {
		t.Fatalf("items=%+v, want 1 项 done", items)
	}
}
