// Package openaisdk holds the shared OpenAI SDK plumbing used by the
// Responses and Chat Completions adapters. Provider-specific policies stay
// with their respective adapters.
package openaisdk

import (
	"errors"
	"fmt"

	sdk "github.com/openai/openai-go/v3"

	"larik/internal/llm"
)

// ConvertError classifies SDK errors using the adapter's overflow policy.
// Non-SDK errors pass through unchanged.
func ConvertError(err error, overflow func(*sdk.Error) bool) error {
	var apiErr *sdk.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	if overflow(apiErr) {
		return fmt.Errorf("%w: %v", llm.ErrContextOverflow, err)
	}
	return llm.ClassifyStatus(apiErr.StatusCode, err)
}
