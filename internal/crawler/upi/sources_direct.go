package upi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// This file holds the token-free per-suffix UPI sources whose validate/parse is
// a single HTTP call: CONFIRMTKT, MMT, EMT, NYKAA, RAILYATRI, GOIBIBO, INDIGO,
// PRACTO, KETTO, JUST_PAY, KAYAK. Each mirrors its Python spider's validate_upi
// + parse_upi_response exactly (endpoint, body, and truthy/name rules).

// errNoMatch marks a "no condition matched" outcome (Python
// NoConditionMatchedException) — the suffix produced no usable verdict.
var errNoMatch = fmt.Errorf("upi: no condition matched")

// --- CONFIRMTKT_UPI ---

type confirmTkt struct{}

func (confirmTkt) name() string { return "CONFIRMTKT_UPI" }
func (confirmTkt) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	u := "https://securedapi.confirmtkt.com/api/platform/validateVPA?vpa=" + url.QueryEscape(vpaFor(national, suffix))
	h := map[string]string{
		"Accept": "*/*", "Accept-Language": "en-GB,en-US;q=0.9,en;q=0.8", "Connection": "keep-alive",
		"Origin": "https://www.confirmtkt.com", "Referer": "https://www.confirmtkt.com/",
		"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Site": "same-site",
		"User-Agent": randomUA(), "sec-ch-ua-mobile": "?0",
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "GET", u, nil, h)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("confirmtkt status %d", st)
	}
	var j struct {
		Status           string `json:"status"`
		IsVPAValid       *int   `json:"isVPAValid"`
		PayerAccountName string `json:"payerAccountName"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.Status != "SUCCESS" || j.IsVPAValid == nil {
		return nil, errNoMatch
	}
	if *j.IsVPAValid == 1 && j.PayerAccountName != "" && j.PayerAccountName != "NA" {
		return trueP("CONFIRMTKT_UPI", suffix, j.PayerAccountName, vpaFor(national, suffix)), nil
	}
	if *j.IsVPAValid == 0 {
		return falseP("CONFIRMTKT_UPI", suffix), nil
	}
	return nil, errNoMatch
}

// --- MMT_UPI ---

type mmtUPI struct{}

func (mmtUPI) name() string { return "MMT_UPI" }
func (mmtUPI) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	bodyJSON, _ := json.Marshal(map[string]string{"vpa": vpaFor(national, suffix)})
	checkoutID := "880231220" + fmt.Sprintf("%d", 100000+rand6()) // referer only
	h := map[string]string{
		"authority": "payments.makemytrip.com", "accept": "application/json, text/plain, */*",
		"accept-language": "en-GB,en-US;q=0.9,en;q=0.8", "cache-control": "no-cache",
		"content-type": "application/json", "deviceid": randAlnum(32),
		"origin": "https://payments.makemytrip.com", "pragma": "no-cache", "user-agent": randomUA(),
		"referer":          "https://payments.makemytrip.com/ui/checkout/?id=" + checkoutID,
		"sec-ch-ua-mobile": "?0", "sec-fetch-dest": "empty", "sec-fetch-mode": "cors",
		"sec-fetch-site": "same-origin", "ver": "7.3.5",
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://payments.makemytrip.com/api/payments/validateVPA", strings.NewReader(string(bodyJSON)), h)
	if err != nil {
		return nil, err
	}
	_ = st // MMT ignores the status code, keys on the JSON body
	var j struct {
		Status       string `json:"status"`
		PayerName    string `json:"payerName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.Status == "SUCCESS" {
		return trueP("MMT_UPI", suffix, j.PayerName, vpaFor(national, suffix)), nil
	}
	if j.ErrorMessage == "Incorrect UPI ID!" {
		return falseP("MMT_UPI", suffix), nil
	}
	if j.ErrorMessage != "" {
		return nil, fmt.Errorf("mmt: %s", j.ErrorMessage)
	}
	return nil, errNoMatch
}

// --- EMT_UPI ---

type emtUPI struct{}

