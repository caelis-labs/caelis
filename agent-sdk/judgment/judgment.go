// Package judgment defines bounded semantic evaluations whose answers are
// supplied choices or probabilities. Callers own policy and result application.
package judgment

import "context"

// Kind identifies the meaning of a question and its answer.
type Kind string

const (
	Choice Kind = "choice"
	Noul   Kind = "noul"
	Score  Kind = "score"
)

// Question describes one independent judgment over the request state.
type Question struct {
	Type         Kind `json:"type"`
	Instructions any  `json:"instructions"`
	Criteria     any  `json:"criteria,omitempty"`
}

// Request contains the evidence and questions for one evaluation. Question IDs
// correlate answers; their meaning must also appear in Instructions.
type Request struct {
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Answer preserves the raw judgment. Confidence measures distribution
// concentration, not correctness or authorization. Noul has no confidence.
type Answer struct {
	Type          Kind               `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
}

// Usage contains provider-reported token counts for an evaluation.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response retains the actual model version and every requested answer.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Evaluator answers closed questions without generating conversational output.
type Evaluator interface {
	Name() string
	Evaluate(context.Context, Request) (Response, error)
}
