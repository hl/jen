package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SummaryEntry is the normalized, agent-friendly view of one answer.
type SummaryEntry struct {
	Type           string                     `json:"type"`
	ProbabilityYes *float64                   `json:"probability_yes,omitempty"`
	Score          *float64                   `json:"score,omitempty"`
	MaxScore       *int                       `json:"max_score,omitempty"`
	Normalized     *float64                   `json:"normalized,omitempty"`
	Legend         map[string]json.RawMessage `json:"legend,omitempty"`
	Choice         string                     `json:"choice,omitempty"`
	Probabilities  map[string]float64         `json:"probabilities,omitempty"`
	Confidence     *float64                   `json:"confidence,omitempty"`
	LowConfidence  bool                       `json:"low_confidence,omitzero"`
}

// GateResult records how the gate question resolved.
type GateResult struct {
	Question    string   `json:"question"`
	Choice      string   `json:"choice"`
	Options     []string `json:"options"`
	OptionIndex int      `json:"option_index"`
	Passed      bool     `json:"passed"`
	Confidence  *float64 `json:"confidence,omitempty"`
	ExitCode    int      `json:"exit_code"`
}

// Report is the full machine-readable output document.
type Report struct {
	Model   string                  `json:"model"`
	Mode    string                  `json:"mode,omitempty"`
	Note    string                  `json:"note,omitempty"`
	Files   []string                `json:"files"`
	Usage   Usage                   `json:"usage"`
	Summary map[string]SummaryEntry `json:"summary"`
	Answers map[string]Answer       `json:"answers"`
	Gate    *GateResult             `json:"gate,omitempty"`
}

// buildReport normalizes the raw API answers into the report document.
func buildReport(resp *APIResponse, codeFiles map[string]string, orderedIDs []string) *Report {
	summary := map[string]SummaryEntry{}
	for _, qid := range orderedIDs {
		answer, ok := resp.Answers[qid]
		if !ok {
			continue
		}
		entry := SummaryEntry{Type: answer.Type, Confidence: answer.Confidence}
		switch answer.Type {
		case "noul":
			entry.ProbabilityYes = answer.Noul
			entry.Confidence = nil // nouls carry no confidence
		case "score":
			maxScore := max(len(answer.Legend)-1, 1)
			entry.Score = answer.Score
			entry.MaxScore = new(maxScore)
			entry.Legend = answer.Legend
			if answer.Score != nil {
				entry.Normalized = new(*answer.Score / float64(maxScore))
			}
		case "choice":
			entry.Choice = answer.Choice
			entry.Probabilities = answer.Probabilities
		}
		if entry.Confidence != nil && *entry.Confidence < lowConfidenceAt {
			entry.LowConfidence = true
		}
		summary[qid] = entry
	}
	return &Report{
		Model:   resp.Model,
		Files:   sortedKeys(codeFiles),
		Usage:   resp.Usage,
		Summary: summary,
		Answers: resp.Answers,
	}
}

// ---------------------------------------------------------------------------
// Text renderers
// ---------------------------------------------------------------------------

func renderTable(report *Report, orderedIDs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "model: %s   files: %d   tokens: %d in / %d out\n\n",
		report.Model, len(report.Files), report.Usage.InputTokens, report.Usage.OutputTokens)

	type row struct{ qid, qtype, value, conf, flag string }
	rows := []row{}
	for _, qid := range orderedIDs {
		entry, ok := report.Summary[qid]
		if !ok {
			continue
		}
		r := row{qid: qid, qtype: entry.Type, conf: "-"}
		switch entry.Type {
		case "noul":
			if entry.ProbabilityYes != nil {
				r.value = fmt.Sprintf("P(yes)=%.3f", *entry.ProbabilityYes)
			}
		case "score":
			if entry.Score != nil && entry.Normalized != nil {
				r.value = fmt.Sprintf("%.2f / %d (%.0f%%)", *entry.Score, *entry.MaxScore, *entry.Normalized*100)
			}
			if entry.Confidence != nil {
				r.conf = fmt.Sprintf("%.2f", *entry.Confidence)
			}
		case "choice":
			r.value = entry.Choice
			if entry.Confidence != nil {
				r.conf = fmt.Sprintf("%.2f", *entry.Confidence)
			}
		}
		if entry.LowConfidence {
			r.flag = "  <-- low confidence"
		}
		rows = append(rows, r)
	}

	widths := [4]int{len("question"), len("type"), len("value"), len("conf")}
	for _, r := range rows {
		for i, v := range []string{r.qid, r.qtype, r.value, r.conf} {
			if len(v) > widths[i] {
				widths[i] = len(v)
			}
		}
	}
	pad := func(s string, w int) string { return s + strings.Repeat(" ", w-len(s)) }
	b.WriteString("  " + pad("question", widths[0]) + "  " + pad("type", widths[1]) +
		"  " + pad("value", widths[2]) + "  " + pad("conf", widths[3]) + "\n")
	for _, w := range widths {
		b.WriteString("  " + strings.Repeat("-", w))
	}
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString("  " + pad(r.qid, widths[0]) + "  " + pad(r.qtype, widths[1]) +
			"  " + pad(r.value, widths[2]) + "  " + pad(r.conf, widths[3]) + r.flag + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderFeedback prints compact, agent-actionable text: verdict first, then
// one line per question.
func renderFeedback(report *Report, orderedIDs []string) string {
	var lines []string
	if gate := report.Gate; gate != nil {
		status := "PASSED"
		if !gate.Passed {
			status = "FAILED"
		}
		note := ""
		if gate.Confidence != nil {
			note = fmt.Sprintf(" (confidence %.2f)", *gate.Confidence)
			if *gate.Confidence < lowConfidenceAt {
				note += " LOW CONFIDENCE: verdict unreliable, weigh the dimensions below"
			}
		}
		lines = append(lines, fmt.Sprintf("PLAN VALIDATION %s: %s = %q%s", status, gate.Question, gate.Choice, note))
	} else {
		lines = append(lines, "PLAN VALIDATION REPORT")
	}
	for _, qid := range orderedIDs {
		entry, ok := report.Summary[qid]
		if !ok {
			continue
		}
		value := "?"
		switch entry.Type {
		case "noul":
			if entry.ProbabilityYes != nil {
				p := *entry.ProbabilityYes
				value = fmt.Sprintf("P(yes)=%.2f", p)
				if p >= 0.4 && p <= 0.6 {
					value += " (uncertain: near 0.5 means the model is torn, not a medium grade)"
				}
			}
		case "score":
			if entry.Score != nil && entry.Normalized != nil {
				value = fmt.Sprintf("%.2f/%d (%.0f%%)", *entry.Score, *entry.MaxScore, *entry.Normalized*100)
			}
			if entry.Confidence != nil {
				value += fmt.Sprintf(" [confidence %.2f]", *entry.Confidence)
			}
		case "choice":
			value = fmt.Sprintf("%q", entry.Choice)
			if entry.Confidence != nil {
				value += fmt.Sprintf(" [confidence %.2f]", *entry.Confidence)
			}
		}
		if entry.LowConfidence {
			value += " LOW CONFIDENCE"
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", qid, value))
	}
	if report.Gate != nil && !report.Gate.Passed {
		lines = append(lines, "Action required: fix the weakest dimensions above, then re-run validation before marking the work complete.")
	}
	return strings.Join(lines, "\n")
}
