package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	maxIDBytes         = 128
	maxTitleBytes      = 256
	maxBodyBytes       = 16 << 10
	maxTasks           = 1000
	maxLegacyFileBytes = 16 << 20
	migrationSchema    = "stado.dev/tasks-legacy-migration/v1"
)

type taskData struct {
	Title           string `json:"title"`
	Body            string `json:"body,omitempty"`
	Status          string `json:"status"`
	Deleted         bool   `json:"deleted"`
	LegacyID        string `json:"legacy_id,omitempty"`
	LegacyCreatedAt string `json:"legacy_created_at,omitempty"`
	LegacyUpdatedAt string `json:"legacy_updated_at,omitempty"`
}

type artifact struct {
	APIVersion   string          `json:"api_version"`
	ID           string          `json:"id"`
	Version      uint64          `json:"version"`
	Kind         string          `json:"kind"`
	KindSchema   kindSchema      `json:"kind_schema"`
	Scope        string          `json:"scope"`
	Binding      scopeBinding    `json:"scope_binding"`
	Authority    string          `json:"authority"`
	Tags         []string        `json:"tags,omitempty"`
	Groups       []string        `json:"groups,omitempty"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	Sensitivity  string          `json:"sensitivity"`
	Provenance   provenance      `json:"provenance"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	ExpiresAt    time.Time       `json:"expires_at,omitempty"`
	Supersedes   []string        `json:"supersedes,omitempty"`
}

type kindSchema struct {
	PluginIdentity string `json:"plugin_identity"`
	PluginCommit   string `json:"plugin_commit,omitempty"`
	ManifestDigest string `json:"manifest_digest"`
	LocalName      string `json:"local_name"`
	SchemaDigest   string `json:"schema_digest"`
}

type scopeBinding struct {
	Principal       string `json:"principal"`
	CanonicalRepoID string `json:"canonical_repo_id,omitempty"`
	AnchorSessionID string `json:"anchor_session_id,omitempty"`
	AnchorForkPoint string `json:"anchor_fork_point,omitempty"`
}

type provenance struct {
	Origins   []string `json:"origins,omitempty"`
	CreatedBy string   `json:"created_by,omitempty"`
	Refs      []string `json:"refs,omitempty"`
}

type taskView struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	LegacyID  string    `json:"legacy_id,omitempty"`
	Tags      []string  `json:"-"`
}

type legacyTask struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type migrationReceipt struct {
	Schema      string         `json:"schema"`
	SHA256      string         `json:"sha256"`
	ArchiveFile string         `json:"archive_file"`
	Tag         string         `json:"tag"`
	Count       int            `json:"count"`
	Refs        []migrationRef `json:"refs"`
}

type migrationRef struct {
	ID         string `json:"id"`
	Version    uint64 `json:"version"`
	LegacyID   string `json:"legacy_id"`
	DataSHA256 string `json:"data_sha256"`
}

func normalizeTask(data taskData) (taskData, error) {
	data.Title = strings.TrimSpace(data.Title)
	data.Body = strings.TrimSpace(data.Body)
	data.Status = strings.ToLower(strings.TrimSpace(data.Status))
	if data.Status == "" {
		data.Status = "open"
	}
	if data.Title == "" || len(data.Title) > maxTitleBytes {
		return taskData{}, fmt.Errorf("task title must be 1..%d bytes", maxTitleBytes)
	}
	if len(data.Body) > maxBodyBytes {
		return taskData{}, fmt.Errorf("task body must be at most %d bytes", maxBodyBytes)
	}
	if data.Status != "open" && data.Status != "in_progress" && data.Status != "done" {
		return taskData{}, errors.New("task status must be open, in_progress, or done")
	}
	if len(data.LegacyID) > maxIDBytes {
		return taskData{}, fmt.Errorf("legacy task id must be at most %d bytes", maxIDBytes)
	}
	return data, nil
}

