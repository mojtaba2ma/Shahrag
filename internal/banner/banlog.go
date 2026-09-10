package banner

// A durable record of who was blocked, when, and why.
//
// The engine already keeps the ACTIVE bans in memory and on disk, and the
// panel lists them. That list answers "who is blocked right now" — but the
// question an operator actually asks is almost always the other one: "a user
// says they could not reach the site last night, was that us?" A four-hour
// ban that expired at 3am has vanished from the active list entirely, so the
// live table can never answer it.
//
// So every ban and every release is appended to a plain text file that the
// Logs page can read alongside nginx's own logs. Text, one event per line,
// same shape as the honeypot log — no database, no rotation logic of our
// own, and `grep` works.
//
// Cost, which is the reason this is written the way it is:
//
//   - The file is opened, appended to, and closed per event. Keeping a
//     handle open would be marginally faster and would break logrotate: a
//     held descriptor keeps a deleted file alive, and the panel would go on
//     writing to a file nobody can see until it is restarted. Bans are rare
//     (a busy server gets a handful an hour), so an open+write+close per
//     event is free in any realistic case.
//   - A ban SCAN can produce many bans at once. Those are written in one
//     call with one open, not one open per address — see LogBans.
//   - Writing is best-effort. A failure to log must never stop a ban from
//     being applied: the protection matters more than the paperwork.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BanLogPath is where ban events are recorded. Alongside nginx's own logs
// so one logrotate rule covers everything and the Logs page reads them the
// same way.
var BanLogPath = envOr("SHAHRAG_BAN_LOG", "/var/log/nginx/shahrag-bans.log")

// maxBanLogBytes caps the file.
//
// Without a cap a server under a long scanning campaign could write for
// months onto a 25 GB disk. logrotate would normally handle this, but the
// panel cannot assume its rule was installed, and a control panel that fills
// the disk it is meant to be monitoring is indefensible. At the cap the file
// is rotated ONCE to a .1 sibling — the previous .1 is dropped. Two files,
// bounded at 4 MB total, roughly 30,000 events of history.
const maxBanLogBytes = 2 << 20 // 2 MiB

var banLogMu sync.Mutex

// BanEvent is one line of the log.
type BanEvent struct {
	When   time.Time
	Action string // "ban" | "unban" | "expire"
	IP     string
	Reason string
	// Hits is how many offences triggered it; zero for a manual ban.
	Hits int
	// Until is when the ban lapses. Zero for a release.
	Until time.Time
	// Permanent bans print "forever" instead of a date.
	Permanent bool
	// Level is the escalation rung this ban was issued at, 1-based.
	// Recorded because "banned for 8 hours" is not explicable on its own
	// and "third offence, so 8 hours" is — and because the ladder is the
	// one part of the decision that cannot be reconstructed later from
	// the config, since the config may have changed since.
	Level int
}

// Line renders one event.
//
// The format matches the honeypot log so the Logs page parses both with the
// same code: an ISO timestamp, then the address, then a quoted description.
//
//	2026-09-08T14:03:11+03:30 203.0.113.5 "ban honeypot" hits="3" until="2026-09-08T18:03:11+03:30"
func (e BanEvent) Line() string {
	var b strings.Builder
	b.WriteString(e.When.Format(time.RFC3339))
	b.WriteByte(' ')
	if e.IP == "" {
		b.WriteByte('-')
	} else {
		b.WriteString(e.IP)
	}
	fmt.Fprintf(&b, " %q", e.Action+" "+e.Reason)
	fmt.Fprintf(&b, " hits=%q", fmt.Sprint(e.Hits))
	if e.Level > 0 {
		fmt.Fprintf(&b, " level=%q", fmt.Sprint(e.Level))
	}
	switch {
	case e.Permanent:
		b.WriteString(` until="forever"`)
	case !e.Until.IsZero():
		fmt.Fprintf(&b, " until=%q", e.Until.Format(time.RFC3339))
	default:
		b.WriteString(` until="-"`)
	}
	return b.String()
}

// LogBanEvents appends events to the ban log.
//
// Takes a slice rather than one event because a scan can ban several
// addresses at once and this way that costs one open, one write and one
// close regardless of how many there are.
func LogBanEvents(events []BanEvent) {
	if len(events) == 0 {
		return
	}
	var buf strings.Builder
	for _, e := range events {
		buf.WriteString(e.Line())
		buf.WriteByte('\n')
	}

	banLogMu.Lock()
	defer banLogMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(BanLogPath), 0o755); err != nil {
		return
	}
	rotateBanLogIfBig()

	f, err := os.OpenFile(BanLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(buf.String())
}

// rotateBanLogIfBig keeps the log bounded. Caller holds banLogMu.
func rotateBanLogIfBig() {
	fi, err := os.Stat(BanLogPath)
	if err != nil || fi.Size() < maxBanLogBytes {
		return
	}
	// Rename rather than truncate: a rename is atomic, so a concurrent
	// reader either sees the whole old file or the whole new one, never a
	// half-emptied file.
	_ = os.Remove(BanLogPath + ".1")
	_ = os.Rename(BanLogPath, BanLogPath+".1")
}

// ReadBanLog returns the most recent events, newest first.
//
// Reads the current file and, if it needs more lines, the rotated sibling.
// Bounded by `limit` so the panel cannot be made to load 2 MB into a JSON
// response.
func ReadBanLog(limit int) []string {
	if limit <= 0 {
		limit = 200
	}
	banLogMu.Lock()
	defer banLogMu.Unlock()

	lines := tailLines(BanLogPath, limit)
	if len(lines) < limit {
		older := tailLines(BanLogPath+".1", limit-len(lines))
		lines = append(older, lines...)
	}
	// Newest first: an operator reads the most recent event.
	out := make([]string, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		out = append(out, lines[i])
	}
	return out
}

// tailLines returns the last n lines of a file, oldest first.
//
// Reads from the END rather than scanning the whole file: with a 2 MB cap
// the difference is small, but the same helper is used on the rotated file
// and this keeps the cost proportional to what is asked for, not to what is
// stored.
func tailLines(path string, n int) []string {
	if n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return nil
	}

	// 200 bytes per line is generous for this format; read a chunk that
	// size and grow if it did not contain enough newlines.
	want := int64(n)*200 + 1024
	if want > fi.Size() {
		want = fi.Size()
	}
	buf := make([]byte, want)
	if _, err := f.ReadAt(buf, fi.Size()-want); err != nil {
		return nil
	}
	text := string(buf)
	// A partial first line is possible when we did not start at byte 0.
	if want < fi.Size() {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		return nil
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
