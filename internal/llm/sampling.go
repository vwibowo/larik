package llm

import (
	"fmt"
	"strconv"
	"strings"
)

// ResolveSampling picks the decoding parameters for one request. More
// specific settings win: a "provider/model" entry, then a bare model entry,
// then def. The result is nil when nothing applies, which sends no
// parameters and leaves the server's own defaults in place.
//
// A perModel entry is used as given rather than merged, so an entry that
// sets no field (`{}` in settings) sends nothing for that model even when
// def would otherwise apply.
//
// Larik ships no built-in values. Measured against qwen3:4b through Ollama,
// Qwen's published parameters (0.7/0.8/20) and Ollama's defaults
// (0.8/0.9/40) produced identical results — 24 of 24 usable tool calls each
// — so a table of vendor recommendations would have been unearned. Sampling
// did matter at the extreme (2.0/1.0/0), where the model stopped emitting
// tool calls altogether, so the knob is worth having; a default for it is
// not.
func ResolveSampling(provider, model string, def *Sampling, perModel map[string]*Sampling) *Sampling {
	if s, ok := perModel[provider+"/"+model]; ok {
		return nonEmpty(s)
	}
	if s, ok := perModel[model]; ok {
		return nonEmpty(s)
	}
	return nonEmpty(def)
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
