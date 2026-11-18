//go:build !linux

package toolcgroup

import "errors"

var errUnsupported = errors.New("toolcgroup: tool cgroups need Linux")

type Tree struct{}

func Own(*Registry) (*Tree, error) { return nil, errUnsupported }

func (t *Tree) Root() string { return "" }

func (t *Tree) FD(string) (int, error) { return -1, errUnsupported }

func ProcCgroupOf(int) (string, error) { return "", errUnsupported }
