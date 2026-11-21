package glyph

const (
	Send          = "✎"
	Composer      = "✎"
	Comment       = "✎"
	Steering      = "↳"
	OK            = "✓"
	Fail          = "✗"
	Pending       = "·"
	Busy          = "…"
	Frames        = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	Delta         = "Δ"
	Fork          = "⑂"
	Head          = "•"
	Bullet        = "•"
	Dead          = "⊘"
	Absent        = "∅"
	HeadOpen      = "∧"
	HeadShut      = "∨"
	Quote         = "❝"
	QuoteOpen     = "╭"
	QuoteClose    = "╰"
	SnakeHang     = "╭"
	SnakeBody     = "│"
	SnakeClose    = "╰"
	SnakeElbow    = "╯"
	RuleSingle    = "─"
	RuleDouble    = "═"
	Gutter        = "│"
	CommentClosed = "▸"
	CommentOpen   = "▾"
	CommentReply  = "›"
	Newline       = "⏎"
	Select        = "▸"

	StateRunning = "▸"
	StateHere    = "●"
	StateIdle    = "○"

	QueueCommitting = "→"
	QueueWaiting    = "·"

	SessionIdle         = "𝄐"
	SessionDisconnected = "⠸"
	SessionInterrupted  = "!"
	AlertWarn           = "!"

	CallOut    = "→"
	ResultBack = "↩"
	ResultFail = "⚠"

	DecideMarker = "←"
	DecideSigil  = "??"

	PitQueue         = "𝄚"
	PitNotifications = "𝄞"
	PitCommand       = "∴"
	PitSearch        = "⌕"
	PitHelp          = "?"
	PitStatus        = "!"
	PitDropped       = "𝄽"
	PitFolk          = "웃"
	PitDecision      = "⁇"
	PitKey           = "⚷"
	PitConfirm       = "[y/N]"

	MarkQueue         = "♭"
	MarkNotifications = "♩"
	MarkForm          = "⌖"
	MarkModels        = "⟡"
	MarkReaped        = "⁂"
	MarkTroupe        = "⌘"
	MarkCompletion    = "♪"
	MarkLogin         = "▸"

	Voice  = "♫"
	Attach = "⊕"
)

type Glyph struct {
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	Marks  string `json:"marks"`
}

