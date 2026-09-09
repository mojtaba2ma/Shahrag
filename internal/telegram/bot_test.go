package telegram

// The bot, tested against a stub Telegram server.
//
// No network: apiBase is redirected at an httptest server that speaks the
// same shapes. That makes the tests fast, deterministic, and runnable on a
// machine that cannot reach api.telegram.org — which, given where this
// project runs, is the normal case.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubTelegram is a minimal Bot API.
type stubTelegram struct {
	mu       sync.Mutex
	sent     []string
	sentTo   []int64
	updates  []update
	getCalls int32
	srv      *httptest.Server
}

func newStub(t *testing.T) *stubTelegram {
	t.Helper()
	s := &stubTelegram{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "getUpdates"):
			atomic.AddInt32(&s.getCalls, 1)
			s.mu.Lock()
			ups := s.updates
			s.updates = nil
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok": true, "result": ups,
			})
		case strings.Contains(r.URL.Path, "sendMessage"):
			body, _ := io.ReadAll(r.Body)
			var p struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			_ = json.Unmarshal(body, &p)
			s.mu.Lock()
			s.sent = append(s.sent, p.Text)
			s.sentTo = append(s.sentTo, p.ChatID)
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
		default:
			w.WriteHeader(404)
		}
	}))
	old := apiBase
	apiBase = s.srv.URL
	t.Cleanup(func() { apiBase = old; s.srv.Close() })
	return s
}

