package providers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	openAICodexReplayProvider = "openai"
	openAICodexReplayKind     = "reasoning_encrypted_content"
)

type openAICodexRequest struct {
	Model        string                `json:"model"`
	Input        []any                 `json:"input"`
	Instructions string                `json:"instructions,omitempty"`
	Tools        []openAICodexTool     `json:"tools,omitempty"`
	ToolChoice   string                `json:"tool_choice,omitempty"`
	PromptCache  string                `json:"prompt_cache_key,omitempty"`
	Store        bool                  `json:"store"`
	Include      []string              `json:"include"`
	Reasoning    *openAICodexReasoning `json:"reasoning,omitempty"`
	Stream       bool                  `json:"stream"`
}

type openAICodexReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type openAICodexInputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAICodexInputImage struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}

type openAICodexUserInput struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type openAICodexOutputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAICodexAssistantInput struct {
	Role    string                  `json:"role"`
	Content []openAICodexOutputText `json:"content"`
}

type openAICodexReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAICodexReasoningInput struct {
	Type             string                        `json:"type"`
	Summary          []openAICodexReasoningSummary `json:"summary"`
	EncryptedContent string                        `json:"encrypted_content"`
}

type openAICodexFunctionCallInput struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAICodexFunctionOutputInput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output any    `json:"output"`
}

type openAICodexTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict"`
}

func openAICodexRequestFromModel(req *model.Request, modelName string) (openAICodexRequest, error) {
	if req == nil {
		return openAICodexRequest{}, fmt.Errorf("model: request is nil")
	}
	if req.Reasoning.BudgetTokens > 0 {
		return openAICodexRequest{}, errorcode.New(errorcode.Unsupported, "openai codex: reasoning token budgets are unsupported")
	}
	if req.Output != nil && req.Output.Mode != "" && req.Output.Mode != model.OutputModeText {
		return openAICodexRequest{}, &model.OutputSpecError{
			Mode:   req.Output.Mode,
			Detail: "OpenAI Codex Responses structured output is not implemented",
		}
	}
	instructions, input, err := openAICodexInputs(req.Instructions, req.Messages)
	if err != nil {
		return openAICodexRequest{}, err
	}
	tools := openAICodexTools(req.Tools)
	toolChoice := ""
	if len(tools) > 0 {
		toolChoice = "auto"
	}
	reasoning := &openAICodexReasoning{Effort: strings.TrimSpace(req.Reasoning.Effort), Summary: "auto"}
	return openAICodexRequest{
		Model:        strings.TrimSpace(modelName),
		Input:        input,
		Instructions: instructions,
		Tools:        tools,
		ToolChoice:   toolChoice,
		Store:        false,
		Include:      []string{"reasoning.encrypted_content"},
		Reasoning:    reasoning,
		Stream:       true,
	}, nil
}

func openAICodexTools(specs []model.ToolSpec) []openAICodexTool {
	definitions := model.FunctionToolDefinitions(specs)
	if len(definitions) == 0 {
		return nil
	}
	out := make([]openAICodexTool, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, openAICodexTool{
			Type:        "function",
			Name:        strings.TrimSpace(definition.Name),
			Description: definition.Description,
			Parameters:  cloneAnyMap(definition.Parameters),
			Strict:      false,
		})
	}
	return out
}

