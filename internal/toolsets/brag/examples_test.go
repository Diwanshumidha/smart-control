package brag

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// examples/plan.json is documentation; keep it a plan submit_plan accepts.
func TestExamplePlanIsValid(t *testing.T) {
	_, a, _ := setup(t)
	b, err := os.ReadFile("../../../examples/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var p planIn
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if errs, _ := a.validatePlan(context.Background(), &p); len(errs) > 0 {
		t.Fatalf("examples/plan.json no longer passes submit_plan:\n%v", errs)
	}
}
