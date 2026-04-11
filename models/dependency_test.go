package models

import "testing"

func TestDependencyRule_Validate(t *testing.T) {
	cases := []struct {
		name    string
		rule    DependencyRule
		wantErr bool
	}{
		{"empty pipeline id", DependencyRule{}, true},
		{"defaults", DependencyRule{PipelineID: "p1"}, false},
		{"valid succeeded gate", DependencyRule{PipelineID: "p1", State: DepStateSucceeded, Mode: DepModeGate}, false},
		{"valid trigger completed", DependencyRule{PipelineID: "p1", State: DepStateCompleted, Mode: DepModeTrigger}, false},
		{"invalid state", DependencyRule{PipelineID: "p1", State: "bogus"}, true},
		{"invalid mode", DependencyRule{PipelineID: "p1", Mode: "bogus"}, true},
		{"negative window", DependencyRule{PipelineID: "p1", WithinSec: -1}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.rule.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("want err=%v got %v", c.wantErr, err)
			}
		})
	}
}

func TestDependencyRule_Normalize(t *testing.T) {
	r := DependencyRule{PipelineID: "p1"}
	r.Normalize()
	if r.State != DepStateSucceeded {
		t.Errorf("default state = %q, want succeeded", r.State)
	}
	if r.Mode != DepModeGate {
		t.Errorf("default mode = %q, want gate", r.Mode)
	}
}

func TestPipeline_EffectiveDependencies_Merges(t *testing.T) {
	p := Pipeline{
		DependsOn:       []string{"legacy-1", "legacy-2"},
		DependencyRules: []DependencyRule{{PipelineID: "rich-1", State: DepStateCompleted, Mode: DepModeTrigger}},
	}
	out := p.EffectiveDependencies()
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	// Rich rules come first
	if out[0].PipelineID != "rich-1" || out[0].Mode != DepModeTrigger {
		t.Errorf("rich rule out of order: %+v", out[0])
	}
	// Legacy becomes gate/succeeded
	for _, r := range out[1:] {
		if r.Mode != DepModeGate || r.State != DepStateSucceeded {
			t.Errorf("legacy rule not normalized: %+v", r)
		}
	}
}

func TestPipeline_EffectiveDependencies_Dedup(t *testing.T) {
	p := Pipeline{
		DependsOn:       []string{"p1"},
		DependencyRules: []DependencyRule{{PipelineID: "p1", State: DepStateCompleted, Mode: DepModeTrigger}},
	}
	out := p.EffectiveDependencies()
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (rich rule should win)", len(out))
	}
	if out[0].Mode != DepModeTrigger {
		t.Errorf("rich rule didn't win: %+v", out[0])
	}
}

func TestPipeline_Validate_SelfDep(t *testing.T) {
	p := Pipeline{
		ID:              "p1",
		Name:            "x",
		DependencyRules: []DependencyRule{{PipelineID: "p1"}},
	}
	if err := p.Validate(); err == nil {
		t.Error("expected self-dep rejection")
	}
	p2 := Pipeline{ID: "p1", Name: "x", DependsOn: []string{"p1"}}
	if err := p2.Validate(); err == nil {
		t.Error("expected legacy self-dep rejection")
	}
}