func openAICodexInputs(instructionParts []model.Part, messages []model.Message) (string, []any, error) {
	out := make([]any, 0, len(messages))
	instructions := make([]string, 0, 1)
	seenToolCalls := map[string]struct{}{}
	if len(instructionParts) > 0 {
		text, err := openAICodexTextOnly(instructionParts, "instructions")
		if err != nil {
			return "", nil, err
		}
		if text != "" {
			instructions = append(instructions, text)
		}
	}
	for _, message := range messages {
		switch message.Role {
		case model.RoleSystem:
			text, err := openAICodexTextOnly(message.Parts, "system message")
			if err != nil {
				return "", nil, err
			}
			if text != "" {
				instructions = append(instructions, text)
			}
		case model.RoleUser:
			content, err := openAICodexUserContent(message.Parts)
			if err != nil {
				return "", nil, err
			}
			if len(content) == 0 {
				return "", nil, errorcode.New(errorcode.InvalidArgument, "openai codex: user message has no supported content")
			}
			out = append(out, openAICodexUserInput{Role: string(model.RoleUser), Content: content})
		case model.RoleAssistant:
			var err error
			out, err = openAICodexAssistantContent(out, message.Parts, seenToolCalls)
			if err != nil {
				return "", nil, err
			}
		case model.RoleTool:
			for _, result := range message.ToolResults() {
				callID := strings.TrimSpace(result.ToolUseID)
				if callID == "" {
					continue
				}
				if _, ok := seenToolCalls[callID]; !ok {
					continue
				}
				output, err := openAICodexToolResultOutput(result)
				if err != nil {
					return "", nil, err
				}
				out = append(out, openAICodexFunctionOutputInput{Type: "function_call_output", CallID: callID, Output: output})
			}
		default:
			return "", nil, errorcode.New(errorcode.Unsupported, fmt.Sprintf("openai codex: unsupported message role %q", message.Role))
		}
	}
	return strings.Join(instructions, "\n\n"), out, nil
}

func openAICodexTextOnly(parts []model.Part, contextLabel string) (string, error) {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Kind != model.PartKindText || part.Text == nil {
			return "", errorcode.New(errorcode.Unsupported, fmt.Sprintf("openai codex: %s contains unsupported part %q", contextLabel, part.Kind))
		}
		if part.Text.Text != "" {
			texts = append(texts, part.Text.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

func openAICodexUserContent(parts []model.Part) ([]any, error) {
	out := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.PartKindText:
			if part.Text != nil {
				out = append(out, openAICodexInputText{Type: "input_text", Text: part.Text.Text})
			}
		case model.PartKindMedia:
			image, err := openAICodexImage(part)
			if err != nil {
				return nil, err
			}
			out = append(out, image)
		default:
			return nil, errorcode.New(errorcode.Unsupported, fmt.Sprintf("openai codex: user message contains unsupported part %q", part.Kind))
		}
	}
	return out, nil
}

func openAICodexImage(part model.Part) (openAICodexInputImage, error) {
	if part.Media == nil || part.Media.Modality != model.MediaModalityImage {
		return openAICodexInputImage{}, errorcode.New(errorcode.Unsupported, "openai codex: only image media is supported")
	}
	if part.Media.Source.Kind != model.MediaSourceInline {
		return openAICodexInputImage{}, errorcode.New(errorcode.Unsupported, "openai codex: only inline image media is supported")
	}
	mimeType := strings.TrimSpace(part.Media.MimeType)
	data := strings.TrimSpace(part.Media.Source.Data)
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") || data == "" {
		return openAICodexInputImage{}, errorcode.New(errorcode.InvalidArgument, "openai codex: inline image requires an image MIME type and base64 data")
	}
	return openAICodexInputImage{Type: "input_image", ImageURL: "data:" + mimeType + ";base64," + data}, nil
}

func openAICodexAssistantContent(out []any, parts []model.Part, seenToolCalls map[string]struct{}) ([]any, error) {
	text := make([]openAICodexOutputText, 0, 1)
	flushText := func() {
		if len(text) == 0 {
			return
		}
		out = append(out, openAICodexAssistantInput{Role: string(model.RoleAssistant), Content: text})
		text = nil
	}
	for _, part := range parts {
		switch part.Kind {
		case model.PartKindText:
			if part.Text != nil {
				text = append(text, openAICodexOutputText{Type: "output_text", Text: part.Text.Text})
			}
		case model.PartKindReasoning:
			flushText()
			if replay, ok := openAICodexReasoningReplay(part); ok {
				out = append(out, replay)
			}
		case model.PartKindToolUse:
			flushText()
			call, err := openAICodexFunctionCall(part)
			if err != nil {
				return nil, err
			}
			seenToolCalls[call.CallID] = struct{}{}
			out = append(out, call)
		default:
			return nil, errorcode.New(errorcode.Unsupported, fmt.Sprintf("openai codex: assistant message contains unsupported part %q", part.Kind))
		}
	}
	flushText()
	return out, nil
}

