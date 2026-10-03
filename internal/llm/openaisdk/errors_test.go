package openaisdk

import (
	"errors"
	"testing"

	sdk "github.com/openai/openai-go/v3"

	"larik/internal/llm"
)

func TestConvertErrorLeavesOtherErrorsAlone(t *testing.T) {
	want := errors.New("network unavailable")
	if got := ConvertError(want, func(*sdk.Error) bool { return false }); got != want {
		t.Fatalf("error = %v, want original", got)
	}
}

func TestConvertErrorClassifiesSDKStatus(t *testing.T) {
	original := &sdk.Error{StatusCode: 429}
	classified := ConvertError(original, func(*sdk.Error) bool { return false })
	var api *llm.APIError
	if !errors.As(classified, &api) || !api.Retryable {
		t.Fatalf("status 429 classification = %v", classified)
	}
	if got := ConvertError(original, func(*sdk.Error) bool { return true }); !errors.Is(got, llm.ErrContextOverflow) {
		t.Fatalf("overflow classification = %v", got)
	}
}
