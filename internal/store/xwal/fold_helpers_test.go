package xwal

func perPatch(reduce func(state, patch []byte) ([]byte, error)) FoldFunc {
	return func(state []byte, patches [][]byte) ([]byte, error) {
		for _, p := range patches {
			next, err := reduce(state, p)
			if err != nil {
				return nil, err
			}
			state = next
		}
		return state, nil
	}
}
