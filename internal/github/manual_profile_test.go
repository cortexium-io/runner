package github

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManualProfileDecodesAndRemainsBoundToCardApproval(t *testing.T) {
	body, err := WithManualImplementationProfile("Fix the header only.\n\n## Dependencies\n\n- PVTI_base", "mechanical")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"id": "PVTI_manual", "status": map[string]string{"name": "Ready"}, "content": map[string]string{"title": "Header", "body": body}})
	var node projectItemNode
	if err := json.Unmarshal(encoded, &node); err != nil {
		t.Fatal(err)
	}
	item := decodeProjectItemNode(node)
	if item.PlanningMetadataInvalid || item.ImplementationProfile != "mechanical" || len(item.Dependencies) != 1 || item.PlanningBatchFingerprint != "" {
		t.Fatalf("manual profile changed planning authority or dependencies: %#v", item)
	}
	p, _, _ := deliveryFixture(t)
	item = signDeliveryFixture(t, p, item, "implementer", "ready")
	if _, err := p.validateAction(item); err != nil {
		t.Fatal(err)
	}
	item.ImplementationProfile = "different"
	if _, err := p.validateAction(item); err == nil {
		t.Fatal("profile change retained approval")
	}
	if unchanged, err := WithManualImplementationProfile(body, "mechanical"); err != nil || unchanged != body {
		t.Fatalf("repeated selection rewrote card: %v", err)
	}
	if _, err := WithManualImplementationProfile(body, "different"); err == nil {
		t.Fatal("conflicting selection overwrote card")
	}
}

func TestManualProfileRejectsMalformedAndAmbiguousSections(t *testing.T) {
	for _, value := range []string{
		manualProfileHeading, manualProfileHeading + " extra\nmechanical",
		manualProfileHeading + "\nmechanical\nother",
		manualProfileHeading + "\nmechanical\n\n" + manualProfileHeading + "\nmechanical",
		manualProfileHeading + "\nmechanical — arbitrary label",
		manualProfileHeading + "\n../mechanical",
		manualProfileHeading + "\n`mechanical`",
		manualProfileHeading + "\nmechanical\x00",
	} {
		t.Run(strings.ReplaceAll(value, "\n", "/"), func(t *testing.T) {
			if _, err := ManualImplementationProfile(value); err == nil {
				t.Fatal("accepted malformed profile selection")
			}
			encoded, _ := json.Marshal(map[string]any{"id": "PVTI_bad", "content": map[string]string{"body": value}})
			var node projectItemNode
			if err := json.Unmarshal(encoded, &node); err != nil {
				t.Fatal(err)
			}
			if item := decodeProjectItemNode(node); !item.PlanningMetadataInvalid || item.ImplementationProfile != "" {
				t.Fatal("malformed profile remained executable")
			}
		})
	}
	if profile, err := ManualImplementationProfile("Ordinary request without selection."); err != nil || profile != "" {
		t.Fatalf("legacy Ready default changed: %q %v", profile, err)
	}
	if _, err := ManualImplementationProfile("Existing planned card.\n\n## Runner planning metadata"); err == nil {
		t.Fatal("planned metadata was accepted as ordinary input")
	}
}
