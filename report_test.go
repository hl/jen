package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFeedbackDistinguishesInconclusiveGateFromFailure(t *testing.T) {
	for _, passed := range []bool{true, false} {
		choice := "accept"
		if !passed {
			choice = "revise"
		}
		for _, confidence := range []float64{0, 0.49, 0.5, 0.9} {
			report := &Report{
				Gate: &GateResult{Question: "gate", Choice: choice,
					Passed: passed, Confidence: new(confidence)},
				Summary: map[string]SummaryEntry{
					"gate": {Type: "choice", Choice: choice, Confidence: new(confidence),
						Probabilities: map[string]float64{"accept": 0.85, "revise": 0.15}},
					"python": {Type: "choice", Choice: "sound", Confidence: new(0.9)},
				},
			}
			feedback := renderFeedback(report, []string{"gate", "python"})
			want := "PASSED"
			if !passed {
				want = "FAILED"
			}
			if confidence < lowConfidenceAt {
				want = "INCONCLUSIVE"
				if strings.Contains(feedback, "Action required: fix") {
					t.Errorf("uncertain choice must not instruct blind fixes: %s", feedback)
				}
			}
			if !strings.HasPrefix(feedback, "PLAN VALIDATION "+want+":") {
				t.Errorf("passed=%v confidence=%v: %s", passed, confidence, feedback)
			}
			for _, detail := range []string{"accept=0.85", "revise=0.15", `python: "sound"`} {
				if !strings.Contains(feedback, detail) {
					t.Errorf("missing %q in %s", detail, feedback)
				}
			}
		}
	}
}

func TestScoreFeedbackPreservesAmbiguousLevelProbabilities(t *testing.T) {
	answer := Answer{Type: "score", Score: new(1.5), Confidence: new(0.2),
		Legend: map[string]json.RawMessage{"0": json.RawMessage(`"broken"`),
			"1": json.RawMessage(`"adequate"`), "2": json.RawMessage(`"strong"`)},
		Probabilities: map[string]float64{"0": 0, "1": 0.5, "2": 0.5}}
	report := buildReport(&APIResponse{Answers: map[string]Answer{"quality": answer}}, nil, []string{"quality"})
	feedback := renderFeedback(report, []string{"quality"})
	for _, detail := range []string{"1=0.50", "2=0.50", "LOW CONFIDENCE", "distribution concentration"} {
		if !strings.Contains(feedback, detail) {
			t.Errorf("missing %q in %s", detail, feedback)
		}
	}
}
