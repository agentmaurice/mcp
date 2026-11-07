package typsttest

import "testing"

func TestRequiredInCIOrByFlag(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("ARTIFACT_REQUIRE_TYPST", "")
	if Required() {
		t.Fatal("must not be required locally")
	}
	t.Setenv("CI", "true")
	if !Required() {
		t.Fatal("CI must require Typst")
	}
	t.Setenv("CI", "")
	t.Setenv("ARTIFACT_REQUIRE_TYPST", "1")
	if !Required() {
		t.Fatal("ARTIFACT_REQUIRE_TYPST=1 must require Typst")
	}
}
