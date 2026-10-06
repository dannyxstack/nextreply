package config

import "testing"

func TestModelFor(t *testing.T) {
	t.Setenv("TOKEN_SECRET", "x")
	t.Setenv("DEV_MODE", "true")
	t.Setenv("MODEL", "claude-opus-5-5")
	t.Setenv("EFFORT", "low")

	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	// 默认：免费用户用便宜模型，付费用户用 MODEL
	for plan, want := range map[string]string{"trial": "claude-sonnet-5", "free": "claude-sonnet-5", "pro": "claude-opus-5-5", "pro_plus": "claude-opus-5-5"} {
		if got := c.ModelFor(plan); got.Model != want || got.Effort != "low" {
			t.Fatalf("%s: %+v", plan, got)
		}
	}

	t.Setenv("MODEL_FREE", "claude-haiku-4-5")
	t.Setenv("MODEL_TRIAL", "claude-sonnet-5")
	t.Setenv("EFFORT_PRO_PLUS", "medium")
	c, _ = FromEnv()
	if c.ModelFor("trial").Model != "claude-sonnet-5" || c.ModelFor("free").Model != "claude-haiku-4-5" {
		t.Fatal("per-plan override", c.PlanModels)
	}
	if c.ModelFor("pro_plus").Effort != "medium" || c.ModelFor("pro").Effort != "low" {
		t.Fatal("effort override", c.PlanModels)
	}
}
