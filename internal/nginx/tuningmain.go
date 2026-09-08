package nginx

// Applying the main-context tuning directives.
//
// worker_processes, worker_rlimit_nofile and multi_accept cannot live in the
// generated file: that file is included from inside http{}, and nginx
// refuses the whole config if a main-context directive appears there. They
// have to be edited into nginx.conf itself.
//
// Editing a file the operator also edits by hand is the risky part, so it is
// done the same careful way the boot-guard already does it:
//
//   - the change is made between two clearly-labelled marker lines, so it
//     can be found and replaced exactly rather than by pattern-matching
//     whatever is there,
//   - editNginxConf snapshots the file, runs `nginx -t`, and RESTORES the
//     snapshot if the test fails, so a bad value can never leave the server
//     with a config it will not start from,
//   - removing the tuning removes the block and nothing else, so switching
//     the feature off returns the file to exactly what it was.
//
// Anything already in nginx.conf that our block also sets is commented out
// rather than deleted, with a note saying why — silently deleting a line
// somebody wrote by hand is not acceptable, and a duplicate directive makes
// nginx fail with "directive is duplicate".

import (
	"fmt"
	"regexp"
	"strings"

	"shahrag/internal/config"
)

const (
	tuneBegin = "# >>> shahrag tuning >>>"
	tuneEnd   = "# <<< shahrag tuning <<<"
)

// reTuneBlock matches our whole managed block, including the markers.
var reTuneBlock = regexp.MustCompile(
	`(?s)\n?` + regexp.QuoteMeta(tuneBegin) + `.*?` + regexp.QuoteMeta(tuneEnd) + `\n?`)

// Directives we manage at main level. Any pre-existing copy is commented
// out, because two of the same directive is a config nginx refuses.
var mainManaged = []string{"worker_processes", "worker_rlimit_nofile"}

// ApplyMainTuning writes (or removes) the managed block in nginx.conf.
//
// Returns whether the file changed, so a caller can avoid a pointless
// reload. Never returns with nginx.conf in a state `nginx -t` rejects:
// editNginxConf rolls back on failure.
func ApplyMainTuning(c *config.Config) (bool, error) {
	body := TuningMainBlock(c)
	events := TuningEventsBlock(c)

	changed := false
	err := editNginxConf(func(s string) string {
		before := s

		// Always start by removing our previous block, so this function
		// is idempotent and so switching the feature off cleans up.
		s = reTuneBlock.ReplaceAllString(s, "\n")
		s = uncommentManaged(s)

		if strings.TrimSpace(body) == "" && strings.TrimSpace(events) == "" {
			changed = s != before
			return s
		}

		// Comment out any hand-written copy of a directive we are about
		// to set, or nginx fails with "directive is duplicate".
		for _, d := range mainManaged {
			if !strings.Contains(body, d+" ") {
				continue
			}
			s = commentOutDirective(s, d)
		}

		var blk strings.Builder
		blk.WriteString(tuneBegin + "\n")
		blk.WriteString("#     Managed by the Shahrag panel. Edit it there, not here:\n")
		blk.WriteString("#     anything written between these markers is replaced on\n")
		blk.WriteString("#     the next save. Remove the tuning in the panel to take\n")
		blk.WriteString("#     this block out entirely.\n")
		blk.WriteString(body)
		blk.WriteString(tuneEnd + "\n")

		s = insertAtTop(s, blk.String())

		if strings.TrimSpace(events) != "" {
			s = putInEvents(s, events)
		}
		changed = s != before
		return s
	})
	return changed, err
}

// insertAtTop places the block above the events{} section, which is where
// main-context directives belong and where an operator expects to find them.
func insertAtTop(s, block string) string {
	if i := strings.Index(s, "events"); i >= 0 {
		// Only if `events` really starts a block at the beginning of a
		// line, not inside a comment or a path.
		re := regexp.MustCompile(`(?m)^\s*events\s*\{`)
		if loc := re.FindStringIndex(s); loc != nil {
			return s[:loc[0]] + block + "\n" + s[loc[0]:]
		}
		_ = i
	}
	return block + "\n" + s
}

// putInEvents adds our lines inside the events{} block, replacing any copy
// we previously wrote there.
func putInEvents(s, lines string) string {
	marker := "    # shahrag:events\n"
	// Remove a previous insertion first.
	reOld := regexp.MustCompile(`(?m)^ *# shahrag:events\n(?: *[a-z_]+ [^;]*;\n)*`)
	s = reOld.ReplaceAllString(s, "")

	re := regexp.MustCompile(`(?m)^(\s*events\s*\{)`)
	if re.MatchString(s) {
		return re.ReplaceAllString(s, "${1}\n"+marker+lines)
	}
	return s
}

// commentOutDirective disables a hand-written directive, explaining why.
//
// Commented rather than deleted: the operator wrote it for a reason, and if
// they later remove the panel's tuning they should be able to see what was
// there and put it back.
func commentOutDirective(s, name string) string {
	re := regexp.MustCompile(`(?m)^(\s*)(` + regexp.QuoteMeta(name) + `\s+[^;]*;)`)
	return re.ReplaceAllString(s,
		"${1}# [shahrag] superseded by the panel's tuning block: ${2}")
}

// uncommentManaged restores anything we commented out, used when the tuning
// is switched off so the file returns to what it was.
func uncommentManaged(s string) string {
	re := regexp.MustCompile(
		`(?m)^(\s*)# \[shahrag\] superseded by the panel's tuning block: (.*)$`)
	return re.ReplaceAllString(s, "${1}${2}")
}

// MainTuningInstalled reports whether our block is currently in nginx.conf.
func MainTuningInstalled() bool {
	txt, err := readConf()
	if err != nil {
		return false
	}
	return strings.Contains(txt, tuneBegin)
}

// MainTuningSummary describes the current state for `shahrag doctor`.
func MainTuningSummary(c *config.Config) string {
	if !c.NginxSettings.Tuning.Enabled {
		return "off"
	}
	if !MainTuningInstalled() {
		return "enabled in the panel but NOT present in nginx.conf (save the settings to apply)"
	}
	t := c.NginxSettings.Tuning
	parts := []string{}
	if t.WorkerProcesses != "" {
		parts = append(parts, "worker_processes "+t.WorkerProcesses)
	}
	if t.WorkerRLimitNofile > 0 {
		parts = append(parts, fmt.Sprintf("rlimit_nofile %d", t.WorkerRLimitNofile))
	}
	if len(parts) == 0 {
		return "installed"
	}
	return "installed (" + strings.Join(parts, ", ") + ")"
}
