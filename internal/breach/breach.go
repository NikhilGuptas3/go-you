// Package breach produces breach_details, ported from the Python
// data_breach/breach_crawler.py + pawn/pawn_service.py.
//
// Email breach uses HaveIBeenPwned live (the only provider still wired in prod;
// firefox/pastebin/emailrep/breachdirectory are dead and not ported). Phone
// breach in prod is computed purely from DynamoDB static data — which go-you
// does not have — so it degrades deterministically to an empty, not-found
// result. This is a documented capability gap, not a bug.
package breach

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/sign3labs/go-you/internal/model"
)

// ConfigGetter is the subset of appconfig.Fetcher the breach lane needs
// (tpi_global_config for the HIBP enable flag + api key).
type ConfigGetter interface {
	Get(key string, def any) any
}

// Service produces breach details. proxyURL is unused for HIBP (Python calls it
// without a proxy) but retained for symmetry / future providers.
type Service struct {
	cfg     ConfigGetter
	timeout time.Duration
}

func NewService(cfg ConfigGetter, timeout time.Duration) *Service {
	return &Service{cfg: cfg, timeout: timeout}
}

// Phone returns the phone breach block. Under the no-cloud constraint there is
// no static-data source, so this is always {phone, breaches:[], not-found} —
// matching pawn_service.get_breach_details when static_data is nil.
func (s *Service) Phone(intl string) *model.BreachDetails {
	return &model.BreachDetails{
		Phone:          intl,
		Breaches:       []model.Breach{},
		BreachesStatus: "not-found",
	}
}

// Email returns the email breach block from HaveIBeenPwned. Gated by the
// caller (tenant breach flag); here we additionally honor the global
// tpi_global_config.haveibeenpwned.enabled kill switch. On any error the block
// is {email, breaches:[], error}.
func (s *Service) Email(ctx context.Context, email string) *model.BreachDetails {
	enabled, apiKey := s.hibpConfig()
	if !enabled {
		// Matches Python: the email branch only runs when haveibeenpwned.enabled
		// is true; otherwise get_data returns None (no breach_details attached).
		return nil
	}
	breaches, status := s.haveibeenpwned(ctx, email, apiKey)
	return &model.BreachDetails{
		Email:            email,
		NumberOfBreaches: len(breaches),
		Breaches:         breaches,
		BreachesStatus:   status,
	}
}

func (s *Service) hibpConfig() (enabled bool, apiKey string) {
	if s.cfg == nil {
		return false, ""
	}
	tpi, ok := s.cfg.Get("tpi_global_config", nil).(map[string]any)
	if !ok {
		return false, ""
	}
	h, ok := tpi["haveibeenpwned"].(map[string]any)
	if !ok {
		return false, ""
	}
	enabled, _ = h["enabled"].(bool)
	apiKey, _ = h["api_key"].(string)
	return enabled, apiKey
}

// hibpBreach is the raw HIBP breach record (subset).
type hibpBreach struct {
	Name         string   `json:"Name"`
	BreachDate   string   `json:"BreachDate"`
	PwnCount     int      `json:"PwnCount"`
	DataClasses  []string `json:"DataClasses"`
	IsVerified   bool     `json:"IsVerified"`
	IsFabricated bool     `json:"IsFabricated"`
	IsSensitive  bool     `json:"IsSensitive"`
	IsRetired    bool     `json:"IsRetired"`
	IsSpamList   bool     `json:"IsSpamList"`
	IsMalware    bool     `json:"IsMalware"`
}

// haveibeenpwned returns the parsed breaches and the status string
// (found/not-found/error), mirroring get_haveibeenpwned_data + BreachDetail.
func (s *Service) haveibeenpwned(ctx context.Context, email, apiKey string) ([]model.Breach, string) {
	u := "https://haveibeenpwned.com/api/v3/breachedaccount/" +
		url.PathEscape(email) + "?truncateResponse=false"
	status, body, err := doHTTP(ctx, s.timeout, "GET", u, map[string]string{"hibp-api-key": apiKey})
	if err != nil {
		return []model.Breach{}, "error"
	}
	switch status {
	case 200:
		var raw []hibpBreach
		if err := json.Unmarshal(body, &raw); err != nil {
			return []model.Breach{}, "error"
		}
		out := make([]model.Breach, 0, len(raw))
		for _, b := range raw {
			out = append(out, model.Breach{
				Name:         strings.ToLower(b.Name),
				Date:         b.BreachDate,
				Data:         strings.Join(b.DataClasses, ","),
				PwnCount:     b.PwnCount,
				IsVerified:   b.IsVerified,
				IsFabricated: b.IsFabricated,
				IsSensitive:  b.IsSensitive,
				IsRetired:    b.IsRetired,
				IsSpamList:   b.IsSpamList,
				IsMalware:    b.IsMalware,
			})
		}
		// Status mirrors BreachDetail: found only when the list is non-empty.
		if len(out) > 0 {
			return out, "found"
		}
		return out, "not-found"
	case 404:
		return []model.Breach{}, "not-found"
	default:
		return []model.Breach{}, "error"
	}
}
