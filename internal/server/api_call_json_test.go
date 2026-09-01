package server

import (
	"encoding/json"
	"testing"
)

func TestCPAAPICallRequestAcceptsAuthIndexSpellingsAndNumbers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "snake number", body: `{"auth_index":7,"method":"GET","url":"https://chatgpt.com/backend-api/wham/usage"}`, want: "7"},
		{name: "camel string", body: `{"authIndex":"8","method":"GET","url":"https://chatgpt.com/backend-api/wham/usage"}`, want: "8"},
		{name: "pascal number", body: `{"AuthIndex":9,"method":"GET","url":"https://chatgpt.com/backend-api/wham/usage"}`, want: "9"},
		{name: "case variant", body: `{"AUTH_INDEX":10,"method":"GET","url":"https://chatgpt.com/backend-api/wham/usage"}`, want: "10"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var call cpaAPICallRequest
			if err := json.Unmarshal([]byte(test.body), &call); err != nil {
				t.Fatal(err)
			}
			if got := firstNonEmpty(call.AuthIndexSnake, call.AuthIndexCamel, call.AuthIndexPascal); got != test.want {
				t.Fatalf("auth index=%q, want %q", got, test.want)
			}
		})
	}
}

func TestCPAAPICallRequestRejectsInvalidAuthIndexType(t *testing.T) {
	t.Parallel()
	var call cpaAPICallRequest
	if err := json.Unmarshal([]byte(`{"auth_index":true}`), &call); err == nil {
		t.Fatal("boolean auth_index was accepted")
	}
}

func TestContainsSidecarCredentialHeader(t *testing.T) {
	t.Parallel()
	if !containsSidecarCredentialHeader(map[string]string{"authorization": "Bearer cais_test_0000000000000000000000000000"}) {
		t.Fatal("sidecar key was not recognized")
	}
	if containsSidecarCredentialHeader(map[string]string{"Authorization": "Bearer native-oauth-token"}) {
		t.Fatal("native token was classified as sidecar")
	}
	if containsSidecarCredentialHeader(map[string]string{"Authorization": "Basic cais_test_0000000000000000000000000000"}) {
		t.Fatal("non-bearer token was classified as sidecar")
	}
}
