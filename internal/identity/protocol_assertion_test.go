package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildAssertionEncodesUntrustedFieldsAsJSON(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := "runtime-\"quoted\"\nline"
	taskID := "task-\"quoted\"\\slash"
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	assertion, err := BuildAssertion(&Material{
		Claims:     Claims{AgentRuntimeID: runtimeID},
		PrivateKey: privateKey,
	}, taskID, now)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "AgentAssertion "
	if !strings.HasPrefix(assertion, prefix) {
		t.Fatalf("assertion prefix = %q", assertion)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(assertion, prefix))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		AgentRuntimeID string `json:"agent_runtime_id"`
		Signature      string `json:"signature"`
		TaskID         string `json:"task_id"`
		Timestamp      string `json:"timestamp"`
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AgentRuntimeID != runtimeID || payload.TaskID != taskID {
		t.Fatalf("unexpected assertion payload: %#v", payload)
	}
	signature, err := base64.StdEncoding.DecodeString(payload.Signature)
	if err != nil {
		t.Fatal(err)
	}
	wantSigned := runtimeID + ":" + taskID + ":" + now.Format("2006-01-02T15:04:05Z")
	if !ed25519.Verify(publicKey, []byte(wantSigned), signature) {
		t.Fatal("assertion signature verification failed")
	}
}
