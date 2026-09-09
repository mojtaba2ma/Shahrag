package web

// The Telegram bot's API.
//
// The bot itself lives in internal/telegram and reads the same
// internal/health Report the panel does, so the two can never disagree.
// This file is only the settings surface and the lifecycle.

import (
	"net/http"
	"strings"
	"time"

	"shahrag/internal/config"
	"shahrag/internal/telegram"
)

type telegramResp struct {
	Enabled bool    `json:"enabled"`
	ChatIDs []int64 `json:"chat_ids"`
	// TokenSet, never the token itself. Echoing a bot token back to a
	// browser puts it in history, in logs and on screen for no reason —
	// the form only needs to know whether one exists.
	TokenSet   bool   `json:"token_set"`
	AlertBans  bool   `json:"alert_bans"`
	AlertNginx bool   `json:"alert_nginx"`
	Running    bool   `json:"running"`
	LastError  string `json:"last_error,omitempty"`
}

func (s *Server) handleGetTelegram(w http.ResponseWriter, r *http.Request) {
	c, _ := s.cfg.Read()
	t := c.Telegram
	resp := telegramResp{
		Enabled:    t.Enabled,
		ChatIDs:    t.ChatIDs,
		TokenSet:   strings.TrimSpace(t.Token) != "",
		AlertBans:  t.AlertBans,
		AlertNginx: t.AlertNginx,
	}
	if s.bot != nil {
		resp.Running = s.bot.Running()
		resp.LastError = s.bot.LastError()
	}
	writeJSON(w, 200, resp)
}

type telegramReq struct {
	Enabled *bool `json:"enabled"`
	// Token is only applied when NON-EMPTY, so saving the form without
	// retyping the token does not wipe it. That is why the GET never
	// returns it: the form legitimately cannot round-trip it.
	Token      *string  `json:"token"`
	ChatIDs    *[]int64 `json:"chat_ids"`
	AlertBans  *bool    `json:"alert_bans"`
	AlertNginx *bool    `json:"alert_nginx"`
	// Test asks the bot to send a message right now, so the operator
	// finds out the token is wrong here rather than during an incident.
	Test bool `json:"test"`
}

func (s *Server) handleSetTelegram(w http.ResponseWriter, r *http.Request) {
	var body telegramReq
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, "Invalid request")
		return
	}

	_, err := s.cfg.Mutate(func(c *config.Config) error {
		t := c.Telegram
		if body.Enabled != nil {
			t.Enabled = *body.Enabled
		}
		if body.Token != nil && strings.TrimSpace(*body.Token) != "" {
			t.Token = strings.TrimSpace(*body.Token)
		}
		if body.ChatIDs != nil {
			seen := map[int64]bool{}
			out := make([]int64, 0, len(*body.ChatIDs))
			for _, id := range *body.ChatIDs {
				if id == 0 || seen[id] {
					continue
				}
				seen[id] = true
				out = append(out, id)
			}
			t.ChatIDs = out
		}
		if body.AlertBans != nil {
			t.AlertBans = *body.AlertBans
		}
		if body.AlertNginx != nil {
			t.AlertNginx = *body.AlertNginx
		}
		if t.Enabled && !t.TelegramConfigured() {
			return &botError{"a bot token and at least one chat ID are both required"}
		}
		c.Telegram = t
		return nil
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	s.restartBot()

	out := map[string]interface{}{"ok": true}
	if body.Test {
		if s.bot == nil || !s.bot.Configured() {
			out["test"] = "not configured"
		} else {
			s.bot.Notify("Shahrag: this is a test message. If you can read " +
				"this, the bot is working.")
			out["test"] = "sent"
		}
	}
	if s.bot != nil {
		out["running"] = s.bot.Running()
		if e := s.bot.LastError(); e != "" {
			out["last_error"] = e
		}
	}
	writeJSON(w, 200, out)
}

type botError struct{ msg string }

func (e *botError) Error() string { return e.msg }

// restartBot rebuilds the bot from the current config.
//
// Rebuilt rather than reconfigured because the token and the allow-list are
// baked in at construction — which is what makes it impossible for a
// half-applied change to leave a bot answering strangers.
func (s *Server) restartBot() {
	if s.bot != nil {
		s.bot.Stop()
		s.bot = nil
	}
	c, err := s.cfg.Read()
	if err != nil || c == nil || !c.Telegram.Enabled || !c.Telegram.TelegramConfigured() {
		return
	}
	s.bot = telegram.New(c.Telegram.Token, c.Telegram.ChatIDs, s.reporter())
	s.bot.Start()
}

// reporter builds the bot's view of the panel.
func (s *Server) reporter() *telegram.PanelReporter {
	return &telegram.PanelReporter{
		Cfg:    s.cfg,
		Health: s.healthC,
		Bans: func() []telegram.BanLine {
			if s.bans == nil {
				return nil
			}
			list := s.bans.ActiveBans()
			out := make([]telegram.BanLine, 0, len(list))
			for _, b := range list {
				out = append(out, telegram.BanLine{
					IP: b.IP, Reason: b.Reason,
					Remaining: int(b.Remaining(nowFunc()).Minutes()),
					Permanent: b.Permanent,
				})
			}
			return out
		},
	}
}

// nowFunc is time.Now, behind a variable so a test can freeze it.
var nowFunc = time.Now
