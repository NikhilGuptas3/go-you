package model

import (
	"encoding/json"
	"testing"
)

func ptrBool(b bool) *bool { return &b }

func TestAccountDetailsMarshal(t *testing.T) {
	tests := []struct {
		name string
		in   AccountDetails
		want map[string]any
	}{
		{
			name: "simple user_exist true",
			in:   AccountDetails{Website: "FLIPKART", UserExist: ptrBool(true)},
			want: map[string]any{"user_exist": true},
		},
		{
			name: "simple user_exist false",
			in:   AccountDetails{Website: "IRCTC", UserExist: ptrBool(false)},
			want: map[string]any{"user_exist": false},
		},
		{
			name: "error entry",
			in:   AccountDetails{Website: "AMAZON", ErrorMsg: "timed out"},
			want: map[string]any{"error_msg": "timed out"},
		},
		{
			name: "rich data flattened alongside user_exist",
			in: AccountDetails{
				Website:   "TELEGRAM",
				UserExist: ptrBool(true),
				Data:      map[string]any{"username": "sachit", "handle": "@sachit"},
			},
			want: map[string]any{"user_exist": true, "username": "sachit", "handle": "@sachit"},
		},
		{
			name: "website is never serialized into the entry",
			in:   AccountDetails{Website: "SPOTIFY", UserExist: ptrBool(true)},
			want: map[string]any{"user_exist": true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			// _website is a transform-only hint carrying the site name; the
			// handler's transform deletes it before the client sees it. Verify
			// it matches the input, then drop it for the field comparison.
			if tc.in.Website != "" {
				if got["_website"] != tc.in.Website {
					t.Errorf("_website hint = %v want %v", got["_website"], tc.in.Website)
				}
				delete(got, "_website")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("key count: got %v want %v", got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("key %q: got %v want %v", k, got[k], want)
				}
			}
			if _, ok := got["website"]; ok {
				t.Errorf("website leaked into entry: %v", got)
			}
		})
	}
}
