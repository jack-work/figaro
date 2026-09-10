package config

// EVERY ACCESSOR ON A NIL *Loaded MUST ANSWER, NOT PANIC.
//
// A *Loaded is nil in tests, in tools, and anywhere the daemon has not read a
// config yet. Individual accessors have carried "Nil-safe" in their doc
// comments for a long time, and the property was true only where somebody
// happened to write the check: of 42 accessors, 20 dereferenced l directly,
// and the one that caught it sat between two that did not.
//
// Asserting it per-method would be the same mistake at test scale, so this
// walks the METHOD SET. A new accessor is covered the moment it is declared,
// which is the only way a rule like this stays true.

import (
	"reflect"
	"testing"
)

func TestEveryLoadedAccessorIsNilSafe(t *testing.T) {
	var nilLoaded *Loaded
	rv := reflect.ValueOf(nilLoaded)
	rt := rv.Type()

	tested := 0
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		mt := m.Type

		// Build zero arguments for whatever it takes. Receiver is arg 0.
		args := make([]reflect.Value, 0, mt.NumIn()-1)
		skip := false
		for a := 1; a < mt.NumIn(); a++ {
			if mt.IsVariadic() && a == mt.NumIn()-1 {
				skip = true // variadic: nothing meaningful to pass
				break
			}
			args = append(args, reflect.Zero(mt.In(a)))
		}
		if skip {
			continue
		}

		tested++
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s panicked on a nil *Loaded: %v\n\n"+
						"Read the config through l.cfg() and the directory through "+
						"l.dir() instead of touching l.Config / l.ConfigDir directly. "+
						"The guard lives in one place so that accessors are nil-safe by "+
						"CONSTRUCTION rather than by review.", m.Name, r)
				}
			}()
			rv.Method(i).Call(args)
		}()
	}

	if tested < 30 {
		t.Fatalf("only %d accessors were exercised; this test is meant to walk the whole "+
			"method set and something has stopped it from finding them", tested)
	}
	t.Logf("%d accessors exercised on a nil *Loaded", tested)
}
