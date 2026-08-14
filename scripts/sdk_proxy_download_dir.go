//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	var result struct {
		Dir   string
		Error string
	}
	if err := json.NewDecoder(os.Stdin).Decode(&result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if result.Error != "" || result.Dir == "" {
		fmt.Fprintln(os.Stderr, result.Error)
		os.Exit(1)
	}
	fmt.Print(result.Dir)
}
