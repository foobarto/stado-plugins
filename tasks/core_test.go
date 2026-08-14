package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecodeLegacyStrictAndExact(t *testing.T) {
	raw := []byte(`[{"id":"old-1","title":" first ","body":" body ","status":"","created_at":"2026-08-14T01:02:03Z","updated_at":"2026-08-14T02:03:04Z"}]`)
	tasks, err := decodeLegacy(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Title != "first" || tasks[0].Body != "body" || tasks[0].Status != "open" {
		t.Fatalf("unexpected legacy normalization: %#v", tasks)
	}
	if _, err := decodeLegacy(append(raw, []byte(` {}`)...)); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing JSON accepted: %v", err)
	}
	if _, err := decodeLegacy([]byte(`[{"id":"x","title":"x","status":"open","created_at":"2026-08-14T01:02:03Z","updated_at":"2026-08-14T02:03:04Z","native":true}]`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field accepted: %v", err)
	}
}

func TestVerifyMigratedRejectsMissingDuplicateChangedAndDeleted(t *testing.T) {
	created := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	legacy := []legacyTask{{ID: "old-1", Title: "one", Status: "open", CreatedAt: created, UpdatedAt: created}}
	data := legacyData(legacy[0])
	raw, _ := json.Marshal(data)
	item := artifact{ID: "new-1", Version: 1, Authority: "candidate", Data: raw, CreatedAt: created, UpdatedAt: created}
	if err := verifyMigrated(legacy, []artifact{item}); err != nil {
		t.Fatal(err)
	}
	for name, items := range map[string][]artifact{
		"missing":   nil,
		"duplicate": {item, item},
		"changed":   {{ID: "new-1", Version: 1, Authority: "candidate", Data: []byte(`{"title":"changed","status":"open","deleted":false,"legacy_id":"old-1","legacy_created_at":"2026-08-14T01:02:03Z","legacy_updated_at":"2026-08-14T01:02:03Z"}`)}},
		"deleted":   {{ID: "new-1", Version: 2, Authority: "candidate", Data: []byte(`{"title":"one","status":"open","deleted":true,"legacy_id":"old-1","legacy_created_at":"2026-08-14T01:02:03Z","legacy_updated_at":"2026-08-14T01:02:03Z"}`)}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyMigrated(legacy, items); err == nil {
				t.Fatal("verification unexpectedly succeeded")
			}
		})
	}
}

func TestTaskTombstoneAndOrdering(t *testing.T) {
	base := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	tasks := []taskView{{ID: "done", Status: "done", CreatedAt: base}, {ID: "progress", Status: "in_progress", CreatedAt: base}, {ID: "newer", Status: "open", CreatedAt: base.Add(time.Hour)}, {ID: "older", Status: "open", CreatedAt: base}}
	sortTasks(tasks)
	got := []string{tasks[0].ID, tasks[1].ID, tasks[2].ID, tasks[3].ID}
	want := []string{"older", "newer", "progress", "done"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	data, err := normalizeTask(taskData{Title: "x", Status: "open", Deleted: true})
	if err != nil || !data.Deleted {
		t.Fatalf("logical tombstone not preserved: %#v %v", data, err)
	}
}

func TestMigrationDigestAndReceiptNamesAreDeterministic(t *testing.T) {
	raw := []byte("legacy bytes")
	digest := digestBytes(raw)
	if len(digest) != 64 || migrationTag(digest) != "stado:tasks-legacy:"+digest {
		t.Fatalf("unexpected digest/tag: %q %q", digest, migrationTag(digest))
	}
	if digestBytes(raw) != digest {
		t.Fatal("digest is not deterministic")
	}
	if createTag("model:retry-1") != createTag("model:retry-1") || createTag("model:retry-1") == createTag("model:retry-2") {
		t.Fatal("logical create tags do not provide stable retry lookup")
	}
}

func TestMigrationReceiptPinsImmutableVersionAcrossLaterEdit(t *testing.T) {
	stamp := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	legacy := []legacyTask{{ID: "old-1", Title: "one", Status: "open", CreatedAt: stamp, UpdatedAt: stamp}}
	data := legacyData(legacy[0])
	raw, _ := json.Marshal(data)
	// A later edit makes the migration version superseded. Exact ref queries
	// must still prove the admitted bytes instead of consulting the live head.
	initial := artifact{ID: "new-1", Version: 1, Authority: "superseded", Data: raw, CreatedAt: stamp, UpdatedAt: stamp}
	refs, err := migrationRefs(legacy, []artifact{initial})
	if err != nil {
		t.Fatal(err)
	}
	receipt := migrationReceipt{Schema: migrationSchema, SHA256: strings.Repeat("a", 64), ArchiveFile: "tasks.json.archive-" + strings.Repeat("a", 64), Tag: migrationTag(strings.Repeat("a", 64)), Count: 1, Refs: refs}
	if err := verifyReceiptRefs(receipt, legacy, []artifact{initial}); err != nil {
		t.Fatalf("immutable migration proof rejected after later head edit: %v", err)
	}
	changed := initial
	changed.Version = 2
	if err := verifyReceiptRefs(receipt, legacy, []artifact{changed}); err == nil {
		t.Fatal("receipt accepted a different artifact version")
	}
}

func TestArtifactAggregateBoundFailsClosedBeforeAppend(t *testing.T) {
	all := make([]artifact, maxTasks)
	if _, err := appendBoundedArtifacts(all, []artifact{{ID: "overflow"}}, maxTasks); err == nil {
		t.Fatal("aggregate bound accepted an unbounded live head")
	}
	if got, err := appendBoundedArtifacts(nil, []artifact{{ID: "ok"}}, maxTasks); err != nil || len(got) != 1 {
		t.Fatalf("bounded append failed: %v %#v", err, got)
	}
}

func TestReceiptRejectsCrashTruncationAndDuplicateRefs(t *testing.T) {
	if err := decodeStrict([]byte(`{"schema":"stado.dev/tasks-legacy-migration/v1"`), &migrationReceipt{}); err == nil {
		t.Fatal("truncated post-replacement receipt accepted")
	}
	digest := strings.Repeat("b", 64)
	ref := migrationRef{ID: "id", Version: 1, LegacyID: "legacy", DataSHA256: digest}
	receipt := migrationReceipt{Schema: migrationSchema, SHA256: digest, ArchiveFile: "tasks.json.archive-" + digest, Tag: migrationTag(digest), Count: 2, Refs: []migrationRef{ref, ref}}
	if err := validateReceipt(receipt); err == nil {
		t.Fatal("duplicate restart proof refs accepted")
	}
}

func TestEditRetryRecognizesAlreadyAppliedNormalizedData(t *testing.T) {
	current := taskData{Title: "same", Body: "body", Status: "open", LegacyID: "legacy"}
	got, changed, err := taskEditChange(current, taskData{Title: " same ", Body: " body ", Status: "OPEN", LegacyID: "legacy"})
	if err != nil || changed || got != current {
		t.Fatalf("reply-loss retry would mint a no-op version: got=%#v changed=%v err=%v", got, changed, err)
	}
	_, changed, err = taskEditChange(current, taskData{Title: "different", Body: "body", Status: "open", LegacyID: "legacy"})
	if err != nil || !changed {
		t.Fatalf("real edit was suppressed: changed=%v err=%v", changed, err)
	}
}
