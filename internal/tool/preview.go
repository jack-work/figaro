package tool

// PreviewArger returns a lookup fn backed by r: it calls a tool's optional
// PreviewArg() method, else "". Nil-safe (nil r => always ""). Mirrors
// Summarizer in summarize.go.
func PreviewArger(r *Registry) func(name string) string {
	if r == nil {
		return func(string) string { return "" }
	}
	return func(name string) string {
		t, ok := r.Get(name)
		if !ok {
			return ""
		}
		p, ok := t.(interface{ PreviewArg() string })
		if !ok {
			return ""
		}
		return p.PreviewArg()
	}
}
