package workerpool

import "testing"

// The lease API is asked with the pool provider's credential, so its root
// must be where that provider already sends it.
func TestTheLeaseAPIIsAtTheProvidersOriginAlone(t *testing.T) {
	got, err := LeaseAPI("http://10.233.1.1:9110/", "http://10.233.1.1:9110/u/explabs/v1")
	if err != nil || got != "http://10.233.1.1:9110/v1" {
		t.Fatalf("LeaseAPI = %q, %v", got, err)
	}
	for _, root := range []string{"http://10.233.1.2:9110", "https://10.233.1.1:9110", "http://10.233.1.1:9111", "http://rou2.example"} {
		if _, err := LeaseAPI(root, "http://10.233.1.1:9110/u/explabs/v1"); err == nil {
			t.Errorf("a lease API at %s was taken for a provider at 10.233.1.1:9110", root)
		}
	}
}