func artifactTask(item artifact) (taskView, taskData, error) {
	if item.ID == "" || item.Version == 0 || (item.Authority != "candidate" && item.Authority != "superseded") {
		return taskView{}, taskData{}, errors.New("invalid task artifact envelope")
	}
	var data taskData
	if err := decodeStrict(item.Data, &data); err != nil {
		return taskView{}, taskData{}, fmt.Errorf("invalid task artifact %q: %w", item.ID, err)
	}
	var err error
	data, err = normalizeTask(data)
	if err != nil {
		return taskView{}, taskData{}, fmt.Errorf("invalid task artifact %q: %w", item.ID, err)
	}
	created, updated := item.CreatedAt, item.UpdatedAt
	if data.LegacyCreatedAt != "" {
		created, err = time.Parse(time.RFC3339Nano, data.LegacyCreatedAt)
		if err != nil {
			return taskView{}, taskData{}, fmt.Errorf("invalid legacy created_at for %q", item.ID)
		}
	}
	if data.LegacyUpdatedAt != "" {
		updated, err = time.Parse(time.RFC3339Nano, data.LegacyUpdatedAt)
		if err != nil {
			return taskView{}, taskData{}, fmt.Errorf("invalid legacy updated_at for %q", item.ID)
		}
	}
	return taskView{ID: item.ID, Version: item.Version, Title: data.Title, Body: data.Body, Status: data.Status, CreatedAt: created, UpdatedAt: updated, LegacyID: data.LegacyID, Tags: append([]string(nil), item.Tags...)}, data, nil
}

func sortTasks(tasks []taskView) {
	rank := func(status string) int {
		switch status {
		case "open":
			return 0
		case "in_progress":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if rank(tasks[i].Status) != rank(tasks[j].Status) {
			return rank(tasks[i].Status) < rank(tasks[j].Status)
		}
		if !tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
		}
		return tasks[i].ID < tasks[j].ID
	})
}

