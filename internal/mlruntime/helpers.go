package mlruntime

// Helpers shared by the multi-attribute primitives (similarity_cosine,
// cluster_dbscan, forecast_exponential_smoothing). They read commonly
// shaped values out of Input.Params and convert various Go numeric
// shapes into float64.
//
// The single-attribute primitives (anomaly, threshold) keep their own
// tight helpers — these are deliberately conservative to make the
// dispatch path between the planner and the primitive resilient to
// representation drift.

// toFloat converts the supported Go numeric shapes to float64. Returns
// (0, false) for anything that doesn't look numeric.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// toInt is the integer counterpart of toFloat. Accepts the same numeric
// shapes; float64 values that aren't whole numbers are still rounded
// rather than rejected, because entity IDs reach primitives through
// FactStore rows where the entity column lands as float64.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	}
	return 0, false
}

func readFloat(params map[string]any, key string) (float64, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	return toFloat(v)
}

func readInt(params map[string]any, key string) (int, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	return toInt(v)
}

func readString(params map[string]any, key string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// readBoolOr returns the bool param at key, or fallback when absent/non-bool.
func readBoolOr(params map[string]any, key string, fallback bool) bool {
	if v, ok := params[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return fallback
}

// readStringSlice accepts either []string or []any (each element a string).
// The planner builds slices freshly per step, so both shapes can land
// here depending on how Params was constructed.
func readStringSlice(params map[string]any, key string) []string {
	v, ok := params[key]
	if !ok {
		return nil
	}
	switch ss := v.(type) {
	case []string:
		return ss
	case []any:
		out := make([]string, 0, len(ss))
		for _, e := range ss {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
