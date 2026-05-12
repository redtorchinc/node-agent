package services

import (
	"errors"
	"testing"

	"github.com/redtorchinc/node-agent/internal/config"
)

func TestValidate_AllowlistEnforced(t *testing.T) {
	cfg := config.ServicesConfig{
		Allowed: []config.ServiceAllowedEntry{
			{Name: "rt-vllm-qwen3.service", Actions: []string{"start", "restart", "status"}},
		},
	}

	cases := []struct {
		unit   string
		action Action
		want   error
	}{
		{"rt-vllm-qwen3.service", ActionStart, nil},
		{"rt-vllm-qwen3.service", ActionStop, ErrActionNotAllowed},
		{"rt-vllm-qwen3.service", "kill", ErrUnknownAction},
		{"sshd.service", ActionStart, ErrUnitNotAllowed},
		{"docker.service", ActionStart, ErrUnitNotAllowed},
		{"", ActionStart, ErrUnitNotAllowed},
		{"rt-vllm-qwen3.service; rm -rf /", ActionStart, ErrUnitNotAllowed},
	}
	for _, tc := range cases {
		_, err := validate(cfg, tc.unit, tc.action)
		if tc.want == nil && err != nil {
			t.Errorf("unit=%q action=%q: unexpected error %v", tc.unit, tc.action, err)
			continue
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("unit=%q action=%q: want %v, got %v", tc.unit, tc.action, tc.want, err)
		}
	}
}

func TestValidate_EmptyActionListMeansAll(t *testing.T) {
	cfg := config.ServicesConfig{
		Allowed: []config.ServiceAllowedEntry{{Name: "rt-vllm-x.service"}}, // no Actions = all
	}
	for _, a := range AllActions {
		if _, err := validate(cfg, "rt-vllm-x.service", a); err != nil {
			t.Errorf("action %s should be allowed when Actions list is empty, got %v", a, err)
		}
	}
}

func TestFromConfig_NilWhenEmptyAllowlist(t *testing.T) {
	if m := FromConfig(config.ServicesConfig{}); m != nil {
		t.Errorf("expected nil manager when allowlist is empty, got %T", m)
	}
}