func (emtUPI) name() string { return "EMT_UPI" }
func (emtUPI) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	// Raw single-quoted pseudo-JSON string body, verbatim from Python.
	raw := "{'contact':'" + vpaFor(national, suffix) + "','_totalFare':'0','Prefix':'0'}"
	h := map[string]string{
		"authority": "www.easemytrip.com", "accept": "application/json, text/plain, */*",
		"accept-language": "en-GB,en;q=0.9", "cache-control": "no-cache", "content-type": "application/json",
		"origin": "https://www.easemytrip.com", "pragma": "no-cache", "sec-ch-ua-mobile": "?0",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin", "user-agent": randomUA(),
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://www.easemytrip.com/hotels/Travel/VPAVerify", strings.NewReader(raw), h)
	if err != nil {
		return nil, err
	}
	_ = st
	var j struct {
		Status           string `json:"status"`
		PayerAccountName string `json:"payerAccountName"`
		Error            string `json:"error"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.Status == "SUCCESS" {
		return trueP("EMT_UPI", suffix, j.PayerAccountName, vpaFor(national, suffix)), nil
	}
	if j.Error == "Please enter correct UPI address" {
		return falseP("EMT_UPI", suffix), nil
	}
	if j.Error != "" {
		return nil, fmt.Errorf("emt: %s", j.Error)
	}
	return nil, errNoMatch
}

// --- NYKAA_UPI ---

type nykaaUPI struct{}

func (nykaaUPI) name() string { return "NYKAA_UPI" }
func (nykaaUPI) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	u := "https://www.nykaa.com/gateway-api/payment/upi/validateVpa/v2?customerVpa=" + url.QueryEscape(vpaFor(national, suffix))
	h := map[string]string{
		"authority": "www.nykaa.com", "accept": "application/json, text/plain, */*",
		"content-type": "application/json", "referer": "https://www.nykaa.com/payment",
		"cache-control": "no-cache", "pragma": "no-cache",
		"sec-ch-ua":  `"Google Chrome";v="110", "Not A(Brand";v="24", "Chromium";v="110"`,
		"user-agent": randomUA(),
	}
	_, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "GET", u, nil, h)
	if err != nil {
		return nil, err
	}
	var j struct {
		Data *struct {
			PayerAccountName string `json:"payerAccountName"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.Data != nil {
		if j.Data.PayerAccountName != "" {
			return trueP("NYKAA_UPI", suffix, j.Data.PayerAccountName, vpaFor(national, suffix)), nil
		}
		return nil, errNoMatch
	}
	if j.Message == "Invalid UPI Id" {
		return falseP("NYKAA_UPI", suffix), nil
	}
	if j.Message != "" {
		return nil, fmt.Errorf("nykaa: %s", j.Message)
	}
	return nil, errNoMatch
}

// --- RAILYATRI_UPI ---

type railYatri struct{}

func (railYatri) name() string { return "RAILYATRI_UPI" }
func (railYatri) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	form := url.Values{"vpa": {vpaFor(national, suffix)}}
	h := map[string]string{
		"authority": "www.railyatri.in", "accept": "*/*",
		"content-type": "application/x-www-form-urlencoded; charset=UTF-8",
		"origin":       "https://www.railyatri.in", "referer": "https://www.railyatri.in/",
		"x-requested-with": "XMLHttpRequest", "user-agent": randomUA(),
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://www.railyatri.in/payment/validate_upi", strings.NewReader(form.Encode()), h)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("railyatri status %d", st)
	}
	var j struct {
		Success      *bool  `json:"success"`
		CustomerName string `json:"customer_name"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.Success != nil && !*j.Success {
		return falseP("RAILYATRI_UPI", suffix), nil
	}
	if j.Success != nil && *j.Success && j.CustomerName != "" {
		return trueP("RAILYATRI_UPI", suffix, j.CustomerName, vpaFor(national, suffix)), nil
	}
	return nil, errNoMatch
}

// --- GOIBIBO_UPI ---

type goibibo struct{}

func (goibibo) name() string { return "GOIBIBO_UPI" }
func (goibibo) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	bodyJSON, _ := json.Marshal(map[string]string{"vpa": vpaFor(national, suffix)})
	h := map[string]string{
		"authority": "payments.goibibo.com", "content-type": "application/json",
		"origin": "https://payments.goibibo.com", "referer": "https://payments.goibibo.com/", "user-agent": randomUA(),
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://payments.goibibo.com/pagos/v2/api/payments/validateVpa", strings.NewReader(string(bodyJSON)), h)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("goibibo status %d", st)
	}
	var j struct {
		Status       string `json:"status"`
		VpaStatus    string `json:"vpaStatus"`
		PayerName    string `json:"payerName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.Status != "SUCCESS" {
		return nil, errNoMatch
	}
	if j.VpaStatus == "SUCCESS" && j.PayerName != "" && j.PayerName != "NA" {
		return trueP("GOIBIBO_UPI", suffix, j.PayerName, vpaFor(national, suffix)), nil
	}
	if j.VpaStatus == "FAILED" && j.ErrorMessage == "Incorrect UPI ID!" {
		return falseP("GOIBIBO_UPI", suffix), nil
	}
	return nil, errNoMatch
}

