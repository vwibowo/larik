package llm

import "testing"

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

func TestResolveSamplingPrefersTheMostSpecificSetting(t *testing.T) {
	def := &Sampling{Temperature: f(0.1)}
	perModel := map[string]*Sampling{
		"ollama/qwen3:4b": {Temperature: f(0.2)},
		"qwen3:4b":        {Temperature: f(0.3)},
	}

	// provider/model beats a bare id, which beats the published default.
	if got := ResolveSampling("ollama", "qwen3:4b", def, perModel); *got.Temperature != 0.2 {
		t.Errorf("provider/model should win, got %v", *got.Temperature)
	}
	if got := ResolveSampling("lmstudio", "qwen3:4b", def, perModel); *got.Temperature != 0.3 {
		t.Errorf("bare model id should win, got %v", *got.Temperature)
	}

	// A model with no entry of its own falls back to the default.
	if got := ResolveSampling("anthropic", "claude-opus-5", def, nil); *got.Temperature != 0.1 {
		t.Errorf("a model with no entry should use the default, got %v", got)
	}
	// With nothing configured at all, nothing is sent.
	if got := ResolveSampling("anthropic", "claude-opus-5", nil, nil); got != nil {
		t.Errorf("expected no sampling, got %v", got)
	}
}

// An empty entry is how a user turns sampling off for one model, so it must
// not fall through to the configured default.
func TestEmptyEntrySendsNothingForThatModel(t *testing.T) {
	perModel := map[string]*Sampling{"ollama/qwen3:4b": {}}
	if got := ResolveSampling("ollama", "qwen3:4b", &Sampling{TopK: i(40)}, perModel); got != nil {
		t.Errorf("an empty entry should send nothing, got %v", got)
	}
	// Another model still gets the default.
	if got := ResolveSampling("ollama", "llama3", &Sampling{TopK: i(40)}, perModel); got == nil || *got.TopK != 40 {
		t.Errorf("an unrelated model should still get the default, got %v", got)
	}
}

func TestParseSamplingRoundTrips(t *testing.T) {
	s, err := ParseSampling("temp=0.7, top_p=0.8 top_k=20")
	if err != nil {
		t.Fatal(err)
	}
	if *s.Temperature != 0.7 || *s.TopP != 0.8 || *s.TopK != 20 {
		t.Fatalf("parsed wrong: %v", s)
	}
	if got := s.String(); got != "temp=0.7 top_p=0.8 top_k=20" {
		t.Errorf("String() = %q", got)
	}
	// A partial spec sets only what it names.
	p, err := ParseSampling("temperature=0.2")
	if err != nil || p.TopP != nil || p.TopK != nil || *p.Temperature != 0.2 {
		t.Errorf("partial spec = %v, err %v", p, err)
	}
	// Empty means unset, not zero.
	if got, err := ParseSampling("  "); got != nil || err != nil {
		t.Errorf("empty spec = %v, %v", got, err)
	}
	for _, bad := range []string{"temp", "temp=hot", "top_k=1.5", "nope=1"} {
		if _, err := ParseSampling(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestEmptyTreatsNilAndBlankAlike(t *testing.T) {
	var nilS *Sampling
	if !nilS.Empty() || !(&Sampling{}).Empty() {
		t.Error("nil and blank should both be empty")
	}
	if (&Sampling{TopK: i(20)}).Empty() {
		t.Error("a set field is not empty")
	}
}
