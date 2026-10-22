package store

type birthSpec struct {
	mintedBy string
	template bool
}

type BirthOption func(*birthSpec)

func MintedBy(principal string) BirthOption {
	return func(b *birthSpec) { b.mintedBy = principal }
}

func Template() BirthOption {
	return func(b *birthSpec) { b.template = true }
}

func births(opts []BirthOption) birthSpec {
	var b birthSpec
	for _, o := range opts {
		o(&b)
	}
	return b
}
