package main

import "testing"

func TestMyGoLabelFollowsModuleVersion(t *testing.T) {
	if got := mygoLabel(); got != "MyGo 0.6.2" {
		t.Fatalf("label %q does not match go.mod", got)
	}
}
