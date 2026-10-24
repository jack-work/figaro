package cli

func listDown(t *transcript)       { t.listMotion(func() { t.pit.moveSelection(1) }, 1) }
func listUp(t *transcript)         { t.listMotion(func() { t.pit.moveSelection(-1) }, -1) }
func listHalfDown(t *transcript)   { t.listPage(func() { t.pit.halfPage(1) }, 1) }
func listHalfUp(t *transcript)     { t.listPage(func() { t.pit.halfPage(-1) }, -1) }
func listBottom(t *transcript)     { t.listMotion(t.pit.toBottom, t.index.total) }
func listTop(t *transcript)        { t.listMotion(t.pit.toTop, -t.index.total) }
func listScrollDown(t *transcript) { t.listMotion(func() { t.pit.scrollBy(1) }, 1) }

func (t *transcript) listMotion(pit func(), head int) {
	if t.headFocused() {
		t.headScroll(head)
		return
	}
	pit()
}

func (t *transcript) listPage(pit func(), head int) {
	if t.headFocused() {
		t.headPage(head)
		return
	}
	pit()
}

func listFullscreen(t *transcript) {
	if t.headFocused() {
		t.head.full = !t.head.full
		return
	}
	t.full = !t.full
	t.focused = focusPit
}

func (t *transcript) listMode() bool {
	return t.pit.open() && t.focused == focusPit && t.pit.list() != nil && !t.pit.glance()
}