func (s *stubTelegram) queue(chat int64, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, update{
		UpdateID: int64(len(s.updates) + 1),
		Message: &struct {
			Text string `json:"text"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		}{Text: text, Chat: struct {
			ID int64 `json:"id"`
		}{ID: chat}},
	})
}

func (s *stubTelegram) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

// fakeReporter answers with recognisable text.
type fakeReporter struct{ calls int32 }

func (f *fakeReporter) HealthText() string   { atomic.AddInt32(&f.calls, 1); return "HEALTH-BODY" }
func (f *fakeReporter) MapText() string      { return "MAP-BODY" }
func (f *fakeReporter) ServicesText() string { return "SERVICES-BODY" }
func (f *fakeReporter) BansText() string     { return "BANS-BODY" }
func (f *fakeReporter) StatsText() string    { return "STATS-BODY" }

// ── Authorisation ────────────────────────────────────────────

// The single most important property: a bot with no chat ids configured
// must be inert, not open to whoever finds it.
func TestAnUnconfiguredBotIsInertNotOpen(t *testing.T) {
	r := &fakeReporter{}
	for _, b := range []*Bot{
		New("", nil, r),
		New("token", nil, r),
		New("", []int64{123}, r),
	} {
		if b.Configured() {
			t.Errorf("a bot with a missing token or no chat ids reports itself configured")
		}
		b.Start()
		if b.Running() {
			t.Errorf("an unconfigured bot started polling")
		}
	}
}

// A stranger gets NOTHING — not even a refusal. An error reply confirms
// the bot exists and that the chat id was merely wrong.
func TestAStrangerIsIgnoredInSilence(t *testing.T) {
	s := newStub(t)
	b := New("tok", []int64{111}, &fakeReporter{})

	s.queue(999, "/health")
	if _, err := b.pollOnce(); err != nil {
		t.Fatal(err)
	}
	if got := s.messages(); len(got) != 0 {
		t.Fatalf("the bot answered a stranger: %v", got)
	}
}

// ...and the offset still advances, or an unauthorised message would be
// redelivered for ever and the bot would never make progress.
func TestAnIgnoredMessageStillAdvancesTheOffset(t *testing.T) {
	s := newStub(t)
	b := New("tok", []int64{111}, &fakeReporter{})

	s.queue(999, "/health")
	if _, err := b.pollOnce(); err != nil {
		t.Fatal(err)
	}
	if b.offset == 0 {
		t.Fatal("the offset did not advance past an ignored message, so it " +
			"would be redelivered for ever")
	}
}

func TestAnAllowedChatGetsAnAnswer(t *testing.T) {
	s := newStub(t)
	b := New("tok", []int64{111}, &fakeReporter{})

	s.queue(111, "/health")
	if _, err := b.pollOnce(); err != nil {
		t.Fatal(err)
	}
	got := s.messages()
	if len(got) != 1 || !strings.Contains(got[0], "HEALTH-BODY") {
		t.Fatalf("wrong answer: %v", got)
	}
}

// ── Commands ─────────────────────────────────────────────────

func TestEveryCommandIsAnswered(t *testing.T) {
	s := newStub(t)
	b := New("tok", []int64{111}, &fakeReporter{})

	for cmd, want := range map[string]string{
		"/health":   "HEALTH-BODY",
		"/status":   "HEALTH-BODY",
		"/map":      "MAP-BODY",
		"/routes":   "MAP-BODY",
		"/services": "SERVICES-BODY",
		"/bans":     "BANS-BODY",
		"/stats":    "STATS-BODY",
		"/help":     "Shahrag",
		"/start":    "Shahrag",
	} {
		s.mu.Lock()
		s.sent = nil
		s.mu.Unlock()
		s.queue(111, cmd)
		if _, err := b.pollOnce(); err != nil {
			t.Fatal(err)
		}
		got := s.messages()
		if len(got) != 1 || !strings.Contains(got[0], want) {
			t.Errorf("%s answered %v, want something containing %q", cmd, got, want)
		}
	}
}

// Telegram appends @botname in groups; the command must still be
// recognised or the bot is useless in any group chat.
func TestGroupSuffixIsStripped(t *testing.T) {
	s := newStub(t)
	b := New("tok", []int64{111}, &fakeReporter{})

	s.queue(111, "/health@shahrag_bot")
	if _, err := b.pollOnce(); err != nil {
		t.Fatal(err)
	}
	got := s.messages()
	if len(got) != 1 || !strings.Contains(got[0], "HEALTH-BODY") {
		t.Fatalf("a group-suffixed command was not recognised: %v", got)
	}
}

func TestAnUnknownCommandFromAnAllowedChatGetsHelp(t *testing.T) {
	s := newStub(t)
	b := New("tok", []int64{111}, &fakeReporter{})

	s.queue(111, "/nonsense")
	if _, err := b.pollOnce(); err != nil {
		t.Fatal(err)
	}
	got := s.messages()
	if len(got) != 1 || !strings.Contains(got[0], "/health") {
		t.Fatalf("a typo did not get the command list: %v", got)
	}
}

// ── Message shape ────────────────────────────────────────────

// The three characters Telegram's HTML mode reserves must be escaped, or
// a path containing one makes the whole message fail to send.
func TestHTMLIsEscaped(t *testing.T) {
	if got := escapeHTML(`a & b <tag> "q"`); got != `a &amp; b &lt;tag&gt; "q"` {
		t.Fatalf("escapeHTML = %q", got)
	}
	// A quote must NOT be escaped: HTML mode does not reserve it, and
	// escaping it would put &quot; in front of the operator.
	if strings.Contains(escapeHTML(`"`), "&quot;") {
		t.Error("a quote was escaped unnecessarily")
	}
}

// A long report must be SPLIT, not truncated. Telegram rejects anything
// over 4096 characters outright, and truncating the tail of a status
// report is how an operator misses the one line that mattered.
func TestALongReportIsSplitNotTruncated(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "line %d with some padding to make it long enough\n", i)
	}
	full := sb.String()
	if len(full) < maxMessage {
		t.Fatal("the fixture is not long enough to exercise splitting")
	}

	parts := splitMessage(full, maxMessage)
	if len(parts) < 2 {
		t.Fatalf("a %d-character report produced %d part(s)", len(full), len(parts))
	}
	for i, p := range parts {
		if len(p) > maxMessage {
			t.Errorf("part %d is %d characters, over the limit", i, len(p))
		}
	}
	// Nothing may be lost. Compare the non-blank lines.
	joined := strings.Join(parts, "\n")
	for _, want := range []string{"line 0 ", "line 250 ", "line 499 "} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q was lost in the split", want)
		}
	}
}

