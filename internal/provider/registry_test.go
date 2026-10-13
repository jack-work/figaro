package provider

import (
	"reflect"
	"testing"
)

// The resolver, the exchange hand-off and the setup hint all ask this one
// method; they used to each carry the fallback, and the hint's copy dropped
// the plural form.
func TestCredentialEnvs(t *testing.T) {
	cases := []struct {
		reg  *Registration
		want []string
	}{
		{&Registration{EnvVar: "ONE"}, []string{"ONE"}},
		{&Registration{EnvVars: []string{"A", "B"}}, []string{"A", "B"}},
		{&Registration{EnvVar: "ONE", EnvVars: []string{"A"}}, []string{"A"}},
		{&Registration{}, nil},
		{nil, nil},
	}
	for _, c := range cases {
		if got := c.reg.CredentialEnvs(); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%#v: got %#v, want %#v", c.reg, got, c.want)
		}
	}
}
