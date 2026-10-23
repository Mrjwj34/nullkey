package domain

import "testing"

func TestSentinels(t *testing.T) {
	for _, err := range []error{ErrStateLocked, ErrNotFound, ErrUnauthorized, ErrConflict} {
		if err.Error() == "" {
			t.Fatalf("sentinel %T has an empty message", err)
		}
	}
}

func TestEnumsAreStable(t *testing.T) {
	if SandboxRunning != "running" || JobCancelling != "cancelling" || OAuthCancelled != "cancelled" {
		t.Fatal("status enum drifted")
	}
	if NetworkServicesOnly != "services_only" || NetworkFiltered != "filtered" || NetworkOpen != "open" {
		t.Fatal("network mode enum drifted")
	}
	if StorageManaged != "managed" || StorageExternal != "external" {
		t.Fatal("storage kind drifted")
	}
}
