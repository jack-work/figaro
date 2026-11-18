package toolcgroup

import "testing"

func TestACgroupBelowARegisteredOneBelongsToItsFigaro(t *testing.T) {
	r := NewRegistry()
	drop, err := r.Register("/app.slice/a.scope/tools-x", "x")
	if err != nil {
		t.Fatal(err)
	}
	for cg, want := range map[string]string{
		"/app.slice/a.scope/tools-x":          "x",
		"/app.slice/a.scope/tools-x/nested/y": "x",
		"/app.slice/a.scope/tools-xy":         "",
		"/app.slice/a.scope":                  "",
		"/":                                   "",
	} {
		if got, _ := r.Lookup(cg); got != want {
			t.Errorf("Lookup(%s) = %q, want %q", cg, got, want)
		}
	}
	if _, err := r.Register("/app.slice/a.scope/tools-x", "other"); err == nil {
		t.Error("one cgroup was registered to two figaros")
	}
	drop()
	if got, ok := r.Lookup("/app.slice/a.scope/tools-x"); ok {
		t.Errorf("an unregistered cgroup still names %q", got)
	}
}

func TestOnlyTheUnifiedHierarchyNamesACgroup(t *testing.T) {
	got, ok := ParseProcCgroup([]byte("1:name=systemd:/old\n0::/user.slice/x.scope\n"))
	if !ok || got != "/user.slice/x.scope" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := ParseProcCgroup([]byte("1:name=systemd:/old\n")); ok {
		t.Fatal("a v1-only line named a cgroup")
	}
}
