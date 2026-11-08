package navstack

const Bound = 50

type History struct {
	IDs []string `json:"ids"`
	Pos int      `json:"pos"`
}

type Dropped struct {
	ID  string `json:"id"`
	Why string `json:"why"`
}

func (h History) Current() string {
	if h.Pos < 0 || h.Pos >= len(h.IDs) {
		return ""
	}
	return h.IDs[h.Pos]
}

func (h History) Where() (int, int) {
	if len(h.IDs) == 0 {
		return 0, 0
	}
	return h.Pos + 1, len(h.IDs)
}

func (h *History) Push(id string) {
	if id == "" || id == h.Current() {
		return
	}
	if len(h.IDs) > 0 {
		h.IDs = h.IDs[:h.Pos+1]
	}
	h.IDs = append(h.IDs, id)
	if over := len(h.IDs) - Bound; over > 0 {
		h.IDs = append([]string(nil), h.IDs[over:]...)
	}
	h.Pos = len(h.IDs) - 1
}

func (h *History) Hop(steps int, home bool, fate func(id string) string) (string, []Dropped, bool) {
	if steps == 0 || len(h.IDs) == 0 {
		return "", nil, false
	}
	dir, n := 1, steps
	if steps < 0 {
		dir, n = -1, -steps
	}
	at, here := h.Pos, h.Current()
	if home {
		here = ""
	}
	var dropped []Dropped
	moved := false
	for ; n > 0; n-- {
		next := at + dir
		if home && dir < 0 && !moved {
			next = at
		}
		landed := false
		for next >= 0 && next < len(h.IDs) {
			id := h.IDs[next]
			if why := fate(id); why != "" {
				dropped = append(dropped, Dropped{ID: id, Why: why})
				h.IDs = append(h.IDs[:next], h.IDs[next+1:]...)
				if next <= at {
					at--
				}
				if dir < 0 {
					next--
				}
				continue
			}
			if id == here {
				next += dir
				continue
			}
			at, here, landed = next, id, true
			break
		}
		if !landed {
			break
		}
		moved = true
	}
	if len(h.IDs) == 0 {
		h.Pos = 0
		return "", dropped, false
	}
	h.Pos = min(max(at, 0), len(h.IDs)-1)
	if !moved {
		return "", dropped, false
	}
	return h.IDs[h.Pos], dropped, true
}
