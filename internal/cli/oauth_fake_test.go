package cli

type fakeOAuth struct {
	names   []string
	deleted []string
}

func (f *fakeOAuth) OAuthList() ([]string, error) { return f.names, nil }

func (f *fakeOAuth) OAuthDelete(name string) error {
	f.deleted = append(f.deleted, name)
	kept := f.names[:0]
	for _, n := range f.names {
		if n != name {
			kept = append(kept, n)
		}
	}
	f.names = kept
	return nil
}