func decodeLegacy(raw []byte) ([]legacyTask, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var tasks []legacyTask
	if err := dec.Decode(&tasks); err != nil {
		return nil, fmt.Errorf("decode legacy tasks.json: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("decode legacy tasks.json: trailing JSON")
	}
	if tasks == nil {
		return nil, errors.New("legacy tasks.json must contain a JSON array")
	}
	if len(tasks) > maxTasks {
		return nil, fmt.Errorf("legacy tasks.json contains %d tasks; limit is %d", len(tasks), maxTasks)
	}
	seen := make(map[string]bool, len(tasks))
	for i := range tasks {
		tasks[i].ID = strings.TrimSpace(tasks[i].ID)
		if tasks[i].ID == "" || len(tasks[i].ID) > maxIDBytes || seen[tasks[i].ID] {
			return nil, fmt.Errorf("legacy task %d has an invalid or duplicate id", i)
		}
		seen[tasks[i].ID] = true
		data, err := normalizeTask(taskData{Title: tasks[i].Title, Body: tasks[i].Body, Status: tasks[i].Status, LegacyID: tasks[i].ID})
		if err != nil {
			return nil, fmt.Errorf("legacy task %q: %w", tasks[i].ID, err)
		}
		if tasks[i].CreatedAt.IsZero() || tasks[i].UpdatedAt.IsZero() {
			return nil, fmt.Errorf("legacy task %q requires created_at and updated_at", tasks[i].ID)
		}
		tasks[i].Title, tasks[i].Body, tasks[i].Status = data.Title, data.Body, data.Status
	}
	return tasks, nil
}

func legacyData(task legacyTask) taskData {
	return taskData{Title: task.Title, Body: task.Body, Status: task.Status, Deleted: false, LegacyID: task.ID,
		LegacyCreatedAt: task.CreatedAt.UTC().Format(time.RFC3339Nano), LegacyUpdatedAt: task.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validPageDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func migrationTag(digest string) string { return "stado:tasks-legacy:" + digest }

func createTag(logicalKey string) string {
	return "stado:tasks-create:" + digestBytes([]byte(logicalKey))
}

func verifyMigrated(legacy []legacyTask, artifacts []artifact) error {
	want := make(map[string]taskData, len(legacy))
	for _, task := range legacy {
		want[task.ID] = legacyData(task)
	}
	got := make(map[string]taskData, len(artifacts))
	for _, item := range artifacts {
		_, data, err := artifactTask(item)
		if err != nil {
			return err
		}
		if data.Deleted || data.LegacyID == "" {
			return errors.New("legacy migration tag selected a non-legacy or deleted task")
		}
		if _, duplicate := got[data.LegacyID]; duplicate {
			return fmt.Errorf("legacy task %q has multiple artifact projections", data.LegacyID)
		}
		got[data.LegacyID] = data
	}
	if len(got) != len(want) {
		return fmt.Errorf("legacy migration verification found %d artifacts; expected %d", len(got), len(want))
	}
	for id, expected := range want {
		actual, ok := got[id]
		if !ok || actual != expected {
			return fmt.Errorf("legacy task %q was not durably re-queried with exact data", id)
		}
	}
	return nil
}

func migrationRefs(legacy []legacyTask, artifacts []artifact) ([]migrationRef, error) {
	if err := verifyMigrated(legacy, artifacts); err != nil {
		return nil, err
	}
	refs := make([]migrationRef, 0, len(artifacts))
	for _, item := range artifacts {
		_, data, err := artifactTask(item)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		refs = append(refs, migrationRef{ID: item.ID, Version: item.Version, LegacyID: data.LegacyID, DataSHA256: digestBytes(encoded)})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].LegacyID < refs[j].LegacyID })
	return refs, nil
}

func validateReceipt(receipt migrationReceipt) error {
	if receipt.Schema != migrationSchema || len(receipt.SHA256) != 64 || receipt.Tag != migrationTag(receipt.SHA256) || receipt.Count < 0 || receipt.Count > maxTasks || len(receipt.Refs) != receipt.Count {
		return errors.New("invalid migration receipt fields")
	}
	if receipt.ArchiveFile == "" || strings.Contains(receipt.ArchiveFile, "/") || strings.Contains(receipt.ArchiveFile, "\\") || receipt.ArchiveFile == "." || receipt.ArchiveFile == ".." {
		return errors.New("invalid migration receipt archive")
	}
	seenID, seenLegacy := map[string]bool{}, map[string]bool{}
	for _, ref := range receipt.Refs {
		if ref.ID == "" || len(ref.ID) > 256 || ref.Version == 0 || ref.LegacyID == "" || len(ref.LegacyID) > maxIDBytes || len(ref.DataSHA256) != 64 || seenID[ref.ID] || seenLegacy[ref.LegacyID] {
			return errors.New("invalid or duplicate migration receipt ref")
		}
		seenID[ref.ID], seenLegacy[ref.LegacyID] = true, true
	}
	return nil
}

func verifyReceiptRefs(receipt migrationReceipt, legacy []legacyTask, artifacts []artifact) error {
	if err := validateReceipt(receipt); err != nil {
		return err
	}
	if err := verifyMigrated(legacy, artifacts); err != nil {
		return err
	}
	byID := make(map[string]artifact, len(artifacts))
	for _, item := range artifacts {
		byID[item.ID] = item
	}
	for _, ref := range receipt.Refs {
		item, ok := byID[ref.ID]
		if !ok || item.Version != ref.Version {
			return fmt.Errorf("migration proof ref %s@%d was not re-queried", ref.ID, ref.Version)
		}
		_, data, err := artifactTask(item)
		if err != nil || data.LegacyID != ref.LegacyID {
			return fmt.Errorf("migration proof ref %s@%d changed identity", ref.ID, ref.Version)
		}
		encoded, _ := json.Marshal(data)
		if digestBytes(encoded) != ref.DataSHA256 {
			return fmt.Errorf("migration proof ref %s@%d changed data", ref.ID, ref.Version)
		}
	}
	return nil
}

func appendBoundedArtifacts(all, page []artifact, limit int) ([]artifact, error) {
	if limit <= 0 || len(all)+len(page) > limit {
		return nil, fmt.Errorf("task artifact query exceeds the fail-closed %d-item aggregate bound", limit)
	}
	return append(all, page...), nil
}

func taskEditChange(current, replacement taskData) (taskData, bool, error) {
	normalized, err := normalizeTask(replacement)
	if err != nil {
		return taskData{}, false, err
	}
	return normalized, normalized != current, nil
}

func decodeStrict(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