// A single enormous line must not loop for ever.
func TestOneHugeLineTerminates(t *testing.T) {
	done := make(chan []string, 1)
	go func() { done <- splitMessage(strings.Repeat("x", 20000), 1000) }()
	select {
	case parts := <-done:
		if len(parts) < 20 {
			t.Fatalf("got %d parts", len(parts))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("splitMessage did not terminate on one long line")
	}
}

func TestShortMessagesAreNotSplit(t *testing.T) {
	if got := splitMessage("hello", maxMessage); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got %v", got)
	}
}

// ── Failure handling ─────────────────────────────────────────

// A wrong token must be reported to the panel, not only to the journal.
func TestABadTokenIsSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	defer func() { apiBase = old }()

	b := New("wrong", []int64{111}, &fakeReporter{})
	if _, err := b.pollOnce(); err == nil {
		t.Fatal("a 401 was not reported as an error")
	} else if !strings.Contains(err.Error(), "token") {
		t.Errorf("the error does not mention the token: %v", err)
	}
}

// An unreachable Telegram must not spin. The loop backs off, and this
// asserts the loop actually stops when told to.
func TestTheLoopStopsPromptly(t *testing.T) {
	old := apiBase
	apiBase = "http://127.0.0.1:1" // nothing listens here
	defer func() { apiBase = old }()

	b := New("tok", []int64{111}, &fakeReporter{})
	b.client.Timeout = 500 * time.Millisecond
	b.Start()
	time.Sleep(200 * time.Millisecond)

	done := make(chan struct{})
	go func() { b.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() did not return promptly")
	}
	if b.Running() {
		t.Error("the bot still reports itself running after Stop")
	}
}

func TestGarbageFromTelegramIsNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL
	defer func() { apiBase = old }()

	b := New("tok", []int64{111}, &fakeReporter{})
	if _, err := b.pollOnce(); err == nil {
		t.Fatal("unreadable output was accepted")
	}
	// And the bot is still usable afterwards.
	if !b.Configured() {
		t.Fatal("the bot broke itself on bad input")
	}
}

// ── Cost ─────────────────────────────────────────────────────

// An idle bot must cost one long poll and nothing else. The poll blocks
// server-side for pollTimeout seconds, so this is about one request per
// minute — the whole reason long polling was chosen over a busy loop.
func TestAnIdleBotMakesOneRequestPerPoll(t *testing.T) {
	s := newStub(t)
	b := New("tok", []int64{111}, &fakeReporter{})

	for i := 0; i < 5; i++ {
		if _, err := b.pollOnce(); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&s.getCalls); got != 5 {
		t.Fatalf("5 polls made %d requests", got)
	}
	if got := len(s.messages()); got != 0 {
		t.Fatalf("an idle bot sent %d messages", got)
	}
}

// The poll timeout must stay long. A short one turns the bot into a
// request-per-second busy loop against Telegram, which is both wasteful
// and a good way to get rate-limited.
func TestThePollIsLongEnoughToBeCheap(t *testing.T) {
	if pollTimeout < 30 {
		t.Fatalf("pollTimeout is %ds, which makes the bot poll %d times a minute",
			pollTimeout, 60/pollTimeout)
	}
}

// Notify reaches every allowed chat, and only those.
func TestNotifyReachesEveryAllowedChat(t *testing.T) {
	s := newStub(t)
	b := New("tok", []int64{111, 222}, &fakeReporter{})

	b.Notify("something happened")
	s.mu.Lock()
	to := append([]int64(nil), s.sentTo...)
	s.mu.Unlock()

	if len(to) != 2 {
		t.Fatalf("notified %d chats, want 2", len(to))
	}
	seen := map[int64]bool{}
	for _, id := range to {
		seen[id] = true
	}
	if !seen[111] || !seen[222] {
		t.Fatalf("wrong recipients: %v", to)
	}
}

func TestNotifyOnAnUnconfiguredBotDoesNothing(t *testing.T) {
	s := newStub(t)
	b := New("tok", nil, &fakeReporter{})
	b.Notify("hello")
	if got := len(s.messages()); got != 0 {
		t.Fatalf("an unconfigured bot sent %d messages", got)
	}
}
