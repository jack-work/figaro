package store

import (
	"strings"
	"testing"
)

// doctor mem has to see the libretto copy and the subscription it pins on its
// source, or the heap a studied form holds is invisible.
func TestFormResidencyCountsLibrettosAndTheSourcesTheyPin(t *testing.T) {
	be, sourceID, src := librettoFixture(t)
	big := strings.Repeat("x", 8192)
	if _, err := src.Apply(setPatch(map[string]string{"skills.a": big}), 0); err != nil {
		t.Fatal(err)
	}
	before := be.FormResidency()
	if before.Forms == 0 || before.StateBytes < 8192 || before.PatchBytes < 8192 {
		t.Fatalf("a resident form with an 8 KB value: %+v", before)
	}
	if before.SubscribedForms != 0 || before.LibrettoForms != 0 {
		t.Fatalf("nothing studies the source yet: %+v", before)
	}

	lib, err := be.Libretto(sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := be.attach(lib, sourceID); err != nil {
		t.Fatal(err)
	}
	after := be.FormResidency()
	if after.LibrettoForms != 1 || after.SubscribedForms != 1 {
		t.Fatalf("a followed libretto: %+v", after)
	}
	if after.LibrettoBytes < 8192 || after.StateBytes < before.StateBytes+8192 {
		t.Fatalf("the libretto's copy of the 8 KB value is not counted: before %+v, after %+v", before, after)
	}
}
