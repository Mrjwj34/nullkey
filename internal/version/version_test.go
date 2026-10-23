package version

import "testing"

func TestStringAndIsDev(t *testing.T) {
	oldVersion, oldCommit := Version, Commit
	t.Cleanup(func() { Version, Commit = oldVersion, oldCommit })
	Version = "1.2.3"
	Commit = "deadbeef"
	if got := String(); got != "1.2.3 (deadbeef)" {
		t.Fatalf("String = %q", got)
	}
	if IsDev() {
		t.Fatal("tagged build reported as dev")
	}
	Commit = "unknown"
	if got := String(); got != "1.2.3" {
		t.Fatalf("String = %q", got)
	}
	if !IsDev() {
		t.Fatal("unknown commit should be dev")
	}
	Version = "1.2.3-dev"
	Commit = "deadbeef"
	if !IsDev() {
		t.Fatal("dev version not detected")
	}
}
