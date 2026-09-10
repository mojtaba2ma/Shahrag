// Package telegram is the panel's Telegram bot.
//
// It answers the same questions the web panel answers, from the same data:
// internal/health's Report and the same topology derivation. That is not a
// convenience — it is the whole reason internal/health was written as a
// package rather than as a web handler. Two surfaces computing "is the
// server healthy?" independently would eventually disagree, and the
// operator would have no way to know which to believe.
//
// Design constraints, all of which come from where this runs:
//
//   - No third-party library. The Bot API is HTTP and JSON; a dependency
//     would be tens of thousands of lines to save a hundred, and this
//     project vendors everything it imports.
//   - Long polling, never a webhook. A webhook needs an inbound port and a
//     public certificate, and on a filtered network an unexpected inbound
//     endpoint is exactly the sort of thing that draws attention. Long
//     polling makes only OUTBOUND connections, which is what the rest of
//     the panel already does.
//   - Bounded cost. One idle long-poll connection and nothing else. The
//     poll blocks server-side for 50 seconds, so an idle bot costs roughly
//     one request per minute and no CPU at all.
//   - Refuses to talk to strangers. Only chat IDs on the allow-list get an
//     answer; anyone else is ignored in silence, not told "unauthorised" —
//     an error reply confirms the bot exists.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// apiBase is Telegram's endpoint. A variable so a test can point it at a
// local stub and never touch the network.
var apiBase = "https://api.telegram.org"

// pollTimeout is how long Telegram holds an empty long poll open.
//
// 50 seconds: long enough that an idle bot makes about one request a
// minute, short enough to stay well inside any proxy's idle timeout.
const pollTimeout = 50

// Reporter is what the bot needs from the rest of the panel.
//
// An interface rather than a concrete dependency so this package does not
// import web or banner — and so a test can drive it without standing up a
// server.
type Reporter interface {
	// HealthText renders the health summary.
	HealthText() string
	// MapText renders the routing summary.
	MapText() string
	// ServicesText lists services and their state.
	ServicesText() string
	// BansText lists currently banned addresses.
	BansText() string
	// StatsText renders traffic figures.
	StatsText() string
}

// Bot polls Telegram and answers commands.
type Bot struct {
	token string
	// allowed is the set of chat IDs that may talk to this bot. Empty
	// means nobody, never everybody: an unconfigured bot must be inert,
	// not open.
	allowed map[int64]bool
	rep     Reporter

	client *http.Client
	offset int64

	mu      sync.Mutex
	running bool
	stop    chan struct{}
	// lastErr is surfaced in the panel so a wrong token is visible there
	// rather than only in the journal.
	lastErr string
	// sent counts outgoing messages, for the cost test.
	sent int
}

// New builds a bot. It does not contact Telegram until Start.
func New(token string, allowed []int64, rep Reporter) *Bot {
	set := make(map[int64]bool, len(allowed))
	for _, id := range allowed {
		if id != 0 {
			set[id] = true
		}
	}
	return &Bot{
		token:   strings.TrimSpace(token),
		allowed: set,
		rep:     rep,
		// A generous timeout: the long poll itself blocks for
		// pollTimeout seconds, so anything shorter would abort every
		// poll and turn the bot into a busy loop.
		client: &http.Client{Timeout: (pollTimeout + 20) * time.Second},
		stop:   make(chan struct{}),
	}
}

// Configured reports whether the bot has enough to run.
func (b *Bot) Configured() bool {
	return b != nil && b.token != "" && len(b.allowed) > 0
}

// Running reports whether the poll loop is going.
func (b *Bot) Running() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

// LastError returns the most recent failure, for the panel to display.
func (b *Bot) LastError() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastErr
}

func (b *Bot) setErr(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	b.mu.Lock()
	b.lastErr = msg
	b.mu.Unlock()
}

// Start begins polling in the background.
func (b *Bot) Start() {
	if !b.Configured() {
		return
	}
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return
	}
	b.running = true
	b.stop = make(chan struct{})
	b.mu.Unlock()
	go b.loop()
}

// Stop ends polling.
func (b *Bot) Stop() {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return
	}
	b.running = false
	close(b.stop)
	b.mu.Unlock()
}

