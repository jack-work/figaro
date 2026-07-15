package tool

// Summarizer returns a display-summary fn backed by r: it calls a tool's
// optional Summarize(args) method, else "". Nil-safe (nil r => always "").
func Summarizer(r *Registry) func(name string, args map[string]any) string {
	if r == nil {
		return func(string, map[string]any) string { return "" }
	}
	return func(name string, args map[string]any) string {
		t, ok := r.Get(name)
		if !ok {
			return ""
		}
		s, ok := t.(interface{ Summarize(map[string]any) string })
		if !ok {
			return ""
		}
		return s.Summarize(args)
	}
}
