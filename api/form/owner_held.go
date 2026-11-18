package form

func OwnerHeldKeys() []string { return RootsOf(WriterOwner) }

func OwnerHeld(path string) (string, bool) { return rootOf(WriterOwner, path) }