func openAICodexReasoningReplay(part model.Part) (openAICodexReasoningInput, bool) {
	if part.Reasoning == nil || part.Reasoning.Replay == nil {
		return openAICodexReasoningInput{}, false
	}
	replay := part.Reasoning.Replay
	provider := strings.ToLower(strings.TrimSpace(replay.Provider))
	if provider != "" && provider != openAICodexReplayProvider && provider != "openai-codex" {
		return openAICodexReasoningInput{}, false
	}
	kind := strings.TrimSpace(replay.Kind)
	if kind != "" && kind != openAICodexReplayKind {
		return openAICodexReasoningInput{}, false
	}
	token := strings.TrimSpace(replay.Token)
	if token == "" {
		return openAICodexReasoningInput{}, false
	}
	summary := []openAICodexReasoningSummary{}
	if part.Reasoning.VisibleText != nil && *part.Reasoning.VisibleText != "" {
		summary = append(summary, openAICodexReasoningSummary{Type: "summary_text", Text: *part.Reasoning.VisibleText})
	}
	return openAICodexReasoningInput{Type: "reasoning", Summary: summary, EncryptedContent: token}, true
}

func openAICodexFunctionCall(part model.Part) (openAICodexFunctionCallInput, error) {
	if part.ToolUse == nil {
		return openAICodexFunctionCallInput{}, errorcode.New(errorcode.InvalidArgument, "openai codex: tool-use part is empty")
	}
	callID := strings.TrimSpace(part.ToolUse.ID)
	name := strings.TrimSpace(part.ToolUse.Name)
	if callID == "" || name == "" {
		return openAICodexFunctionCallInput{}, errorcode.New(errorcode.InvalidArgument, "openai codex: tool call requires id and name")
	}
	arguments := "{}"
	if calls := model.NewMessage(model.RoleAssistant, part).ToolCalls(); len(calls) > 0 && strings.TrimSpace(calls[0].Args) != "" {
		arguments = strings.TrimSpace(calls[0].Args)
	}
	if !json.Valid([]byte(arguments)) {
		return openAICodexFunctionCallInput{}, errorcode.New(errorcode.InvalidArgument, "openai codex: tool call arguments are not valid JSON")
	}
	return openAICodexFunctionCallInput{Type: "function_call", CallID: callID, Name: name, Arguments: arguments}, nil
}

func openAICodexToolResultOutput(result model.ToolResultPart) (any, error) {
	if len(result.Content) == 0 {
		return "{}", nil
	}
	plain := make([]string, 0, len(result.Content))
	content := make([]any, 0, len(result.Content))
	hasImage := false
	for _, part := range result.Content {
		switch part.Kind {
		case model.PartKindText:
			if part.Text == nil {
				continue
			}
			plain = append(plain, part.Text.Text)
			content = append(content, openAICodexInputText{Type: "input_text", Text: part.Text.Text})
		case model.PartKindJSON:
			raw := strings.TrimSpace(string(part.JSONValue()))
			if raw == "" {
				raw = "{}"
			}
			if !json.Valid([]byte(raw)) {
				return nil, errorcode.New(errorcode.InvalidArgument, "openai codex: tool result contains invalid JSON")
			}
			plain = append(plain, raw)
			content = append(content, openAICodexInputText{Type: "input_text", Text: raw})
		case model.PartKindMedia:
			image, err := openAICodexImage(part)
			if err != nil {
				return nil, err
			}
			hasImage = true
			content = append(content, image)
		default:
			return nil, errorcode.New(errorcode.Unsupported, fmt.Sprintf("openai codex: tool result contains unsupported part %q", part.Kind))
		}
	}
	if !hasImage {
		return strings.Join(plain, "\n"), nil
	}
	return content, nil
}

