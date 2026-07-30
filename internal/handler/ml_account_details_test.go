package handler

import (
	"testing"

	"github.com/sign3labs/go-you/internal/model"
)

// TestMLPayloadAccountDetailsIsMap verifies that the ml_service payload sends
// account_details as an OBJECT keyed by website, NOT the list form go-you carries
// in the model. ml_service does account_details.get('SKYPE'); if it receives a
// list it dies with "'list' object has no attribute 'get'" and every
// FEATURE_ENGINE feature errors. This is the fix for that: keyAccountDetailsForML
// runs over the stripped payload before the ml call.
func TestMLPayloadAccountDetailsIsMap(t *testing.T) {
	resp := &model.PersonaResponse{
		RequestID: "t1",
		PhoneData: &model.Section{
			Type:       "phone",
			Key:        "+916265257963",
			StatusCode: sectionStatusSuccess,
			Status:     statusOK,
			PrimaryData: &model.PrimaryData{
				AccountDetails: []model.AccountDetails{
					{Website: "FLIPKART", UserExist: bp(true)},
					{Website: "SKYPE", UserExist: bp(false)},
				},
			},
		},
		EmailData: &model.Section{
			Type:       "email",
			Key:        "a@b.com",
			StatusCode: sectionStatusSuccess,
			Status:     statusOK,
			PrimaryData: &model.PrimaryData{
				AccountDetails: []model.AccountDetails{
					{Website: "GITHUB", UserExist: bp(true)},
				},
			},
		},
	}

	// Build the ml payload the same way applyIntelligence does.
	payload := toStrippedMap(resp)
	keyAccountDetailsForML(payload)

	for _, tc := range []struct {
		sec  string
		site string
	}{
		{"phone_data", "FLIPKART"},
		{"phone_data", "SKYPE"},
		{"email_data", "GITHUB"},
	} {
		s, ok := payload[tc.sec].(map[string]any)
		if !ok {
			t.Fatalf("%s missing from payload", tc.sec)
		}
		pd, ok := s["primary_data"].(map[string]any)
		if !ok {
			t.Fatalf("%s.primary_data missing", tc.sec)
		}
		ad, ok := pd["account_details"].(map[string]any)
		if !ok {
			t.Fatalf("%s.account_details is %T, want map keyed by website (ml_service .get(SITE) needs a dict)", tc.sec, pd["account_details"])
		}
		entry, ok := ad[tc.site].(map[string]any)
		if !ok {
			t.Fatalf("%s.account_details[%q] missing or wrong type", tc.sec, tc.site)
		}
		if _, leaked := entry["_website"]; leaked {
			t.Errorf("%s.account_details[%q] still carries _website hint — should be the map key only", tc.sec, tc.site)
		}
		if _, ok := entry["user_exist"]; !ok {
			t.Errorf("%s.account_details[%q] lost user_exist", tc.sec, tc.site)
		}
	}
}
