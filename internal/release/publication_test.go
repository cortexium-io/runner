package release

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicationConfirmsOwnershipAndRejoinsLostAcknowledgement(t *testing.T) {
	for _, mode := range []string{"success", "lost acknowledgement", "failed status"} {
		t.Run(mode, func(t *testing.T) {
			head, base := strings.Repeat("a", 40), strings.Repeat("b", 40)
			b := Live{Config: Config{Repository: "owner/repo", GH: "/gh", Delivery: &DeliveryConfig{Ownership: []string{"/ownership"}}}}
			j := Job{Request: Request{RunID: "fixture", Key: "publication", Action: "publish", Candidate: head, Source: base}}
			state, publications := "", 0
			b.runCommand = func(_ context.Context, _ []byte, path string, args ...string) ([]byte, error) {
				if path == "/ownership" {
					publications++
					state = "success"
					if mode == "failed status" {
						state = "failure"
					}
					if mode == "lost acknowledgement" {
						return nil, &commandUncertain{program: "ownership"}
					}
					return nil, nil
				}
				if strings.Contains(strings.Join(args, " "), "/pulls?") {
					return json.Marshal([]any{map[string]any{"number": 1, "head": map[string]string{"sha": head}, "base": map[string]string{"sha": base}}})
				}
				return json.Marshal(map[string]any{"sha": head, "statuses": []any{map[string]string{"context": "Techbase / generated ownership", "state": state}}})
			}
			save := func() error { return nil }
			_ = b.publish(t.Context(), &j, save)
			if j.Done {
				t.Fatal("publication advanced without independently confirming status")
			}
			if err := b.publish(t.Context(), &j, save); err != nil || !j.Done || j.Failed != (mode == "failed status") || publications != 1 {
				t.Fatal("ownership status was repeated, waived or not reconciled", err, j, publications)
			}
		})
	}
}