type openAICodexUsage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
	InputTokenDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokenDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u openAICodexUsage) toKernelUsage() model.Usage {
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	return model.Usage{
		PromptTokens:      u.InputTokens,
		CachedInputTokens: u.InputTokenDetails.CachedTokens,
		CompletionTokens:  u.OutputTokens,
		ReasoningTokens:   u.OutputTokenDetails.ReasoningTokens,
		TotalTokens:       total,
	}
}

type openAICodexErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

type openAICodexIncompleteDetails struct {
	Reason string `json:"reason"`
}

type openAICodexOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAICodexOutputItem struct {
	ID               string                        `json:"id"`
	Type             string                        `json:"type"`
	CallID           string                        `json:"call_id"`
	Name             string                        `json:"name"`
	Arguments        string                        `json:"arguments"`
	EncryptedContent string                        `json:"encrypted_content"`
	Summary          []openAICodexReasoningSummary `json:"summary"`
	Content          []openAICodexOutputContent    `json:"content"`
}

type openAICodexResponseWire struct {
	ID                string                        `json:"id"`
	Model             string                        `json:"model"`
	Status            string                        `json:"status"`
	Output            []openAICodexOutputItem       `json:"output"`
	IncompleteDetails *openAICodexIncompleteDetails `json:"incomplete_details"`
	Usage             *openAICodexUsage             `json:"usage"`
	Error             *openAICodexErrorPayload      `json:"error"`
}

type openAICodexStreamWire struct {
	Type         string                   `json:"type"`
	Delta        string                   `json:"delta"`
	ItemID       string                   `json:"item_id"`
	OutputIndex  int                      `json:"output_index"`
	SummaryIndex int                      `json:"summary_index"`
	Item         *openAICodexOutputItem   `json:"item"`
	Response     *openAICodexResponseWire `json:"response"`
	Code         string                   `json:"code"`
	Message      string                   `json:"message"`
	Param        string                   `json:"param"`
}

type openAICodexOutputSlot struct {
	kind             string
	id               string
	callID           string
	name             string
	text             strings.Builder
	reasoning        strings.Builder
	arguments        strings.Builder
	encryptedContent string
}

type openAICodexAccumulator struct {
	slots       map[int]*openAICodexOutputSlot
	itemIndexes map[string]int
	hasToolCall bool
}

func newOpenAICodexAccumulator() *openAICodexAccumulator {
	return &openAICodexAccumulator{slots: map[int]*openAICodexOutputSlot{}, itemIndexes: map[string]int{}}
}

func (a *openAICodexAccumulator) slot(index int, itemID string, kind string) *openAICodexOutputSlot {
	if mapped, ok := a.itemIndexes[strings.TrimSpace(itemID)]; ok {
		index = mapped
	}
	entry := a.slots[index]
	if entry == nil {
		entry = &openAICodexOutputSlot{}
		a.slots[index] = entry
	}
	if itemID = strings.TrimSpace(itemID); itemID != "" {
		a.itemIndexes[itemID] = index
		entry.id = itemID
	}
	if kind != "" {
		entry.kind = kind
	}
	return entry
}

