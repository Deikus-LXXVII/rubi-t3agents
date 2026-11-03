package main

// Briefs and reports. t3_start and t3_role_start take an optional typed brief; Rubi renders it the same way every
// time under a fixed heading, adds fixed standing orders and asks the agent to end with a report in a fixed shape.
// A thread's final text that contains that report reaches the Bot with the report parsed into fields. The rendered
// text goes through the same start-text check as any first message and is shown whole in the approval.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxBriefGoal    = 600
	maxBriefScope   = 600
	maxBriefItem    = 300
	maxBriefItems   = 8
	maxBriefTimebox = 40
	maxBriefReport  = 300
	maxStartTotal   = 4000 // the whole first message: the task and the rendered brief

	briefHeading = "## BRIEF"
	ordersHeader = "## STANDING ORDERS"
)

// briefIn is the optional typed brief of a start.
type briefIn struct {
	Goal       string   `json:"goal" jsonschema:"what to achieve, in a sentence or two (required)"`
	Scope      string   `json:"scope,omitempty" jsonschema:"where to work: the files, directories or area the work stays in"`
	Acceptance []string `json:"acceptance,omitempty" jsonschema:"the conditions that make it done, one per item (at most 8)"`
	Verify     []string `json:"verify,omitempty" jsonschema:"the commands or checks that show the conditions hold, one per item (at most 8)"`
	Timebox    string   `json:"timebox,omitempty" jsonschema:"how long it may take, e.g. 30 minutes"`
	Forbidden  []string `json:"forbidden,omitempty" jsonschema:"what must not be done, one per item (at most 8)"`
	Report     string   `json:"report,omitempty" jsonschema:"what the final report should cover besides the fixed shape"`
}

// briefLine checks a single-line field: plain text without control or invisible characters.
func briefLine(name, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\r\n") {
		return "", fmt.Errorf("brief %s must be on one line", name)
	}
	return briefText(name, s, max)
}

// briefText checks a field that may hold several lines (the goal and the scope).
func briefText(name, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("brief %s is not valid text", name)
	}
	if n := utf8.RuneCountInString(s); n > max {
		return "", fmt.Errorf("brief %s has %d characters, at most %d", name, n, max)
	}
	for _, r := range s {
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || r == 0x2028 || r == 0x2029 || r >= 0xE0000 && r <= 0xE007F) {
			return "", fmt.Errorf("brief %s contains an invisible or control character (U+%04X); send plain text", name, r)
		}
	}
	if strings.Contains(s, "\n\n\n") {
		return "", fmt.Errorf("brief %s has more than one blank line in a row", name)
	}
	return s, nil
}

