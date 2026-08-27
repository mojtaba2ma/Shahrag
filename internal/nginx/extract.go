package nginx

// Extracting (and splicing back) the part of a generated config that belongs
// to ONE service.
//
// The panel can show a service's own nginx output next to its JSON, which is
// far easier to reason about than scrolling a 400-line gateway.conf looking
// for the three blocks that matter. Editing it is supported too, but only in
// a way that cannot corrupt the file: the exact byte ranges are located,
// replaced, and the result is validated by nginx itself before it is kept.
//
// The generator marks every service's location block with a comment:
//
//	    # xray → http://127.0.0.1:4628
//	    location /take { ... }
//
// and every SNI rule's map entry with:
//
//	    # unblock-epic
//	    ~*^(.+\.)?epicgames\.com$    $ssl_preread_server_name:443;
//
// Those markers are what this file keys on, so extraction stays in step with
// the generator by construction.

import (
	"fmt"
	"regexp"
	"strings"
)

// BlockSeparator introduces each extracted block. A service bound to several
// domains produces one block per server{}, and the operator must be able to
// tell them apart — and put them back in the same order.
const BlockSeparator = "# ── block %d of %d · server_name: %s ──"

var reBlockSeparator = regexp.MustCompile(`(?m)^# ── block \d+ of \d+ · server_name: .*──\s*$`)

// span is a half-open byte range within a file.
type span struct {
	start, end int
	serverName string
}

// serviceSpans locates every location block belonging to `name`.
//
// A block starts at its "    # <name> → " marker and ends just before the
// next marker, the fake-site comment, or the closing brace of the server
// block — whichever comes first.
func serviceSpans(conf, name string) []span {
	marker := "    # " + name + " → "
	var out []span
	lines := strings.Split(conf, "\n")

	// Byte offset of the start of each line, so spans can be byte ranges.
	offsets := make([]int, len(lines)+1)
	pos := 0
	for i, l := range lines {
		offsets[i] = pos
		pos += len(l) + 1 // +1 for the newline
	}
	offsets[len(lines)] = pos

	currentServer := ""
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "server_name ") {
			currentServer = strings.TrimSuffix(
				strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "server_name ")), ";")
		}
		if !strings.HasPrefix(l, marker) {
			continue
		}
		// Walk forward to the end of this service's blocks.
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			t := lines[j]
			// Another service's marker, the fake site, or the end of the
			// server block terminates this one.
			if strings.HasPrefix(t, "    # ") && strings.Contains(t, " → ") {
				end = j
				break
			}
			if strings.HasPrefix(t, "    # Fake site") {
				end = j
				break
			}
			if t == "}" {
				end = j
				break
			}
		}
		// Trim the blank lines the generator puts between blocks so the
		// extracted text is exactly the block.
		for end > i+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		out = append(out, span{start: offsets[i], end: offsets[end], serverName: currentServer})
	}
	return out
}

// ExtractServiceNginx returns the generated nginx text for one service, with
// a separator before each block so several blocks stay distinguishable.
// The second return value is the number of blocks found.
func ExtractServiceNginx(conf, name string) (string, int) {
	spans := serviceSpans(conf, name)
	if len(spans) == 0 {
		return "", 0
	}
	var b strings.Builder
	for i, sp := range spans {
		fmt.Fprintf(&b, BlockSeparator+"\n", i+1, len(spans), sp.serverName)
		b.WriteString(strings.TrimRight(conf[sp.start:sp.end], "\n"))
		b.WriteString("\n")
	}
	return b.String(), len(spans)
}

// ReplaceServiceNginx splices edited blocks back into the configuration.
//
// The edited text must still contain one separator per block, in the same
// order, so each piece can be put back exactly where it came from. Anything
// else is refused rather than guessed at — a misplaced block would move a
// location into the wrong server{}.
func ReplaceServiceNginx(conf, name, edited string) (string, error) {
	spans := serviceSpans(conf, name)
	if len(spans) == 0 {
		return "", fmt.Errorf("no generated block found for service %q", name)
	}

	parts := splitOnSeparators(edited)
	if len(parts) != len(spans) {
		return "", fmt.Errorf(
			"expected %d block(s) separated by the '# ── block N of M ──' markers, found %d — "+
				"keep every marker line so each block can be put back in its own server block",
			len(spans), len(parts))
	}

	// Replace from the END so earlier offsets stay valid.
	out := conf
	for i := len(spans) - 1; i >= 0; i-- {
		body := strings.TrimRight(parts[i], "\n")
		if strings.TrimSpace(body) == "" {
			return "", fmt.Errorf("block %d is empty", i+1)
		}
		out = out[:spans[i].start] + body + "\n" + out[spans[i].end:]
	}
	return out, nil
}

// splitOnSeparators returns the text after each separator line.
func splitOnSeparators(s string) []string {
	locs := reBlockSeparator.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return nil
	}
	var parts []string
	for i, loc := range locs {
		start := loc[1]
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		parts = append(parts, strings.Trim(s[start:end], "\n"))
	}
	return parts
}

// ── SNI (stream) rules ──────────────────────────────────────

// sniSpan locates the map entry for one SNI rule: its "# <name>" comment and
// the mapping line that follows it.
func sniSpan(streamConf, name string) (span, bool) {
	lines := strings.Split(streamConf, "\n")
	offsets := make([]int, len(lines)+1)
	pos := 0
	for i, l := range lines {
		offsets[i] = pos
		pos += len(l) + 1
	}
	offsets[len(lines)] = pos

	marker := "    # " + name
	for i, l := range lines {
		if strings.TrimRight(l, " \t") != marker {
			continue
		}
		end := i + 1
		// The mapping line is the next non-empty line inside the map.
		for end < len(lines) && strings.TrimSpace(lines[end]) == "" {
			end++
		}
		if end < len(lines) {
			end++
		}
		return span{start: offsets[i], end: offsets[end]}, true
	}
	return span{}, false
}

// ExtractSNINginx returns the map entry generated for one SNI rule.
func ExtractSNINginx(streamConf, name string) (string, bool) {
	sp, ok := sniSpan(streamConf, name)
	if !ok {
		return "", false
	}
	return strings.TrimRight(streamConf[sp.start:sp.end], "\n") + "\n", true
}

// ReplaceSNINginx splices an edited map entry back into the stream config.
func ReplaceSNINginx(streamConf, name, edited string) (string, error) {
	sp, ok := sniSpan(streamConf, name)
	if !ok {
		return "", fmt.Errorf("no generated map entry found for SNI rule %q", name)
	}
	body := strings.TrimRight(edited, "\n")
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("the map entry is empty")
	}
	return streamConf[:sp.start] + body + "\n" + streamConf[sp.end:], nil
}
