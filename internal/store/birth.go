package store

import "github.com/jack-work/figaro/api/message"

type birthSpec struct {
	mintedBy string
	atRoot   bool
	template bool
	stamp    func(child string) message.Patch
}

type BirthOption func(*birthSpec)

func MintedBy(principal string) BirthOption {
	return func(b *birthSpec) { b.mintedBy = principal }
}

func AtRoot() BirthOption {
	return func(b *birthSpec) { b.atRoot = true }
}

func Template() BirthOption {
	return func(b *birthSpec) { b.template = true }
}

func Stamped(fill func(child string) message.Patch) BirthOption {
	return func(b *birthSpec) { b.stamp = fill }
}

func births(opts []BirthOption) birthSpec {
	var b birthSpec
	for _, o := range opts {
		o(&b)
	}
	return b
}