func (a *openAICodexAccumulator) applyItem(item openAICodexOutputItem, index int) {
	entry := a.slot(index, item.ID, item.Type)
	switch item.Type {
	case "reasoning":
		entry.encryptedContent = strings.TrimSpace(item.EncryptedContent)
		if summary := openAICodexSummaryText(item.Summary); summary != "" {
			entry.reasoning.Reset()
			entry.reasoning.WriteString(summary)
		}
	case "message":
		if text := openAICodexOutputTextContent(item.Content); text != "" {
			entry.text.Reset()
			entry.text.WriteString(text)
		}
	case "function_call":
		a.hasToolCall = true
		entry.callID = strings.TrimSpace(item.CallID)
		entry.name = strings.TrimSpace(item.Name)
		if item.Arguments != "" {
			entry.arguments.Reset()
			entry.arguments.WriteString(item.Arguments)
		}
	}
}

func openAICodexSummaryText(parts []openAICodexReasoningSummary) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "summary_text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func openAICodexOutputTextContent(parts []openAICodexOutputContent) string {
	var out strings.Builder
	for _, part := range parts {
		if part.Type == "output_text" {
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

func (a *openAICodexAccumulator) appendText(event openAICodexStreamWire) {
	a.slot(event.OutputIndex, event.ItemID, "message").text.WriteString(event.Delta)
}

func (a *openAICodexAccumulator) appendReasoning(event openAICodexStreamWire) {
	a.slot(event.OutputIndex, event.ItemID, "reasoning").reasoning.WriteString(event.Delta)
}

func (a *openAICodexAccumulator) appendArguments(event openAICodexStreamWire) {
	a.hasToolCall = true
	a.slot(event.OutputIndex, event.ItemID, "function_call").arguments.WriteString(event.Delta)
}

func (a *openAICodexAccumulator) message() (model.Message, error) {
	indexes := make([]int, 0, len(a.slots))
	for index := range a.slots {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	parts := make([]model.Part, 0, len(indexes))
	for _, index := range indexes {
		entry := a.slots[index]
		switch entry.kind {
		case "reasoning":
			visible := entry.reasoning.String()
			token := strings.TrimSpace(entry.encryptedContent)
			if visible == "" && token == "" {
				continue
			}
			visibility := model.ReasoningVisibilityVisible
			if visible == "" {
				visibility = model.ReasoningVisibilityTokenOnly
			}
			part := model.NewReasoningPart(visible, visibility)
			if token != "" && part.Reasoning != nil {
				part.Reasoning.Replay = &model.ReplayMeta{Provider: openAICodexReplayProvider, Kind: openAICodexReplayKind, Token: token}
			}
			parts = append(parts, part)
		case "message":
			if entry.text.Len() > 0 {
				parts = append(parts, model.NewTextPart(entry.text.String()))
			}
		case "function_call":
			callID := strings.TrimSpace(entry.callID)
			name := strings.TrimSpace(entry.name)
			if callID == "" || name == "" {
				return model.Message{}, errorcode.New(errorcode.InvalidArgument, "openai codex: streamed tool call requires id and name")
			}
			arguments := strings.TrimSpace(entry.arguments.String())
			if arguments == "" {
				arguments = "{}"
			}
			if !json.Valid([]byte(arguments)) {
				return model.Message{}, errorcode.New(errorcode.InvalidArgument, "openai codex: streamed tool arguments are not valid JSON")
			}
			parts = append(parts, model.NewToolUsePart(callID, name, json.RawMessage(arguments)))
		}
	}
	return model.NewMessage(model.RoleAssistant, parts...), nil
}

func openAICodexFinishReason(response *openAICodexResponseWire, hasToolCall bool) (model.FinishReason, string) {
	reason := ""
	if response != nil && response.IncompleteDetails != nil {
		reason = strings.TrimSpace(response.IncompleteDetails.Reason)
	}
	switch reason {
	case "max_output_tokens":
		return model.FinishReasonLength, reason
	case "content_filter":
		return model.FinishReasonContentFilter, reason
	case "":
		if hasToolCall {
			return model.FinishReasonToolCalls, ""
		}
		return model.FinishReasonStop, ""
	default:
		if hasToolCall {
			return model.FinishReasonToolCalls, reason
		}
		return model.FinishReasonUnknown, reason
	}
}
