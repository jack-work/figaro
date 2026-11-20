package rpc

import (
	"reflect"
	"testing"
)

// review-lane-usability-1 #2: a reroute rode SetRequest, and a peer that
// predates it dropped the flag and folded the whole outfit. It is its own
// method, which an old daemon refuses by name.
func TestRerouteIsAMethodNotAFlagOnSet(t *testing.T) {
	if _, ok := reflect.TypeOf(SetRequest{}).FieldByName("Reroute"); ok {
		t.Fatal("SetRequest carries a reroute flag an old daemon would drop")
	}
	if verb, _ := VerbOf(MethodReroute); verb != VerbSet {
		t.Fatalf("%s is authorized as %q, want set", MethodReroute, verb)
	}
}
