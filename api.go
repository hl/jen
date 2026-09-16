package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"
)

const apiURL = "https://api.typesafe.ai/v1/systemone"

// maxRetryWait caps any single backoff sleep, including Retry-After values.
const maxRetryWait = 60 * time.Second

// Answer is one typed answer from the System One API. Which fields are set
// depends on Type.
type Answer struct {
	Type          string                     `json:"type"`
	Noul          *float64                   `json:"noul,omitempty"`
	Choice        string                     `json:"choice,omitempty"`
	Score         *float64                   `json:"score,omitempty"`
	Legend        map[string]json.RawMessage `json:"legend,omitempty"`
	Probabilities map[string]float64         `json:"probabilities,omitempty"`
	Confidence    *float64                   `json:"confidence,omitempty"`
}

// validateAnswers checks every requested judgment before any report or gate
// result is emitted. A syntactically valid JSON body is not an evaluation.
func validateAnswers(resp *APIResponse, questions json.RawMessage) error {
	var byID map[string]questionMeta
	if err := json.Unmarshal(questions, &byID); err != nil {
		return err
	}
	if resp == nil || resp.Model == "" || len(resp.Answers) != len(byID) {
		return fmt.Errorf("missing model or incomplete answer set")
	}
	for id, question := range byID {
		answer, ok := resp.Answers[id]
		if !ok || answer.Type != question.Type {
			return fmt.Errorf("question %q returned a missing or mismatched answer", id)
		}
		if question.Type == "noul" {
			if !inRange(answer.Noul, 0, 1) {
				return fmt.Errorf("question %q returned an invalid noul", id)
			}
			continue
		}
		if !inRange(answer.Confidence, 0, 1) {
			return fmt.Errorf("question %q returned missing or invalid confidence", id)
		}
		var options []string
		switch question.Type {
		case "choice":
			options, _ = orderedKeys(question.Criteria)
			if indexOf(options, answer.Choice) < 0 {
				return fmt.Errorf("question %q returned an unknown choice %q", id, answer.Choice)
			}
		case "score":
			var levels []json.RawMessage
			if err := json.Unmarshal(question.Criteria, &levels); err != nil {
				return err
			}
			if !inRange(answer.Score, 0, float64(len(levels)-1)) || len(answer.Legend) != len(levels) {
				return fmt.Errorf("question %q returned missing or invalid score/legend", id)
			}
			for i := range levels {
				key := strconv.Itoa(i)
				if !validEntry(answer.Legend[key]) {
					return fmt.Errorf("question %q returned an invalid legend level %q", id, key)
				}
				options = append(options, key)
			}
		}
		if err := validateProbabilities(answer.Probabilities, options); err != nil {
			return fmt.Errorf("question %q: %w", id, err)
		}
	}
	return nil
}

func inRange(value *float64, low, high float64) bool {
	return value != nil && !math.IsNaN(*value) && *value >= low && *value <= high
}

func validateProbabilities(probabilities map[string]float64, options []string) error {
	if len(probabilities) != len(options) {
		return fmt.Errorf("incomplete probability distribution")
	}
	total := 0.0
	for _, option := range options {
		p, ok := probabilities[option]
		if !ok || !inRange(&p, 0, 1) {
			return fmt.Errorf("missing or invalid probability for %q", option)
		}
		total += p
	}
	if math.Abs(total-1) > 0.001 {
		return fmt.Errorf("probabilities do not sum to 1")
	}
	return nil
}

// Usage reports token consumption for the request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// APIResponse is the body returned by the evaluation endpoint.
type APIResponse struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// callAPI posts the payload with retries: exponential backoff on 429/529
// (honoring Retry-After) and on connection errors.
func callAPI(apiKey string, payload []byte, timeoutSeconds float64) (*APIResponse, error) {
	client := &http.Client{Timeout: time.Duration(timeoutSeconds * float64(time.Second))}
	const maxAttempts = 5
	delay := time.Second
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("connection error: %w", err)
			if attempt < maxAttempts {
				time.Sleep(delay)
				delay *= 2
				continue
			}
			return nil, lastErr
		}

		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("could not read API response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
			if attempt < maxAttempts {
				wait := delay
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, parseErr := strconv.ParseFloat(ra, 64); parseErr == nil {
						wait = time.Duration(secs * float64(time.Second))
					}
				}
				// A hostile or confused Retry-After must not park the process.
				if wait > maxRetryWait {
					wait = maxRetryWait
				}
				fmt.Fprintf(os.Stderr, "API busy (HTTP %d); retrying in %s...\n",
					resp.StatusCode, wait.Round(time.Second))
				time.Sleep(wait)
				delay *= 2
				continue
			}
			return nil, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, body)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("authentication failed (HTTP 401): check your %s", apiKeyEnv)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, body)
		}

		var apiResp APIResponse
		if err := json.Unmarshal(body, &apiResp); err != nil {
			return nil, fmt.Errorf("invalid API response body: %w", err)
		}
		return &apiResp, nil
	}
	return nil, lastErr
}
