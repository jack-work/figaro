package form

import "encoding/json"

const LawRoot = "law"

const LawRepealed = "(repealed)"

func IsLaw(path string) bool { return under(path, LawRoot) }

func LawsAsTheyStand(s Snapshot) Patch {
	return Merge(Build(Snapshot{}, nil, []string{LawRoot}), LawsAdded(s))
}

func LawsAdded(s Snapshot) Patch {
	raw, ok := s.Get(LawRoot)
	if !ok {
		return Patch{}
	}
	return Build(Snapshot{}, map[string]json.RawMessage{LawRoot: raw}, nil)
}

func UnderLawsOf(s Snapshot, p Patch) Patch {
	for _, e := range p.Entries() {
		if IsLaw(e.Key) {
			return Merge(p, LawsAdded(s))
		}
	}
	return p
}
