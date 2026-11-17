package store

func heldPatches(ps []VersionedPatch, err error) []VersionedPatch {
	if err != nil {
		panic(err)
	}
	return ps
}
