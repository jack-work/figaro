package livedoc

// ToolStyle is how a tool call draws, decided by the producer so every client
// draws the same call the same way.
type ToolStyle struct {
	Label   string
	HeadArg string
	BodyArg string
	Diff    bool
}

var toolStyles = map[string]ToolStyle{
	"bash":    {Label: "$", HeadArg: "command"},
	"process": {Label: "$", HeadArg: "command"},
	"write":   {HeadArg: "path", BodyArg: "content"},
	"read":    {HeadArg: "path"},
	"edit":    {HeadArg: "path", Diff: true},
}

// StyleTool stamps a tool node with the style its name earns.
func StyleTool(n *Node) {
	st := toolStyles[n.Name]
	n.Label, n.HeadArg, n.BodyArg, n.Diff = st.Label, st.HeadArg, st.BodyArg, st.Diff
}
