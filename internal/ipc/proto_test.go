package ipc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	req := Request{
		Method: MethodFindSymbol,
		Params: json.RawMessage(`{"name":"Dispatch","limit":5}`),
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Method != MethodFindSymbol {
		t.Errorf("Method = %q, want %q", got.Method, MethodFindSymbol)
	}
	if string(got.Params) != string(req.Params) {
		t.Errorf("Params = %s, want %s (raw message must pass through verbatim)", got.Params, req.Params)
	}
}

func TestRequestOmitsEmptyParams(t *testing.T) {
	b, err := json.Marshal(Request{Method: MethodPing})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "params") {
		t.Errorf("params key must be absent when empty, got %s", b)
	}
	if want := `{"method":"ping"}`; string(b) != want {
		t.Errorf("marshal = %s, want %s", b, want)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	result := json.RawMessage(`{"matches":[{"name":"Foo","file":"a.go","line":10}]}`)
	resp := Response{OK: true, Result: result}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Response
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.OK {
		t.Error("OK = false, want true")
	}
	if string(got.Result) != string(result) {
		t.Errorf("Result = %s, want %s (raw message must pass through verbatim)", got.Result, result)
	}
	if got.Error != "" || got.Code != "" {
		t.Errorf("Error/Code = %q/%q, want empty on success", got.Error, got.Code)
	}
}

// Code is additive: it must not appear on the wire when empty, so old
// clients and successful responses see the pre-Code shape unchanged.
func TestResponseCodeOmitEmpty(t *testing.T) {
	cases := []struct {
		name     string
		resp     Response
		wantKeys []string
		omitKeys []string
	}{
		{
			name:     "success omits error and code",
			resp:     Response{OK: true, Result: json.RawMessage(`{}`)},
			wantKeys: []string{`"ok"`, `"result"`},
			omitKeys: []string{`"error"`, `"code"`},
		},
		{
			name:     "codeless error omits code",
			resp:     Response{OK: false, Error: "boom"},
			wantKeys: []string{`"ok"`, `"error"`},
			omitKeys: []string{`"code"`, `"result"`},
		},
		{
			name:     "classified error carries code",
			resp:     Response{OK: false, Error: "symbol not found", Code: CodeNotFound},
			wantKeys: []string{`"error"`, `"code":"not_found"`},
			omitKeys: []string{`"result"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			for _, k := range tc.wantKeys {
				if !strings.Contains(string(b), k) {
					t.Errorf("marshal = %s, missing %s", b, k)
				}
			}
			for _, k := range tc.omitKeys {
				if strings.Contains(string(b), k) {
					t.Errorf("marshal = %s, must omit %s", b, k)
				}
			}
		})
	}
}

func TestMethodMarshalsAsPlainString(t *testing.T) {
	cases := []struct {
		method Method
		want   string
	}{
		{MethodFindSymbol, `"find_symbol"`},
		{MethodGetReferences, `"get_references"`},
		{MethodSearchLexical, `"search_lexical"`},
		{MethodPing, `"ping"`},
	}
	for _, tc := range cases {
		b, err := json.Marshal(tc.method)
		if err != nil {
			t.Fatalf("marshal %q: %v", tc.method, err)
		}
		if string(b) != tc.want {
			t.Errorf("marshal(%q) = %s, want %s", tc.method, b, tc.want)
		}
		var got Method
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if got != tc.method {
			t.Errorf("round trip = %q, want %q", got, tc.method)
		}
	}
}

func TestFindSymbolParamsOmitEmpty(t *testing.T) {
	b, err := json.Marshal(FindSymbolParams{Name: "Dispatch"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"name":"Dispatch"}`; string(b) != want {
		t.Errorf("marshal = %s, want %s (zero-value optional fields must be absent)", b, want)
	}
}

func TestFindSymbolParamsRoundTrip(t *testing.T) {
	p := FindSymbolParams{
		Name:    "Dispatch",
		Kind:    "func",
		Limit:   10,
		Project: "api",
		Since:   "main",
		Focus:   "auth",
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got FindSymbolParams
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != p {
		t.Errorf("round trip = %+v, want %+v", got, p)
	}
}
