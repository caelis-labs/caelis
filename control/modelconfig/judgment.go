package modelconfig

import (
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
	"github.com/caelis-labs/caelis/agent-sdk/judgment/typesafe"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// APISystemOne identifies the typed evaluation protocol. It is configuration
// vocabulary only; conversational model factories do not implement it.
const APISystemOne model.APIType = "typesafe-system-one"

// IsJudgment reports whether configuration selects a typed evaluator.
func IsJudgment(config Config) bool { return NormalizeConfig(config).API == APISystemOne }

// BuildJudgment constructs a typed evaluator from resolved Host credentials.
func BuildJudgment(config Config) (judgment.Evaluator, error) {
	config = NormalizeConfig(config)
	if config.API != APISystemOne {
		return nil, fmt.Errorf("modelconfig: model does not support typed judgments")
	}
	return typesafe.New(typesafe.Config{
		BaseURL: config.BaseURL, APIKey: config.Token, Model: config.Model,
		HTTPClient: config.HTTPClient, Timeout: min(config.Timeout, 10*time.Second),
	})
}
