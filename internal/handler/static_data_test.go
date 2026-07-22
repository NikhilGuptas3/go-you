package handler

import (
	"testing"

	"github.com/sign3labs/go-you/internal/model"
)

// TestStaticDataInMLPayloadNotClient verifies the two-sided contract for the
// static_data lane:
//   - toStrippedMap(resp) (the ml_service YOU.analytic_response) RETAINS
//     phone_data.static_data / email_data.static_data, so ml_service can read it.
//   - transformResponse (the client response) STRIPS static_data from both
//     sections (port of remove_static_data).
func TestStaticDataInMLPayloadNotClient(t *testing.T) {
	staticDoc := map[string]any{
		"vedantu": []any{
			map[string]any{"payload": map[string]any{"primary_ph": "916265257963", "name": "X"}},
		},
	}
	resp := &model.PersonaResponse{
		RequestID: "t1",
		PhoneData: &model.Section{
			Type:       "phone",
			Key:        "+916265257963",
			StatusCode: sectionStatusSuccess,
			Status:     statusOK,
			PrimaryData: &model.PrimaryData{
				AccountDetails: []model.AccountDetails{{Website: "FLIPKART", UserExist: bp(true)}},
			},
			StaticData: staticDoc,
		},
		EmailData: &model.Section{
			Type:       "email",
			Key:        "a@b.com",
			StatusCode: sectionStatusSuccess,
			Status:     statusOK,
			PrimaryData: &model.PrimaryData{
				AccountDetails: []model.AccountDetails{{Website: "SPOTIFY", UserExist: bp(true)}},
			},
			StaticData: staticDoc,
		},
	}

	// ml_service payload path: static_data must be present.
	payload := toStrippedMap(resp)
	for _, sec := range []string{"phone_data", "email_data"} {
		s, ok := payload[sec].(map[string]any)
		if !ok {
			t.Fatalf("ml payload missing %s", sec)
		}
		if _, ok := s["static_data"]; !ok {
			t.Errorf("ml payload %s.static_data missing — ml_service can't read it", sec)
		}
	}

	// client response path: static_data must be stripped from both sections.
	out := transformResponse(resp, nil, false, nil)
	for _, sec := range []string{"phone_data", "email_data"} {
		s, ok := out[sec].(map[string]any)
		if !ok {
			continue // section may be reshaped away; absence is fine for this assertion
		}
		if _, leaked := s["static_data"]; leaked {
			t.Errorf("client response %s.static_data leaked — remove_static_data failed", sec)
		}
	}
}
