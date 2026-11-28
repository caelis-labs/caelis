package collaboration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type userInputRecord struct {
	UserInput
	Attachments map[int]model.AttachmentRef `json:"attachments,omitempty"`
}

func decodeUserInput(body string) (UserInput, error) {
	var record userInputRecord
	err := json.Unmarshal([]byte(body), &record)
	record.attachmentRefs = record.Attachments
	return record.UserInput, err
}

func (s *Service) referenceUserImages(ctx context.Context, input UserInput) (userInputRecord, error) {
	record := userInputRecord{UserInput: input}
	if s.attachmentWriter == nil {
		return record, nil
	}
	for i, part := range input.ContentParts {
		if part.Type != model.ContentPartImage {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(part.Data)
		if err != nil || len(data) == 0 {
			return userInputRecord{}, errorcode.New(errorcode.InvalidArgument, "User image is empty or invalid")
		}
		ref, err := s.attachmentWriter.StoreAttachment(ctx, input.ChildSessionID, data)
		if err != nil {
			return userInputRecord{}, err
		}
		if ref.Instance == "" {
			continue
		}
		if record.Attachments == nil {
			record.Attachments = make(map[int]model.AttachmentRef)
			record.ContentParts = slices.Clone(input.ContentParts)
		}
		record.Attachments[i] = ref
		record.ContentParts[i].Data = ""
	}
	return record, nil
}

func (s *Service) loadUserImages(ctx context.Context, input UserInput) (UserInput, error) {
	for i, ref := range input.attachmentRefs {
		if i < 0 || i >= len(input.ContentParts) || input.ContentParts[i].Type != model.ContentPartImage {
			return input, fmt.Errorf("invalid queued user image position")
		}
		data, err := model.LoadAttachment(ctx, s.attachments, input.ChildSessionID, ref)
		if err != nil {
			return input, err
		}
		input.ContentParts[i].Data = base64.StdEncoding.EncodeToString(data)
	}
	input.attachmentRefs = nil
	return input, nil
}

func matchesUserInputParts(input UserInput, parts []model.ContentPart) bool {
	if len(input.ContentParts) != len(parts) {
		return false
	}
	for i, part := range parts {
		if ref, ok := input.attachmentRefs[i]; ok {
			data, err := base64.StdEncoding.DecodeString(part.Data)
			digest := sha256.Sum256(data)
			if err != nil || ref.Size != int64(len(data)) || ref.Digest != hex.EncodeToString(digest[:]) {
				return false
			}
			part.Data = ""
		}
		if input.ContentParts[i] != part {
			return false
		}
	}
	return true
}
