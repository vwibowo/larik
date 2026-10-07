package llm

import (
	"fmt"
	"strconv"
	"strings"
)

// samplingDefaults holds decoding parameters a model's own vendor publishes,
// for models whose server defaults are a poor fit for tool calling. Keys are
// matched as a prefix of the bare model id, lowercased, so an Ollama tag such
// as "qwen3-coder:30b" finds the "qwen3" entry; the longest key wins.
//
// Only add an entry backed by a published recommendation, and keep this table
// short. A model that is absent sends nothing and keeps its server's defaults.
var samplingDefaults = map[string]Sampling{
	// Qwen's model cards recommend Temperature 0.7, TopP 0.8, TopK 20 for
	// non-thinking generation, which is what a request with no effort set
	// asks for. Ollama would otherwise apply 0.8 / 0.9 / 40.
	"qwen3": {Temperature: f(0.7), TopP: f(0.8), TopK: i(20)},
}

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

// ResolveSampling picks the decoding parameters for one request. More specific
// settings win: a "provider/model" entry, then a bare model entry, then the
// published default for that model, then def. The result is nil when nothing
// applies, which sends no parameters at all.
//
// A perModel entry is used as given rather than merged, so an entry that sets
// no field (`{}` in settings) suppresses a published default.
func ResolveSampling(provider, model string, def *Sampling, perModel map[string]*Sampling) *Sampling {
	if s, ok := perModel[provider+"/"+model]; ok {
		return nonEmpty(s)
	}
	if s, ok := perModel[model]; ok {
		return nonEmpty(s)
	}
	if s, ok := publishedSampling(model); ok {
		return nonEmpty(&s)
	}
	return nonEmpty(def)
}

// publishedSampling returns the vendor-published parameters for model, found
// by the longest matching prefix of its lowercased id.
func publishedSampling(model string) (Sampling, bool) {
	name := strings.ToLower(model)
	// An OpenRouter-style "vendor/model" id matches on its last element too.
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var best string
	for k := range samplingDefaults {
		if strings.HasPrefix(name, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return Sampling{}, false
	}
	return samplingDefaults[best], true
}

func nonEmpty(s *Sampling) *Sampling {
	if s.Empty() {
		return nil
	}
	return s
}

// ParseSampling reads a compact "temp=0.7 top_p=0.8 top_k=20" spec, as
// /config and /sampling accept it. Pairs are separated by spaces or commas
// and may appear in any order. An empty spec returns nil, which sends no
// parameters; a spec that names keys returns only those.
func ParseSampling(spec string) (*Sampling, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out Sampling
	for _, field := range strings.FieldsFunc(spec, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return nil, fmt.Errorf("%q is not key=value", field)
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "temp", "temperature":
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return nil, fmt.Errorf("temperature %q is not a number", value)
			}
			out.Temperature = &v
		case "top_p", "topp":
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return nil, fmt.Errorf("top_p %q is not a number", value)
			}
			out.TopP = &v
		case "top_k", "topk":
			v, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("top_k %q is not a whole number", value)
			}
			out.TopK = &v
		default:
			return nil, fmt.Errorf("unknown parameter %q (temp, top_p, top_k)", key)
		}
	}
	return &out, nil
}

// String renders s the way ParseSampling reads it. A nil or empty Sampling
// renders as "" so callers can show it as unset.
func (s *Sampling) String() string {
	if s.Empty() {
		return ""
	}
	var parts []string
	if s.Temperature != nil {
		parts = append(parts, "temp="+strconv.FormatFloat(*s.Temperature, 'g', -1, 64))
	}
	if s.TopP != nil {
		parts = append(parts, "top_p="+strconv.FormatFloat(*s.TopP, 'g', -1, 64))
	}
	if s.TopK != nil {
		parts = append(parts, "top_k="+strconv.Itoa(*s.TopK))
	}
	return strings.Join(parts, " ")
}

// JSON renders s as the object settings files hold, for writing a config
// entry. An empty Sampling becomes an empty object, which suppresses a
// published default rather than falling through to it.
func (s *Sampling) JSON() map[string]any {
	out := map[string]any{}
	if s == nil {
		return out
	}
	if s.Temperature != nil {
		out["temperature"] = *s.Temperature
	}
	if s.TopP != nil {
		out["top_p"] = *s.TopP
	}
	if s.TopK != nil {
		out["top_k"] = *s.TopK
	}
	return out
}