func (b *Bot) loop() {
	// Back-off after a failure, so a wrong token or a blocked network
	// does not turn into a tight retry loop that hammers both the API and
	// the journal. Doubles to a minute and stays there.
	backoff := 2 * time.Second
	for {
		select {
		case <-b.stop:
			return
		default:
		}

		n, err := b.pollOnce()
		if err != nil {
			b.setErr("%v", err)
			select {
			case <-b.stop:
				return
			case <-time.After(backoff):
			}
			if backoff < time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 2 * time.Second
		b.setErr("")
		_ = n
	}
}

// update is the subset of Telegram's Update we use.
type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	// CallbackQuery arrives when an inline button is tapped. Without
	// handling it the buttons appear and do nothing, and Telegram shows a
	// spinner on the button for ever.
	CallbackQuery *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// button is one inline keyboard button.
type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	// WebApp opens the panel's own mini-app, when one is configured.
	WebApp *webAppInfo `json:"web_app,omitempty"`
}

type webAppInfo struct {
	URL string `json:"url"`
}

type inlineKeyboard struct {
	Inline [][]button `json:"inline_keyboard"`
}

// mainKeyboard is the button grid shown under every reply.
//
// Two per row: on a phone a three-column grid truncates the labels, and a
// single column pushes the message off the screen. Ordered by how often
// they are actually pressed during an incident.
func mainKeyboard() *inlineKeyboard {
	return &inlineKeyboard{Inline: [][]button{
		{{Text: "🩺 Health", Data: "health"}, {Text: "📊 Stats", Data: "stats"}},
		{{Text: "🗺 Map", Data: "map"}, {Text: "🧩 Services", Data: "services"}},
		{{Text: "🚫 Bans", Data: "bans"}, {Text: "🔄 Refresh", Data: "refresh"}},
	}}
}

