package main

import "testing"

func TestChooseDefaultDB(t *testing.T) {
	if got := chooseDefaultDB(false); got != "/config/distillarr.db" {
		t.Errorf("fresh install: got %q, want distillarr.db", got)
	}
	if got := chooseDefaultDB(true); got != "/config/mediatrans.db" {
		t.Errorf("existing mediatrans.db: got %q, want the legacy path kept", got)
	}
}

func TestEnvOrLegacy(t *testing.T) {
	const newKey, legacyKey = "DISTILLARR_TEST_VAR", "MEDIIATRANS_TEST_VAR"
	t.Setenv(newKey, "")
	t.Setenv(legacyKey, "")

	if got := envOrLegacy(newKey, legacyKey, "default"); got != "default" {
		t.Errorf("neither set: got %q, want default", got)
	}

	t.Setenv(legacyKey, "legacy-value")
	if got := envOrLegacy(newKey, legacyKey, "default"); got != "legacy-value" {
		t.Errorf("legacy only: got %q, want legacy-value", got)
	}

	t.Setenv(newKey, "new-value")
	if got := envOrLegacy(newKey, legacyKey, "default"); got != "new-value" {
		t.Errorf("both set: got %q, want the new name to win", got)
	}
}
