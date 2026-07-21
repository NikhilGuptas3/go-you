package breach

import "testing"

func TestPhoneEmptyWhenNoStatic(t *testing.T) {
	s := NewService(nil, 0)
	b := s.Phone("+919876543210", "919876543210", nil)
	if b.Phone != "+919876543210" || b.BreachesStatus != "not-found" || len(b.Breaches) != 0 {
		t.Errorf("phone breach = %+v, want empty not-found", b)
	}
	// Must be an empty slice, not nil, so it serializes as [] not null.
	if b.Breaches == nil {
		t.Error("Breaches should be an empty slice, not nil")
	}
}

// TestPhoneBreachFromStatic: a matching static payload with mapped leaked
// attributes yields a found breach with the correct name/data/date.
func TestPhoneBreachFromStatic(t *testing.T) {
	s := NewService(stubCfg{m: map[string]any{
		"breach_date_per_source": map[string]any{
			// keys are source-lower; the mapped breach name is "Truecaller"
			"truecaller": "2019-05",
		},
	}}, 0)
	static := map[string]any{
		// raw source key -> source_mapping["truecaller_delhi"] == "Truecaller"
		"truecaller_delhi": []map[string]any{
			{"payload": map[string]any{
				"primary_ph": "919876543210",
				"name":       "Ravi Kumar", // -> attributes_mapping name -> "Name"
				"address":    "Delhi",      // -> "Address"
				"unmapped":   "ignored",
			}},
		},
		// a source not in source_mapping -> skipped
		"random_source": []map[string]any{
			{"payload": map[string]any{"primary_ph": "919876543210", "name": "x"}},
		},
		// a mapped source but wrong number -> skipped
		"facebook": []map[string]any{
			{"payload": map[string]any{"primary_ph": "910000000000", "name": "y"}},
		},
	}
	b := s.Phone("+919876543210", "919876543210", static)
	if b.BreachesStatus != "found" || len(b.Breaches) != 1 {
		t.Fatalf("expected 1 found breach, got %+v", b)
	}
	got := b.Breaches[0]
	if got.Name != "Truecaller" {
		t.Errorf("name = %q, want Truecaller", got.Name)
	}
	// attributes sorted: Address, Name -> "Address ,Name"
	if got.Data != "Address ,Name" {
		t.Errorf("data = %q, want %q", got.Data, "Address ,Name")
	}
	if got.Date != "2019-05" {
		t.Errorf("date = %q, want 2019-05", got.Date)
	}
}

// TestPhoneBreachNumberAsFloat: a phone stored as a JSON number matches the
// string login id (float64 -> "919876543210", no trailing .0).
func TestPhoneBreachNumberAsFloat(t *testing.T) {
	s := NewService(nil, 0)
	static := map[string]any{
		"amazon": []map[string]any{
			{"payload": map[string]any{
				"primary_ph": float64(919876543210),
				"email":      "r@x.com", // -> "Email"
			}},
		},
	}
	b := s.Phone("+919876543210", "919876543210", static)
	if b.BreachesStatus != "found" || len(b.Breaches) != 1 || b.Breaches[0].Name != "Amazon" {
		t.Fatalf("expected found Amazon breach, got %+v", b)
	}
	if b.Breaches[0].Date != "NA" {
		t.Errorf("date = %q, want NA (no breach_date config)", b.Breaches[0].Date)
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