var table = []Glyph{
	{"send", Send, "the send-to pit and the draft that goes out"},
	{"composer", Composer, "the composer in normal mode"},
	{"comment", Comment, "a comment on the transcript"},
	{"comment.closed", CommentClosed, "a collapsed comment row"},
	{"comment.open", CommentOpen, "an expanded comment row"},
	{"comment.reply", CommentReply, "a reply inside an open comment thread"},
	{"steering", Steering, "input that reached a turn already running, and the queued-messages heading"},
	{"tool.ok", OK, "a tool call, outfit layer, login or setup step that finished well"},
	{"tool.error", Fail, "a tool call, outfit layer, login or setup step that failed"},
	{"tool.running", Frames, "the frames of the spinner, one per tick, for a running tool call"},
	{"tool.pending", Pending, "a node with no settled status yet, an idle notification, a provider not signed in"},
	{"thinking", Frames, "the frames of the spinner, one per tick, while the model thinks"},
	{"busy", Busy, "work under way where a mark is wanted, such as a sign-in flow"},
	{"delta", Delta, "a change to a key in a form transition"},
	{"fork", Fork, "a fork point in a form transition"},
	{"head", Head, "the head of a form's history"},
	{"bullet", Bullet, "a list item"},
	{"dead", Dead, "a form or node that is gone"},
	{"absent", Absent, "the missing side of a transition: a key born has no old, a key removed has no new"},
	{"head.open", HeadOpen, "the turn's question header, collapsed and able to open"},
	{"head.shut", HeadShut, "the turn's question header, open and able to shut"},
	{"quote", Quote, "a quoted passage"},
	{"quote.open", QuoteOpen, "the top corner of a quote block"},
	{"quote.close", QuoteClose, "the bottom corner of a quote block"},
	{"snake.hang", SnakeHang, "where the adornment snake hangs from a block"},
	{"snake.body", SnakeBody, "the spine of the adornment snake"},
	{"snake.close", SnakeClose, "where the adornment snake ends"},
	{"snake.elbow", SnakeElbow, "where the snake joins the block's first row"},
	{"rule", RuleSingle, "the horizontal rule between transcript regions"},
	{"rule.double", RuleDouble, "the header rule once the transcript is scrolled under it"},
	{"gutter", Gutter, "the left gutter of tool output and quoted text"},
	{"newline", Newline, "a line break folded out of a one-line summary"},
	{"select", Select, "the selected row of a pit"},
	{"state.running", StateRunning, "an aria with a turn in flight"},
	{"state.here", StateHere, "the aria this shell is bound to, or the troupe selected"},
	{"state.idle", StateIdle, "an aria at rest"},
	{"queue.committing", QueueCommitting, "a queued message being committed"},
	{"queue.waiting", QueueWaiting, "a queued message waiting its turn"},
	{"session.idle", SessionIdle, "a session with no turn"},
	{"session.disconnected", SessionDisconnected, "the turn goes on at the daemon, this view is detached"},
	{"session.interrupted", SessionInterrupted, "a turn that was hung up, not failed"},
	{"alert.warn", AlertWarn, "a warning notification"},
	{"transcript.call", CallOut, "a tool call in an exported transcript"},
	{"transcript.result", ResultBack, "a tool result in an exported transcript"},
	{"transcript.result.error", ResultFail, "a failed tool result in an exported transcript"},
	{"decide.marker", DecideMarker, "the option the decision chose"},
	{"decide.sigil", DecideSigil, "the line prefix that asks for a decision"},
	{"pit.queue", PitQueue, "the queue pit"},
	{"pit.queue.mark", MarkQueue, "the selection mark in the queue, output and dropped pits"},
	{"pit.notifications", PitNotifications, "the notifications pit"},
	{"pit.notifications.mark", MarkNotifications, "the selection mark in the notifications pit"},
	{"pit.command", PitCommand, "the command pit"},
	{"pit.search", PitSearch, "the search pit"},
	{"pit.help", PitHelp, "the help pit"},
	{"pit.status", PitStatus, "the status pit"},
	{"pit.dropped", PitDropped, "the dropped-input pit"},
	{"pit.form", PitFolk, "the form pit"},
	{"pit.form.mark", MarkForm, "the selection mark in the form pit"},
	{"pit.models", PitFolk, "the models pit"},
	{"pit.models.mark", MarkModels, "the selection mark in the models pit"},
	{"pit.reaped", PitFolk, "the reaped pit"},
	{"pit.reaped.mark", MarkReaped, "the selection mark in the reaped pit"},
	{"pit.troupe", PitFolk, "the troupe pit"},
	{"pit.troupe.mark", MarkTroupe, "the selection mark in the troupe pit"},
	{"pit.confirm", PitConfirm, "the confirmation pit"},
	{"pit.completion.mark", MarkCompletion, "the selection mark in the completion rows of the command pit"},
	{"pit.decision", PitDecision, "the decision pit"},
	{"pit.login", PitKey, "the login pit"},
	{"pit.login.mark", MarkLogin, "the selection mark in the login pit"},
	{"pit.answer", PitKey, "the answer box"},
	{"voice", Voice, "spoken input: the mic toggle and a composer filling from speech"},
	{"attach", Attach, "a file or photo added to the draft, and the control that adds one"},
}

func All() []Glyph { return append([]Glyph(nil), table...) }

func Symbol(name string) (string, bool) {
	for _, g := range table {
		if g.Name == name {
			return g.Symbol, true
		}
	}
	return "", false
}
