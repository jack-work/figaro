package cli

func listDown(t *transcript)       { t.pit.moveSelection(1) }
func listUp(t *transcript)         { t.pit.moveSelection(-1) }
func listHalfDown(t *transcript)   { t.pit.halfPage(1) }
func listHalfUp(t *transcript)     { t.pit.halfPage(-1) }
func listBottom(t *transcript)     { t.pit.toBottom() }
func listTop(t *transcript)        { t.pit.toTop() }
func listScrollDown(t *transcript) { t.pit.scrollBy(1) }

func listFullscreen(t *transcript) {
	t.full = !t.full
	t.focused = focusPit
}

func (t *transcript) listMode() bool {
	return t.pit.open() && t.focused == focusPit && t.pit.list() != nil && !t.pit.glance()
}