// --- IndiGo_UPI (no name) ---

type indiGo struct{}

func (indiGo) name() string { return "IndiGo_UPI" }
func (indiGo) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	form := url.Values{"IndiGoUPIValidation.VPA": {vpaFor(national, suffix)}}
	h := map[string]string{
		"authority": "book.goindigo.in", "content-type": "application/x-www-form-urlencoded; charset=UTF-8",
		"origin": "https://book.goindigo.in", "referer": "https://book.goindigo.in/",
		"x-requested-with": "XMLHttpRequest", "user-agent": randomUA(),
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://book.goindigo.in/Payment/UPIValidation", strings.NewReader(form.Encode()), h)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("indigo status %d", st)
	}
	var j struct {
		Inner *struct {
			Resp *struct {
				Success *bool `json:"success"`
			} `json:"upiValidationResponse"`
		} `json:"indiGoUPIValidation"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.Inner == nil || j.Inner.Resp == nil || j.Inner.Resp.Success == nil {
		return nil, errNoMatch
	}
	if *j.Inner.Resp.Success {
		return trueP("IndiGo_UPI", suffix, "", vpaFor(national, suffix)), nil
	}
	return falseP("IndiGo_UPI", suffix), nil
}

// --- PRACTO_UPI (no name) ---

type practo struct{}

func (practo) name() string { return "PRACTO_UPI" }
func (practo) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	form := url.Values{"vpa": {vpaFor(national, suffix)}}
	h := map[string]string{
		"authority": "payments.practo.com", "accept": "*/*",
		"content-type": "application/x-www-form-urlencoded; charset=UTF-8",
		"origin":       "https://payments.practo.com", "referer": "https://payments.practo.com/",
		"x-requested-with": "XMLHttpRequest", "user-agent": randomUA(),
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://payments.practo.com/v2/payments/validateVpa", strings.NewReader(form.Encode()), h)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("practo status %d", st)
	}
	var j struct {
		APIStatus string `json:"apiStatus"`
		Payload   *struct {
			IsValid *bool `json:"isValid"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.APIStatus != "success" || j.Payload == nil || j.Payload.IsValid == nil {
		return nil, errNoMatch
	}
	if *j.Payload.IsValid {
		return trueP("PRACTO_UPI", suffix, "", vpaFor(national, suffix)), nil
	}
	return falseP("PRACTO_UPI", suffix), nil
}

// --- KETTO_UPI ---

type ketto struct{}

