package controlplane

import "testing"

func TestDriverRegistryIsShared(t *testing.T) {
	api := New(nil, nil, "", nil)
	if api.driverRegistry() != api.driverRegistry() {
		t.Fatal("driverRegistry builds a new registry on every call")
	}
}

func BenchmarkDriverRegistry(b *testing.B) {
	api := New(nil, nil, "", nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = api.driverRegistry()
	}
}
