// Command traceaudit validates every typed wirev1 Envelope in one Caelis
// headless JSONL trace. Outer headless record validation remains owned by the
// Terminal-Bench report builder.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/caelis-labs/caelis/control/client/wirev1"
)

type headlessRecord struct {
	SchemaVersion string          `json:"schema_version"`
	Type          string          `json:"type"`
	Envelope      json.RawMessage `json:"envelope"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: traceaudit <caelis.jsonl>")
		os.Exit(2)
	}
	if err := validate(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validate(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var record headlessRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("line %d: decode headless record: %w", lineNumber, err)
		}
		if record.SchemaVersion != "caelis.headless/v1" || record.Type != "envelope" {
			continue
		}
		envelope, err := wirev1.UnmarshalEnvelope(record.Envelope)
		if err != nil {
			return fmt.Errorf("line %d: decode wirev1 envelope: %w", lineNumber, err)
		}
		if envelope.Kind == "" {
			return fmt.Errorf("line %d: wirev1 envelope kind is empty", lineNumber)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan trace: %w", err)
	}
	return nil
}