// pollOnce performs one long poll and answers whatever arrived.
func (b *Bot) pollOnce() (int, error) {
	u := fmt.Sprintf("%s/bot%s/getUpdates?timeout=%d&offset=%d",
		apiBase, b.token, pollTimeout, b.offset)

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cannot reach Telegram: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == 401 {
		return 0, fmt.Errorf("Telegram rejected the token")
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("Telegram returned %d", resp.StatusCode)
	}

	var out struct {
		OK     bool     `json:"ok"`
		Result []update `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("unreadable answer from Telegram: %w", err)
	}
	if !out.OK {
		return 0, fmt.Errorf("Telegram reported a failure")
	}

	for _, up := range out.Result {
		// Advance the offset even for messages we ignore, or an
		// unauthorised sender would have their message redelivered for
		// ever and the bot would never make progress.
		if up.UpdateID >= b.offset {
			b.offset = up.UpdateID + 1
		}
		// A tapped button.
		if cq := up.CallbackQuery; cq != nil {
			chat := int64(0)
			if cq.Message != nil {
				chat = cq.Message.Chat.ID
			}
			// Answer FIRST, always: Telegram shows a spinner on the
			// button until the callback is acknowledged, and leaving it
			// spinning looks like a broken bot even when the reply
			// arrives.
			b.answerCallback(cq.ID)
			if chat != 0 && b.allowed[chat] {
				b.handle(chat, "/"+cq.Data)
			}
			continue
		}

		if up.Message == nil {
			continue
		}
		chat := up.Message.Chat.ID
		if !b.allowed[chat] {
			// Silence, not a refusal. An error reply confirms the bot
			// exists and that the chat id is wrong, which is more than a
			// stranger should learn.
			log.Printf("[shahrag] telegram: ignoring a message from chat %d", chat)
			continue
		}
		b.handle(chat, up.Message.Text)
	}
	return len(out.Result), nil
}

// Commands the bot understands.
const helpText = `Shahrag

/health   — is the server all right
/map      — what listens where, and where it goes
/services — services and their state
/bans     — addresses blocked right now
/stats    — traffic
/help     — this list`

func (b *Bot) handle(chat int64, text string) {
	cmd := strings.ToLower(strings.TrimSpace(text))
	// Telegram appends @botname when several bots share a group.
	if i := strings.Index(cmd, "@"); i > 0 {
		cmd = cmd[:i]
	}
	if i := strings.IndexAny(cmd, " \t"); i > 0 {
		cmd = cmd[:i]
	}

	var reply string
	switch cmd {
	case "/health", "/status", "/refresh":
		reply = b.rep.HealthText()
	case "/map", "/routes":
		reply = b.rep.MapText()
	case "/services":
		reply = b.rep.ServicesText()
	case "/bans":
		reply = b.rep.BansText()
	case "/stats":
		reply = b.rep.StatsText()
	case "/start", "/help":
		reply = helpText
	default:
		// An unknown command from an ALLOWED chat gets the help, because
		// that chat is already trusted and a typo deserves a hint.
		reply = helpText
	}
	b.Send(chat, reply)
}

// answerCallback acknowledges a button press.
//
// Telegram spins the button until this arrives, so it is sent before the
// reply is even computed — a health report takes a moment and a spinning
// button in the meantime reads as a failure.
func (b *Bot) answerCallback(id string) {
	body, _ := json.Marshal(map[string]interface{}{"callback_query_id": id})
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/bot%s/answerCallbackQuery", apiBase, b.token),
		bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := b.client.Do(req.WithContext(ctx))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<15))
}

// maxMessage is Telegram's own limit, minus room for the code fence.
const maxMessage = 4000

// Send delivers a message, splitting anything too long.
//
// Telegram rejects a message over 4096 characters outright, and a routing
// map on a busy server can exceed that — so it is split at line boundaries
// rather than truncated. Truncating the tail of a status report is how an
// operator misses the one line that mattered.
func (b *Bot) Send(chat int64, text string) {
	for _, part := range splitMessage(text, maxMessage) {
		b.sendOne(chat, part)
	}
}

func (b *Bot) sendOne(chat int64, text string) {
	b.sendWith(chat, text, mainKeyboard())
}

// sendWith delivers a message with a specific keyboard.
//
// The formatting changed in r49 after the first version wrapped EVERY reply
// in one <pre> block. That made the whole message a single copy target —
// tapping it copied the entire report — and it rendered as a wall of
// monospace on a phone. Now ordinary text carries the prose, and only the
// values that are worth copying (an address, a port, a path) are wrapped in
// <code>, which Telegram makes individually tappable.
func (b *Bot) sendWith(chat int64, text string, kb interface{}) {
	payload := map[string]interface{}{
		"chat_id":    chat,
		"text":       text,
		"parse_mode": "HTML",
		// These are status reports, not conversation.
		"disable_notification":     true,
		"disable_web_page_preview": true,
	}
	if kb != nil {
		payload["reply_markup"] = kb
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/bot%s/sendMessage", apiBase, b.token),
		bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := b.client.Do(req.WithContext(ctx))
	if err != nil {
		b.setErr("cannot send: %v", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		b.setErr("Telegram refused the message (%d)", resp.StatusCode)
		return
	}
	b.mu.Lock()
	b.sent++
	b.mu.Unlock()
}

// SentCount is the number of messages delivered. Test-only.
func (b *Bot) SentCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sent
}

// escapeHTML escapes the three characters Telegram's HTML mode reserves.
//
// Only three, and that is the whole reason HTML mode is used instead of
// MarkdownV2, which reserves eighteen and rejects the message if any of
// them is unescaped — including characters that appear in ordinary paths
// and version strings.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// splitMessage breaks text into parts no longer than max, at line
// boundaries wherever possible.
func splitMessage(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > max {
		cut := strings.LastIndexByte(s[:max], '\n')
		if cut <= 0 {
			// One enormous line: split it mid-way rather than looping
			// for ever.
			cut = max
		}
		out = append(out, strings.TrimRight(s[:cut], "\n"))
		s = strings.TrimLeft(s[cut:], "\n")
	}
	if strings.TrimSpace(s) != "" {
		out = append(out, s)
	}
	return out
}

// Notify pushes a message to every allowed chat.
//
// Used for alerts — a ban, nginx going down — rather than for answers.
func (b *Bot) Notify(text string) {
	if !b.Configured() {
		return
	}
	for chat := range b.allowed {
		b.Send(chat, text)
	}
}
