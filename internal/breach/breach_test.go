package breach

import "testing"

func TestPhoneAlwaysEmpty(t *testing.T) {
	s := NewService(nil, 0)
	b := s.Phone("+919876543210")
	if b.Phone != "+919876543210" || b.BreachesStatus != "not-found" || len(b.Breaches) != 0 {
		t.Errorf("phone breach = %+v, want empty not-found", b)
	}
	// Must be an empty slice, not nil, so it serializes as [] not null.
	if b.Breaches == nil {
		t.Error("Breaches should be an empty slice, not nil")
	}
}

type stubCfg struct{ m map[string]any }

func (s stubCfg) Get(k string, def any) any {
	if v, ok := s.m[k]; ok {
		return v
	}
	return def
}

func TestEmailDisabledWhenKillSwitchOff(t *testing.T) {
	// haveibeenpwned.enabled false => Email returns nil (no block attached).
	s := NewService(stubCfg{m: map[string]any{
		"tpi_global_config": map[string]any{
			"haveibeenpwned": map[string]any{"enabled": false, "api_key": "k"},
		},
	}}, 0)
	if got := s.Email(nil, "a@b.com"); got != nil {
		t.Errorf("expected nil when kill switch off, got %+v", got)
	}
}

func TestEmailDisabledWhenNoConfig(t *testing.T) {
	s := NewService(nil, 0)
	if got := s.Email(nil, "a@b.com"); got != nil {
		t.Errorf("expected nil with no config, got %+v", got)
	}
}
