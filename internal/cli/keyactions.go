package cli

// The action registry: the public vocabulary a keys.toml names.
//
// The names here are NOT the Go identifiers. A Go rename touches one line of
// this table and no user's file; a rename of a NAME is a breaking change, so
// the two are kept apart on purpose.
//
// An entry may carry both levels. Where it does, the input implementation is
// the one a user binding installs (it runs on the read loop and can read
// modifiers), and the pager one is there so the default table's rows still
// resolve to a name.

type keyLevel uint8

const (
	levelPager keyLevel = iota
	levelInput
)

type keyAction struct {
	name  string
	pager pagerFunc
	input inputFunc
}

// level is the one a user binding installs.
func (a keyAction) level() keyLevel {
	if a.input != nil {
		return levelInput
	}
	return levelPager
}

var keyActions = []keyAction{
	{name: "composer-bottom", pager: composerBottom},
	{name: "composer-command", pager: composerCommand},
	{name: "composer-down-line", pager: composerDownLine},
	{name: "composer-escape", pager: composerEscape},
	{name: "composer-find-next", pager: composerFindNext},
	{name: "composer-find-prev", pager: composerFindPrev},
	{name: "composer-first-text", pager: composerFirstText},
	{name: "composer-fullscreen", pager: composerFullscreen},
	{name: "composer-insert-after", pager: composerInsertAfter},
	{name: "composer-insert-end", pager: composerInsertEnd},
	{name: "composer-insert-here", pager: composerInsertHere},
	{name: "composer-insert-home", pager: composerInsertHome},
	{name: "composer-left", pager: composerLeft},
	{name: "composer-line-end", pager: composerLineEnd},
	{name: "composer-line-start", pager: composerLineStart},
	{name: "composer-mark-char", pager: composerMarkChar},
	{name: "composer-mark-line", pager: composerMarkLine},
	{name: "composer-para-next", pager: composerParaNext},
	{name: "composer-para-prev", pager: composerParaPrev},
	{name: "composer-right", pager: composerRight},
	{name: "composer-search-back", pager: composerSearchBack},
	{name: "composer-search-fwd", pager: composerSearchFwd},
	{name: "composer-submit", pager: composerSubmit},
	{name: "composer-top", pager: composerTop},
	{name: "composer-up-line", pager: composerUpLine},
	{name: "composer-word-back", pager: composerWordBack},
	{name: "composer-word-end", pager: composerWordEnd},
	{name: "composer-word-fwd", pager: composerWordFwd},
	{name: "comment-cancel", pager: pagerCommentCancel},
	{name: "comment-next", pager: pagerCommentNext},
	{name: "comment-pending", pager: pagerCommentPending},
	{name: "comment-prev", pager: pagerCommentPrev},
	{name: "composer-box", pager: pagerComposerBox},
	{name: "decision-pit", pager: pagerDecisionPit},
	{name: "delete-comment", pager: pagerDeleteComment},
	{name: "find-next-visual", pager: pagerFindNextVisual},
	{name: "find-prev-visual", pager: pagerFindPrevVisual},
	{name: "find-row-next", pager: pagerFindRowNext},
	{name: "find-row-prev", pager: pagerFindRowPrev},
	{name: "leader", pager: pagerLeader},
	{name: "pit-activate", pager: pagerPitActivate},
	{name: "visual-attend", pager: pagerVisualAttend},
	{name: "visual-composer", pager: pagerVisualComposer},
	{name: "visual-select", pager: pagerVisualSelect},
	{name: "aria-back", pager: pagerAriaBack},
	{name: "aria-forward", pager: pagerAriaForward},
	{name: "attend-fork", pager: pagerAttendFork},
	{name: "attend-parent", pager: pagerAttendParent},
	{name: "attend-row", pager: pagerAttendRow},
	{name: "clear-selection", pager: pagerClearSelection},
	{name: "cmd-abort", pager: cmdAbort},
	{name: "cmd-accept", pager: boxAccept},
	{name: "cmd-accept-snap", pager: boxAcceptSnap},
	{name: "cmd-backspace", pager: boxBackspace},
	{name: "cmd-cancel", pager: boxCancel},
	{name: "cmd-capitalize", pager: cmdCapitalize},
	{name: "cmd-complete", pager: cmdComplete},
	{name: "cmd-delete-fwd", pager: cmdDeleteFwd},
	{name: "cmd-delete-space", pager: cmdDeleteSpace},
	{name: "cmd-downcase", pager: cmdDowncase},
	{name: "cmd-end", pager: cmdEnd},
	{name: "cmd-hist-first", pager: cmdHistFirst},
	{name: "cmd-hist-last", pager: cmdHistLast},
	{name: "cmd-hist-next", pager: cmdHistNext},
	{name: "cmd-hist-prev", pager: cmdHistPrev},
	{name: "cmd-home", pager: cmdHome},
	{name: "cmd-insert-complete", pager: cmdInsertComplete},
	{name: "cmd-kill-to-end", pager: cmdKillToEnd},
	{name: "cmd-kill-to-start", pager: cmdKillToStart},
	{name: "cmd-kill-word", pager: cmdKillWord},
	{name: "cmd-kill-word-alpha", pager: cmdKillWordAlpha},
	{name: "cmd-kill-word-fwd", pager: cmdKillWordFwd},
	{name: "cmd-left", pager: cmdLeft},
	{name: "cmd-list-complete", pager: cmdListComplete},
	{name: "cmd-paste", pager: cmdPaste},
	{name: "cmd-prefix-next", pager: cmdPrefixNext},
	{name: "cmd-prefix-prev", pager: cmdPrefixPrev},
	{name: "cmd-redraw", pager: cmdRedraw},
	{name: "cmd-revert", pager: cmdRevert},
	{name: "cmd-right", pager: cmdRight},
	{name: "cmd-search-next", pager: cmdSearchNext},
	{name: "cmd-search-prev", pager: cmdSearchPrev},
	{name: "cmd-transpose", pager: cmdTranspose},
	{name: "cmd-transpose-word", pager: cmdTransposeWord},
	{name: "cmd-undo", pager: cmdUndo},
	{name: "cmd-upcase", pager: cmdUpcase},
	{name: "cmd-word-left", pager: cmdWordLeft},
	{name: "cmd-word-right", pager: cmdWordRight},
	{name: "cmd-yank", pager: cmdYank},
	{name: "cmd-yank-last-arg", pager: cmdYankLastArg},
	{name: "cmd-yank-pop", pager: cmdYankPop},
	{name: "confirm-no", pager: confirmNo},
	{name: "confirm-yes", pager: confirmYes},
	{name: "disconnect", input: inputDisconnect},
	{name: "enter-transcript", input: inputEnterTranscript},
	{name: "find-next", pager: pagerFindNext},
	{name: "find-prev", pager: pagerFindPrev},
	{name: "focus-transcript", pager: pagerFocusTranscript},
	{name: "fork-cancel", pager: pagerForkCancel},
	{name: "fork-next", pager: pagerForkNext},
	{name: "fork-pending", pager: pagerForkPending},
	{name: "fork-prev", pager: pagerForkPrev},
	{name: "form-pit", pager: pagerFormPit},
	{name: "half-down", pager: pagerHalfDown},
	{name: "half-up", pager: pagerHalfUp},
	{name: "hang-up", input: inputHangUp},
	{name: "hang-up-drop", input: inputHangUpDrop},
	{name: "help-panel", pager: pagerHelpPanel},
	{name: "interrupt", input: inputInterrupt},
	{name: "jump-prompt", pager: pagerCommandBox},
	{name: "leave-pit", input: inputLeavePit},
	{name: "line-down", pager: pagerLineDown},
	{name: "line-up", pager: pagerLineUp},
	{name: "list-home", pager: pagerListHome},
	{name: "list-pit", pager: pagerListPit},
	{name: "panel-dismiss", pager: panelDismiss},
	{name: "panel-toggle-help", pager: panelToggleHelp},
	{name: "panel-toggle-queued", pager: panelToggleQueued},
	{name: "panel-toggle-status", pager: panelToggleStatus},
	{name: "pending-top", pager: pagerPendingTop},
	{name: "pit-drop", pager: pagerPitDrop},
	{name: "pit-drop-now", pager: pagerPitDropNow},
	{name: "question-next", input: inputQuestionNext},
	{name: "question-prev", input: inputQuestionPrev},
	{name: "queued-panel", pager: pagerQueuedPanel},
	{name: "search-accept", pager: searchAccept},
	{name: "search-backspace", pager: searchBackspace},
	{name: "search-cancel", pager: searchCancel},
	{name: "search-prompt", pager: pagerSearchPrompt},
	{name: "search-prompt-back", pager: pagerSearchPromptBack},
	{name: "select-next", pager: pagerSelectNext, input: inputSelectNext},
	{name: "select-prev", pager: pagerSelectPrev, input: inputSelectPrev},
	{name: "status-panel", pager: pagerStatusPanel},
	{name: "tail", pager: pagerTail},
	{name: "toggle-bar-verbose", input: inputToggleBarVerbose},
	{name: "toggle-blocks", pager: pagerToggleBlocks},
	{name: "toggle-bodies", pager: pagerToggleBodies},
	{name: "toggle-sticky", input: inputToggleSticky},
	{name: "toggle-verbose", input: inputToggleVerbose},
	{name: "top", pager: pagerTop},
	{name: "visual-char", pager: pagerVisualChar},
	{name: "visual-command", pager: pagerVisualCommand},
	{name: "visual-down", pager: pagerVisualDown},
	{name: "visual-end", pager: pagerVisualEnd},
	{name: "visual-first-text", pager: pagerVisualFirstText},
	{name: "visual-half-down", pager: pagerVisualHalfDown},
	{name: "visual-half-up", pager: pagerVisualHalfUp},
	{name: "visual-leave", pager: pagerVisualLeave},
	{name: "visual-left", pager: pagerVisualLeft},
	{name: "visual-line", pager: pagerVisualLine},
	{name: "visual-para-next", pager: pagerVisualParaNext},
	{name: "visual-para-prev", pager: pagerVisualParaPrev},
	{name: "visual-pending-top", pager: pagerVisualPendingTop},
	{name: "visual-right", pager: pagerVisualRight},
	{name: "visual-row-end", pager: pagerVisualRowEnd},
	{name: "visual-row-start", pager: pagerVisualRowStart},
	{name: "visual-screen-bot", pager: pagerVisualScreenBot},
	{name: "visual-screen-mid", pager: pagerVisualScreenMid},
	{name: "visual-screen-top", pager: pagerVisualScreenTop},
	{name: "visual-top", pager: pagerVisualTop},
	{name: "visual-up", pager: pagerVisualUp},
	{name: "visual-word-end", pager: pagerVisualWordEnd},
	{name: "visual-word-next", pager: pagerVisualWordNext},
	{name: "visual-word-prev", pager: pagerVisualWordPrev},
	{name: "yank", input: inputYank},
	{name: "yank-coordinate", input: inputYankCoordinate},
}

var actionByName = func() map[string]*keyAction {
	m := make(map[string]*keyAction, len(keyActions))
	for i := range keyActions {
		m[keyActions[i].name] = &keyActions[i]
	}
	return m
}()
