package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostCallAllocatesDeclaredCeilingForMaximumAdmittedSkill(t *testing.T) {
	body := strings.Repeat("x", 128<<10)
	wire, err := json.Marshal(resourceOpenResponse{
		Schema: resourceOpenSchema,
		resourceFact: resourceFact{
			ID: "sha256:body", Digest: "sha256:body", Kind: "skill", Name: "large",
			Scope: "project", Provenance: "project-discovered", ModelVisible: true,
		},
		ContentFormat: "text/markdown", Content: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) <= 64<<10 {
		t.Fatalf("fixture response = %d bytes, must exceed the obsolete 64 KiB allocation", len(wire))
	}
	var opened resourceOpenResponse
	err = callHostJSONProtocol(resourceOpenRequest{Kind: "skill", ID: "sha256:body", CatalogDigest: "sha256:catalog"}, &opened, 1<<20,
		func(_ []byte, capacity int32) (int32, []byte) {
			if capacity != 1<<20 {
				t.Fatalf("host call capacity = %d, want declared 1 MiB ceiling", capacity)
			}
			return int32(len(wire)), append([]byte(nil), wire...)
		})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Content != body {
		t.Fatalf("opened content bytes = %d, want %d", len(opened.Content), len(body))
	}
}

func TestHostCallRejectsErrorLengthOutsideAllocatedCeiling(t *testing.T) {
	var response any
	if err := callHostJSONProtocol(struct{}{}, &response, 16, func([]byte, int32) (int32, []byte) {
		return -17, nil
	}); err == nil || !strings.Contains(err.Error(), "invalid length") {
		t.Fatalf("invalid negative length error = %v", err)
	}
}