func briefList(name string, xs []string) ([]string, error) {
	if len(xs) > maxBriefItems {
		return nil, fmt.Errorf("brief %s has %d items, at most %d", name, len(xs), maxBriefItems)
	}
	var out []string
	for _, x := range xs {
		if strings.TrimSpace(x) == "" {
			continue
		}
		v, err := briefLine(name, x, maxBriefItem)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// clean validates every field and returns the trimmed brief; the goal is required.
func (b briefIn) clean() (briefIn, error) {
	var out briefIn
	var err error
	if out.Goal, err = briefText("goal", b.Goal, maxBriefGoal); err != nil {
		return out, err
	}
	if out.Goal == "" {
		return out, errors.New("a brief needs a goal")
	}
	if out.Scope, err = briefText("scope", b.Scope, maxBriefScope); err != nil {
		return out, err
	}
	if out.Timebox, err = briefLine("timebox", b.Timebox, maxBriefTimebox); err != nil {
		return out, err
	}
	if out.Report, err = briefLine("report", b.Report, maxBriefReport); err != nil {
		return out, err
	}
	if out.Acceptance, err = briefList("acceptance", b.Acceptance); err != nil {
		return out, err
	}
	if out.Verify, err = briefList("verify", b.Verify); err != nil {
		return out, err
	}
	if out.Forbidden, err = briefList("forbidden", b.Forbidden); err != nil {
		return out, err
	}
	return out, nil
}

// reportShape is the report the agent is asked to end with.
const reportShape = "STATUS: PASS|ISSUES|BLOCKED\nCOMMIT: <sha or none>\nEVIDENCE: <one fact per line, starting each line with EVIDENCE:>"

// standingOrders are the same for every brief; briefID names the brief they belong to.
func standingOrders(briefID string) string {
	return ordersHeader + " (brief " + briefID + ")\n" +
		"1. Stay in the scope of the brief; do not touch anything outside it.\n" +
		"2. Never push to a protected branch (main, master, trunk, develop, release, production).\n" +
		"3. When you finish or are blocked, end your last message with this report and nothing after it:\n" + reportShape
}

func listBlock(title string, xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(title + ":\n")
	for _, x := range xs {
		b.WriteString("- " + x + "\n")
	}
	return b.String()
}

// render is the deterministic text of a cleaned brief; briefID is derived from it.
func (b briefIn) render() (text, briefID string) {
	var body strings.Builder
	body.WriteString("GOAL: " + b.Goal + "\n")
	if b.Scope != "" {
		body.WriteString("SCOPE: " + b.Scope + "\n")
	}
	body.WriteString(listBlock("ACCEPTANCE", b.Acceptance))
	body.WriteString(listBlock("VERIFY", b.Verify))
	if b.Timebox != "" {
		body.WriteString("TIMEBOX: " + b.Timebox + "\n")
	}
	body.WriteString(listBlock("FORBIDDEN", b.Forbidden))
	if b.Report != "" {
		body.WriteString("REPORT SHOULD COVER: " + b.Report + "\n")
	}
	sum := sha256.Sum256([]byte(body.String()))
	briefID = "br_" + hex.EncodeToString(sum[:4])
	return briefHeading + " " + briefID + "\n" + body.String() + "\n" + standingOrders(briefID), briefID
}

// composeStart puts the Bot's task and its brief together as the first message. Without a brief the task is as it
// was and the brief id is "". The whole text must pass the start-text check and fit.
func composeStart(task string, b *briefIn) (text, briefID string, err error) {
	if b == nil {
		return task, "", nil
	}
	cb, err := b.clean()
	if err != nil {
		return "", "", err
	}
	rendered, id := cb.render()
	text = task + "\n\n" + rendered
	if n := utf8.RuneCountInString(text); n > maxStartTotal {
		return "", "", fmt.Errorf("the task and the brief have %d characters together, at most %d", n, maxStartTotal)
	}
	if err := checkStartText(text); err != nil {
		return "", "", fmt.Errorf("the task and the brief together: %w", err)
	}
	return text, id, nil
}

// ---- the report ----

// report is a parsed final report.
type report struct {
	Status   string
	Commit   string
	Evidence []string
}

var (
	reportStatusRE = regexp.MustCompile(`^STATUS:[ \t]*(PASS|ISSUES|BLOCKED)[ \t]*$`)
	reportCommitRE = regexp.MustCompile(`^COMMIT:[ \t]*(none|[0-9a-f]{7,40})[ \t]*$`)
	reportEvidence = "EVIDENCE:"
)

const (
	maxReportEvidence = 20
	maxEvidenceRunes  = 300
)

// parseReport reads the last well-formed report out of a turn's final text: a STATUS line, the COMMIT line right
// after it and at least one EVIDENCE line right after that. Anything else (a lower-case status, an unknown status, a
// missing or odd commit, no evidence, lines in another order) is not a report and ok is false: the Bot gets the raw
// text only.
func parseReport(text string) (r report, ok bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := reportStatusRE.FindStringSubmatch(strings.TrimRight(lines[i], " \t"))
		if m == nil {
			continue
		}
		cand := report{Status: m[1]}
		if i+1 >= len(lines) {
			continue
		}
		cm := reportCommitRE.FindStringSubmatch(strings.TrimRight(lines[i+1], " \t"))
		if cm == nil {
			continue
		}
		cand.Commit = cm[1]
		for _, l := range lines[i+2:] {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, reportEvidence) {
				break
			}
			if e := strings.TrimSpace(strings.TrimPrefix(l, reportEvidence)); e != "" && len(cand.Evidence) < maxReportEvidence {
				cand.Evidence = append(cand.Evidence, oneLine(e, maxEvidenceRunes))
			}
		}
		if len(cand.Evidence) == 0 {
			continue
		}
		return cand, true
	}
	return report{}, false
}

// addTo puts the parsed report into an event's data.
func (r report) addTo(data map[string]any) {
	data["status"], data["commit"], data["evidence"] = r.Status, r.Commit, r.Evidence
}
