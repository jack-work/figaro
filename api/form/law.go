package form

const LawRoot = "law"

const LawRepealed = "(repealed)"

func IsLaw(path string) bool { return under(path, LawRoot) }