func (ketto) name() string { return "KETTO_UPI" }
func (ketto) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	bodyJSON, _ := json.Marshal(map[string]string{"vpa": vpaFor(national, suffix)})
	h := map[string]string{
		"sec-ch-ua-mobile": "?0", "User-Agent": randomUA(),
		"Content-Type": "application/json", "Accept": "application/json, text/plain, */*",
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://www.ketto.org/api/verify/vpa", strings.NewReader(string(bodyJSON)), h)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("ketto status %d", st)
	}
	var j struct {
		Error json.RawMessage `json:"error"`
		Data  *struct {
			IsVPAValid       *int   `json:"isVPAValid"`
			CustomerName     string `json:"customer_name"`
			PayerAccountName string `json:"payerAccountName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if isJSONTrue(j.Error) {
		return nil, fmt.Errorf("ketto error")
	}
	if j.Data == nil || j.Data.IsVPAValid == nil {
		return nil, errNoMatch
	}
	if *j.Data.IsVPAValid == 1 {
		name := j.Data.CustomerName
		if name == "" {
			name = j.Data.PayerAccountName
		}
		return trueP("KETTO_UPI", suffix, name, vpaFor(national, suffix)), nil
	}
	if *j.Data.IsVPAValid == 0 {
		return falseP("KETTO_UPI", suffix), nil
	}
	return nil, errNoMatch
}

// --- JUST_PAY_UPI (no name) ---

type justPay struct{}

func (justPay) name() string { return "JUST_PAY_UPI" }
func (justPay) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	bodyJSON, _ := json.Marshal(map[string]string{"CustomerVPA": vpaFor(national, suffix)})
	h := map[string]string{
		"authority": "checkout.firstcry.com", "accept": "application/json, text/javascript, */*; q=0.01",
		"accept-language": "en-US,en;q=0.9,hi;q=0.8", "cache-control": "no-cache",
		"content-type": "application/json; charset=UTF-8", "origin": "https://checkout.firstcry.com",
		"pragma": "no-cache", "referer": "https://checkout.firstcry.com/pay", "sec-ch-ua-mobile": "?0",
		"sec-fetch-dest": "empty", "sec-fetch-mode": "cors", "sec-fetch-site": "same-origin",
		"user-agent": randomUA(), "x-requested-with": "XMLHttpRequest",
	}
	_, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://checkout.firstcry.com/Fccheckout/VerifyJuspayVPA", strings.NewReader(string(bodyJSON)), h)
	if err != nil {
		return nil, err
	}
	var j struct {
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, err
	}
	if j.Status == "VALID" {
		return trueP("JUST_PAY_UPI", suffix, "", vpaFor(national, suffix)), nil
	}
	if j.Status == "INVALID" {
		return falseP("JUST_PAY_UPI", suffix), nil
	}
	if isJSONTrue(j.Error) {
		return nil, fmt.Errorf("justpay error")
	}
	return nil, errNoMatch
}

// --- KAYAK_EMT (no name; bare-bool response) ---

type kayak struct{}

func (kayak) name() string { return "KAYAK_EMT" }
func (kayak) validate(ctx context.Context, national, suffix string, sm SourceMeta, proxyURL *url.URL) (*Profile, error) {
	raw := "{'VPA':'" + vpaFor(national, suffix) + "'}"
	h := map[string]string{
		"authority": "flightservice-web.easemytrip.com", "accept": "application/json, text/javascript, */*; q=0.01",
		"accept-language": "en-US,en;q=0.9,hi;q=0.8", "cache-control": "no-cache",
		"content-type": "application/json; charset=UTF-8", "origin": "https://flight.easemytrip.com",
		"pragma": "no-cache", "sec-ch-ua-mobile": "?0", "sec-fetch-dest": "empty", "sec-fetch-mode": "cors",
		"sec-fetch-site": "same-site", "user-agent": randomUA(),
	}
	st, body, err := do(ctx, httpClient(proxyURL, sourceTimeout(sm)), "POST", "https://flightservice-web.easemytrip.com/EmtAppService/Book/CheckVPANew", strings.NewReader(raw), h)
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("kayak status %d", st)
	}
	var b bool
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(body))), &b); err != nil {
		return nil, errNoMatch
	}
	if b {
		return trueP("KAYAK_EMT", suffix, "", vpaFor(national, suffix)), nil
	}
	return falseP("KAYAK_EMT", suffix), nil
}

// isJSONTrue reports whether a raw JSON value is the literal boolean true.
func isJSONTrue(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "true"
}
