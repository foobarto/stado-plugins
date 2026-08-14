//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

// Every state transition uses the generic broker artifact namespace. The only
// filesystem access is the bounded, one-way legacy importer rooted beneath the
// host-resolved cfg:state_dir/tasks directory.

//go:wasmimport stado stado_artifact_propose
func stadoArtifactPropose(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_artifact_query
func stadoArtifactQuery(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_artifact_edit
func stadoArtifactEdit(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_ui_choose
func stadoUIChoose(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_cfg_state_dir
func stadoCfgStateDir(bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_stat
func stadoFSStat(pathPtr, pathLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_fs_read
func stadoFSRead(pathPtr, pathLen, respPtr, respCap uint32) int32

//go:wasmimport stado stado_fs_write
func stadoFSWrite(pathPtr, pathLen, dataPtr, dataLen uint32) int32

//go:wasmimport stado stado_fs_last_error
func stadoFSLastError(respPtr, respCap uint32) int32

var (
	pinned sync.Map
	appMu  sync.Mutex
)

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buffer := make([]byte, size)
	pointer := uintptr(unsafe.Pointer(&buffer[0]))
	pinned.Store(pointer, buffer)
	return int32(pointer)
}

//go:wasmexport stado_free
func stadoFree(pointer int32, size int32) {
	pinned.Delete(uintptr(pointer))
	_ = size
}

type lifecycleAnchor struct {
	SessionID         string `json:"session_id"`
	SessionGeneration uint64 `json:"session_generation"`
	CanonicalRepoID   string `json:"canonical_repo_id,omitempty"`
}

type commandEnvelope struct {
	Schema      string          `json:"schema"`
	Application string          `json:"application"`
	Anchor      lifecycleAnchor `json:"anchor"`
	Sequence    uint64          `json:"sequence"`
	Command     string          `json:"command"`
	Args        string          `json:"args,omitempty"`
}

type toolArgs struct {
	Action         string  `json:"action"`
	IdempotencyKey string  `json:"idempotency_key,omitempty"`
	ID             string  `json:"id,omitempty"`
	Title          *string `json:"title,omitempty"`
	Body           *string `json:"body,omitempty"`
	Status         *string `json:"status,omitempty"`
	Limit          *int    `json:"limit,omitempty"`
}

type queryResponse struct {
	Items      []artifact `json:"items"`
	PageDigest string     `json:"page_digest"`
	NextOffset int        `json:"next_offset,omitempty"`
	Complete   bool       `json:"complete"`
}

type fsStat struct {
	Mode  uint32 `json:"mode"`
	Size  int64  `json:"size"`
	MTime string `json:"mtime"`
	Type  string `json:"type"`
}

type chooseOption struct {
	ID    string       `json:"id"`
	Label string       `json:"label"`
	Input *chooseInput `json:"input,omitempty"`
}

type chooseInput struct {
	Default   string          `json:"default"`
	Validator chooseValidator `json:"validator"`
}

type chooseValidator struct {
	Kind string `json:"kind"`
	Spec string `json:"spec,omitempty"`
}

type chooseResponse struct {
	Selected   []string `json:"selected"`
	InputValue string   `json:"input_value,omitempty"`
	Cancelled  bool     `json:"cancelled"`
}

//go:wasmexport stado_tool_tasks
func stadoToolTasks(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	appMu.Lock()
	defer appMu.Unlock()
	if err := ensureLegacyMigrated(); err != nil {
		return writeError(resultPointer, resultCapacity, "tasks legacy migration blocked: "+err.Error())
	}
	var args toolArgs
	if err := decodeStrict(wasmBytes(inputPointer, inputLength), &args); err != nil {
		return writeError(resultPointer, resultCapacity, "tasks: "+err.Error())
	}
	result, err := runTool(args)
	if err != nil {
		return writeError(resultPointer, resultCapacity, "tasks: "+err.Error())
	}
	return writeJSON(resultPointer, resultCapacity, result)
}

//go:wasmexport stado_plugin_command
func stadoPluginCommand(inputPointer, inputLength, resultPointer, resultCapacity int32) int32 {
	appMu.Lock()
	defer appMu.Unlock()
	var envelope commandEnvelope
	if err := decodeStrict(wasmBytes(inputPointer, inputLength), &envelope); err != nil {
		return writeCommandError(resultPointer, resultCapacity, "tasks command envelope: "+err.Error())
	}
	if envelope.Schema != "stado.dev/application-command/v1" || envelope.Application == "" || envelope.Sequence == 0 || envelope.Command != "tasks" || envelope.Anchor.SessionID == "" || envelope.Anchor.SessionGeneration == 0 {
		return writeCommandError(resultPointer, resultCapacity, "tasks command: invalid authenticated envelope")
	}
	if err := ensureLegacyMigrated(); err != nil {
		return writeCommandError(resultPointer, resultCapacity, "tasks legacy migration blocked: "+err.Error())
	}
	message, err := runCommand(strings.TrimSpace(envelope.Args))
	if err != nil {
		return writeCommandError(resultPointer, resultCapacity, "tasks command: "+err.Error())
	}
	return writeJSON(resultPointer, resultCapacity, map[string]string{"status": "ok", "message": message})
}

func runTool(args toolArgs) (any, error) {
	action := strings.ToLower(strings.TrimSpace(args.Action))
	switch action {
	case "create":
		if args.Title == nil {
			return nil, errors.New("title is required for create")
		}
		status, body := "open", ""
		if args.Status != nil {
			status = *args.Status
		}
		if args.Body != nil {
			body = *args.Body
		}
		if err := validateLogicalKey(args.IdempotencyKey); err != nil {
			return nil, err
		}
		task, err := createTask(taskData{Title: *args.Title, Body: body, Status: status}, "model:"+args.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task": task}, nil
	case "list":
		tasks, err := listTasks()
		if err != nil {
			return nil, err
		}
		if args.Status != nil {
			status := strings.ToLower(strings.TrimSpace(*args.Status))
			if _, err := normalizeTask(taskData{Title: "filter", Status: status}); err != nil {
				return nil, err
			}
			filtered := tasks[:0]
			for _, task := range tasks {
				if task.Status == status {
					filtered = append(filtered, task)
				}
			}
			tasks = filtered
		}
		limit := 50
		if args.Limit != nil && *args.Limit > 0 && *args.Limit <= 50 {
			limit = *args.Limit
		}
		count := len(tasks)
		if len(tasks) > limit {
			tasks = tasks[:limit]
		}
		return map[string]any{"tasks": tasks, "count": count, "truncated": count > limit}, nil
	case "read":
		task, _, err := findTask(args.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task": task}, nil
	case "update", "edit":
		if args.Title == nil && args.Body == nil && args.Status == nil {
			return nil, errors.New("update requires at least one of title, body, or status")
		}
		task, data, err := findTask(args.ID)
		if err != nil {
			return nil, err
		}
		before := data
		if args.Title != nil {
			data.Title = *args.Title
		}
		if args.Body != nil {
			data.Body = *args.Body
		}
		if args.Status != nil {
			data.Status = *args.Status
		}
		updated, err := editTask(task, before, data)
		if err != nil {
			return nil, err
		}
		return map[string]any{"task": updated}, nil
	case "delete":
		task, data, err := findTask(args.ID)
		if err != nil {
			return nil, err
		}
		before := data
		data.Deleted = true
		if _, err := editTask(task, before, data); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": task.ID}, nil
	default:
		return nil, fmt.Errorf("unknown task action %q", args.Action)
	}
}

func runCommand(args string) (string, error) {
	parts := strings.Fields(args)
	if len(parts) != 0 {
		switch parts[0] {
		case "list":
			tasks, err := listTasks()
			if err != nil {
				return "", err
			}
			return formatTaskList(tasks, 50), nil
		case "add":
			if len(parts) < 4 || parts[1] != "--key" {
				return "", errors.New("usage: /tasks add --key <idempotency-key> <title>")
			}
			if err := validateLogicalKey(parts[2]); err != nil {
				return "", err
			}
			title := strings.TrimSpace(strings.Join(parts[3:], " "))
			task, err := createTask(taskData{Title: title, Status: "open"}, "operator:"+parts[2])
			if err != nil {
				return "", err
			}
			return "created task " + task.ID + ": " + task.Title, nil
		case "read":
			if len(parts) != 2 {
				return "", errors.New("usage: /tasks read <id>")
			}
			task, _, err := findTask(parts[1])
			if err != nil {
				return "", err
			}
			return formatTask(task), nil
		case "delete":
			if len(parts) != 2 {
				return "", errors.New("usage: /tasks delete <id>")
			}
			task, data, err := findTask(parts[1])
			if err != nil {
				return "", err
			}
			before := data
			data.Deleted = true
			if _, err := editTask(task, before, data); err != nil {
				return "", err
			}
			return "deleted task " + task.ID, nil
		default:
			return "", errors.New("usage: /tasks [list | add --key <idempotency-key> <title> | read <id> | delete <id>]")
		}
	}
	return interactiveTasks()
}

func interactiveTasks() (string, error) {
	tasks, err := listTasks()
	if err != nil {
		return "", err
	}
	options := []chooseOption{{ID: "add", Label: "Add task", Input: &chooseInput{Validator: chooseValidator{Kind: "length", Spec: "1:256"}}}}
	for i, task := range tasks {
		if i == 98 {
			break
		}
		options = append(options, chooseOption{ID: "task:" + task.ID, Label: "[" + task.Status + "] " + task.Title})
	}
	response, err := choose("Tasks (select a task or add one; use /tasks list when more than 98 are live)", options)
	if err != nil {
		return "", err
	}
	if response.Cancelled || len(response.Selected) == 0 {
		return "tasks unchanged", nil
	}
	selected := response.Selected[0]
	if selected == "add" {
		keyResponse, err := choose("Choose a stable idempotency key (reuse it if this create must be retried)", []chooseOption{{ID: "create", Label: "Create task", Input: &chooseInput{Validator: chooseValidator{Kind: "length", Spec: "1:64"}}}})
		if err != nil {
			return "", err
		}
		if keyResponse.Cancelled || len(keyResponse.Selected) == 0 {
			return "tasks unchanged", nil
		}
		if err := validateLogicalKey(keyResponse.InputValue); err != nil {
			return "", err
		}
		task, err := createTask(taskData{Title: response.InputValue, Status: "open"}, "operator:"+keyResponse.InputValue)
		if err != nil {
			return "", err
		}
		return "created task " + task.ID + ": " + task.Title, nil
	}
	id := strings.TrimPrefix(selected, "task:")
	task, data, err := findTask(id)
	if err != nil {
		return "", err
	}
	action, err := choose("Task: "+task.Title, []chooseOption{
		{ID: "open", Label: "Mark open"}, {ID: "in_progress", Label: "Mark in progress"}, {ID: "done", Label: "Mark done"},
		{ID: "title", Label: "Edit title", Input: &chooseInput{Default: task.Title, Validator: chooseValidator{Kind: "length", Spec: "1:256"}}},
		{ID: "delete", Label: "Delete (logical tombstone)"},
	})
	if err != nil {
		return "", err
	}
	if action.Cancelled || len(action.Selected) == 0 {
		return "tasks unchanged", nil
	}
	before := data
	switch action.Selected[0] {
	case "open", "in_progress", "done":
		data.Status = action.Selected[0]
	case "title":
		data.Title = action.InputValue
	case "delete":
		data.Deleted = true
	default:
		return "", errors.New("unknown task action selected")
	}
	updated, err := editTask(task, before, data)
	if err != nil {
		return "", err
	}
	if data.Deleted {
		return "deleted task " + task.ID, nil
	}
	return "updated task " + updated.ID + ": " + updated.Title + " [" + updated.Status + "]", nil
}

func createTask(data taskData, logicalKey string) (taskView, error) {
	var err error
	data, err = normalizeTask(data)
	if err != nil {
		return taskView{}, err
	}
	keyTag := createTag(logicalKey)
	prior, err := queryArtifacts([]string{keyTag}, 1)
	if err != nil {
		return taskView{}, err
	}
	if len(prior) == 1 {
		view, got, err := artifactTask(prior[0])
		if err != nil || got != data || got.Deleted {
			return taskView{}, errors.New("task create idempotency key already belongs to different or deleted data")
		}
		return view, nil
	}
	live, err := listTasks()
	if err != nil {
		return taskView{}, err
	}
	if len(live) >= maxTasks {
		return taskView{}, fmt.Errorf("task limit reached (%d)", maxTasks)
	}
	raw, err := callHostJSON(stadoArtifactPropose, map[string]any{
		"idempotency_key": "task-create:" + logicalKey, "kind": "task", "scope": "global", "tags": []string{"stado:tasks", "stado:tasks:live", keyTag}, "data": data,
	})
	if err != nil {
		return taskView{}, err
	}
	var item artifact
	if err := decodeStrict(raw, &item); err != nil {
		return taskView{}, errors.New("artifact broker returned invalid task candidate")
	}
	view, got, err := artifactTask(item)
	if err != nil || got != data {
		return taskView{}, errors.New("artifact broker did not preserve exact task data")
	}
	return view, nil
}

func editTask(current taskView, before, data taskData) (taskView, error) {
	data, changed, err := taskEditChange(before, data)
	if err != nil {
		return taskView{}, err
	}
	if !changed {
		return current, nil
	}
	encoded, _ := json.Marshal(data)
	tags := []string{"stado:tasks", "stado:tasks:live"}
	if data.Deleted {
		tags = []string{"stado:tasks", "stado:tasks:deleted"}
	}
	for _, tag := range current.Tags {
		if strings.HasPrefix(tag, "stado:tasks-legacy:") || strings.HasPrefix(tag, "stado:tasks-create:") {
			tags = append(tags, tag)
		}
	}
	raw, err := callHostJSON(stadoArtifactEdit, map[string]any{
		"idempotency_key": "task-edit:" + current.ID + ":v" + strconv.FormatUint(current.Version, 10) + ":" + digestBytes(encoded)[:24],
		"kind":            "task", "id": current.ID, "expected_version": current.Version, "tags": tags, "data": data,
	})
	if err != nil {
		return taskView{}, err
	}
	var item artifact
	if err := decodeStrict(raw, &item); err != nil {
		return taskView{}, errors.New("artifact broker returned invalid edited task candidate")
	}
	view, got, err := artifactTask(item)
	if err != nil || item.ID != current.ID || item.Version != current.Version+1 || got != data {
		return taskView{}, errors.New("artifact broker did not preserve exact edited task data")
	}
	return view, nil
}

func findTask(id string) (taskView, taskData, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 256 {
		return taskView{}, taskData{}, errors.New("task id is required and must be at most 256 bytes")
	}
	items, err := queryArtifacts([]string{"stado:tasks:live"}, maxTasks)
	if err != nil {
		return taskView{}, taskData{}, err
	}
	for _, item := range items {
		view, data, err := artifactTask(item)
		if err != nil {
			return taskView{}, taskData{}, err
		}
		if !data.Deleted && (view.ID == id || data.LegacyID == id) {
			return view, data, nil
		}
	}
	return taskView{}, taskData{}, fmt.Errorf("task %q not found", id)
}

func listTasks() ([]taskView, error) {
	items, err := queryArtifacts([]string{"stado:tasks:live"}, maxTasks)
	if err != nil {
		return nil, err
	}
	tasks := make([]taskView, 0, len(items))
	for _, item := range items {
		view, data, err := artifactTask(item)
		if err != nil {
			return nil, err
		}
		if item.Authority != "candidate" {
			return nil, errors.New("live task query returned a non-current artifact version")
		}
		if !data.Deleted {
			tasks = append(tasks, view)
		}
	}
	sortTasks(tasks)
	return tasks, nil
}

func queryArtifacts(tags []string, maxTotal int) ([]artifact, error) {
	if maxTotal <= 0 || maxTotal > maxTasks {
		return nil, errors.New("invalid task query aggregate bound")
	}
	for restart := 0; restart < 4; restart++ {
		var all []artifact
		offset, digest := 0, ""
		for {
			request := map[string]any{"kind": "self#task", "active_only": false, "max_items": 50, "page_offset": offset}
			if len(tags) != 0 {
				request["tags"] = tags
			}
			if offset > 0 {
				request["page_digest"] = digest
			}
			raw, err := callHostJSON(stadoArtifactQuery, request)
			if err != nil {
				if strings.Contains(err.Error(), "projection changed") {
					break
				}
				return nil, err
			}
			var response queryResponse
			if err := decodeStrict(raw, &response); err != nil || !validPageDigest(response.PageDigest) {
				return nil, errors.New("artifact broker returned invalid paginated task projection")
			}
			if digest != "" && response.PageDigest != digest {
				break
			}
			digest = response.PageDigest
			all, err = appendBoundedArtifacts(all, response.Items, maxTotal)
			if err != nil {
				return nil, err
			}
			if response.Complete {
				return all, nil
			}
			if response.NextOffset <= offset {
				return nil, errors.New("artifact pagination did not advance")
			}
			offset = response.NextOffset
		}
	}
	return nil, errors.New("task artifact projection changed during four pagination attempts; retry")
}

func ensureLegacyMigrated() error {
	stateDir, err := cfgStateDir()
	if err != nil {
		return err
	}
	dir := path.Join(stateDir, "tasks")
	source := path.Join(dir, "tasks.json")
	raw, exists, err := readBoundedFile(source)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		var receipt migrationReceipt
		if err := decodeStrict(raw, &receipt); err != nil || validateReceipt(receipt) != nil {
			return errors.New("migration receipt is invalid; recover the legacy files manually")
		}
		archiveRaw, archiveExists, err := readBoundedFile(path.Join(dir, receipt.ArchiveFile))
		if err != nil || !archiveExists || digestBytes(archiveRaw) != receipt.SHA256 {
			return errors.New("legacy tasks archive is missing or does not match its receipt; recover manually")
		}
		legacy, err := decodeLegacy(archiveRaw)
		if err != nil || len(legacy) != receipt.Count {
			return errors.New("legacy tasks archive no longer validates against its receipt")
		}
		items, err := queryExactRefs(receipt.Refs)
		if err != nil {
			return err
		}
		return verifyReceiptRefs(receipt, legacy, items)
	}
	if !strings.HasPrefix(trimmed, "[") {
		return errors.New("legacy tasks.json is neither a task array nor a valid migration receipt")
	}
	legacy, err := decodeLegacy(raw)
	if err != nil {
		return err
	}
	digest := digestBytes(raw)
	tag := migrationTag(digest)
	proposed := make([]artifact, 0, len(legacy))
	for _, old := range legacy {
		data := legacyData(old)
		response, err := callHostJSON(stadoArtifactPropose, map[string]any{
			"idempotency_key": "tasks-legacy:" + digest[:24] + ":" + digestBytes([]byte(old.ID))[:24],
			"kind":            "task", "scope": "global", "tags": []string{"stado:tasks", "stado:tasks:live", tag}, "data": data,
		})
		if err != nil {
			return fmt.Errorf("propose legacy task %q: %w", old.ID, err)
		}
		var item artifact
		if err := decodeStrict(response, &item); err != nil {
			return fmt.Errorf("propose legacy task %q: invalid broker acknowledgement", old.ID)
		}
		_, got, err := artifactTask(item)
		if err != nil || got != data {
			return fmt.Errorf("propose legacy task %q: broker did not preserve exact data", old.ID)
		}
		proposed = append(proposed, item)
	}
	refs, err := migrationRefs(legacy, proposed)
	if err != nil {
		return err
	}
	items, err := queryExactRefs(refs)
	if err != nil {
		return err
	}
	receipt := migrationReceipt{Schema: migrationSchema, SHA256: digest, ArchiveFile: "tasks.json.archive-" + digest, Tag: tag, Count: len(legacy), Refs: refs}
	if err := verifyReceiptRefs(receipt, legacy, items); err != nil {
		return err
	}
	archiveFile := receipt.ArchiveFile
	archivePath := path.Join(dir, archiveFile)
	if err := writeExactFile(archivePath, raw); err != nil {
		return fmt.Errorf("write verified legacy archive: %w", err)
	}
	archiveRaw, archiveExists, err := readBoundedFile(archivePath)
	if err != nil || !archiveExists || !bytesEqual(archiveRaw, raw) || digestBytes(archiveRaw) != digest {
		return errors.New("legacy archive could not be durably read back and verified; tasks.json was left untouched")
	}
	currentRaw, currentExists, err := readBoundedFile(source)
	if err != nil || !currentExists || !bytesEqual(currentRaw, raw) || digestBytes(currentRaw) != digest {
		return errors.New("legacy tasks.json changed immediately before receipt replacement; archive remains recoverable and no migration was acknowledged")
	}
	receiptRaw, _ := json.Marshal(receipt)
	if err := writeExactFile(source, receiptRaw); err != nil {
		return fmt.Errorf("write migration receipt after artifact and archive verification: %w", err)
	}
	verifiedReceipt, receiptExists, err := readBoundedFile(source)
	if err != nil || !receiptExists || !bytesEqual(verifiedReceipt, receiptRaw) {
		return errors.New("migration receipt could not be durably read back and verified; original tasks.json and archive remain recoverable")
	}
	return nil
}

func queryExactRefs(refs []migrationRef) ([]artifact, error) {
	all := make([]artifact, 0, len(refs))
	for offset := 0; offset < len(refs); offset += 32 {
		end := offset + 32
		if end > len(refs) {
			end = len(refs)
		}
		wireRefs := make([]map[string]any, 0, end-offset)
		for _, ref := range refs[offset:end] {
			wireRefs = append(wireRefs, map[string]any{"id": ref.ID, "version": ref.Version})
		}
		raw, err := callHostJSON(stadoArtifactQuery, map[string]any{"kind": "self#task", "refs": wireRefs, "active_only": false, "max_items": len(wireRefs)})
		if err != nil {
			return nil, err
		}
		var response queryResponse
		if err := decodeStrict(raw, &response); err != nil || !response.Complete || !validPageDigest(response.PageDigest) || len(response.Items) != len(wireRefs) {
			return nil, errors.New("artifact broker did not return the exact bounded migration proof refs")
		}
		all = append(all, response.Items...)
	}
	return all, nil
}

func cfgStateDir() (string, error) {
	buffer := make([]byte, 4096)
	n := stadoCfgStateDir(slicePointer(buffer))
	if n <= 0 || int(n) > len(buffer) {
		return "", errors.New("cfg:state_dir is unavailable")
	}
	return string(buffer[:n]), nil
}

func readBoundedFile(name string) ([]byte, bool, error) {
	statBuffer := make([]byte, 1024)
	namePointer, nameLength := slicePointer([]byte(name))
	statPointer, statCapacity := slicePointer(statBuffer)
	n := stadoFSStat(namePointer, nameLength, statPointer, statCapacity)
	if n < 0 {
		message := fsLastError()
		if strings.Contains(strings.ToLower(message), "no such file") || strings.Contains(strings.ToLower(message), "not found") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("stat %s: %s", name, message)
	}
	if n == 0 || int(n) > len(statBuffer) {
		return nil, false, fmt.Errorf("stat %s returned an invalid response", name)
	}
	var stat fsStat
	if err := decodeStrict(statBuffer[:n], &stat); err != nil || stat.Type != "file" || stat.Size < 0 {
		return nil, false, fmt.Errorf("legacy path %s is not a regular file", name)
	}
	if stat.Size > maxLegacyFileBytes {
		return nil, false, fmt.Errorf("legacy file %s is %d bytes, exceeding the exact %d-byte WASM import ceiling; no bytes were read, truncated, overwritten, archived, or acknowledged—copy it aside and migrate it manually", name, stat.Size, maxLegacyFileBytes)
	}
	capacity := int(stat.Size)
	if capacity == 0 {
		capacity = 1
	}
	data := make([]byte, capacity)
	dataPointer, dataCapacity := slicePointer(data)
	n = stadoFSRead(namePointer, nameLength, dataPointer, dataCapacity)
	if n < 0 {
		return nil, false, fmt.Errorf("read %s: %s", name, fsLastError())
	}
	if int64(n) != stat.Size {
		return nil, false, fmt.Errorf("read %s changed during migration (stat %d bytes, read %d); retry", name, stat.Size, n)
	}
	return append([]byte(nil), data[:n]...), true, nil
}

func writeExactFile(name string, data []byte) error {
	namePointer, nameLength := slicePointer([]byte(name))
	dataPointer, dataLength := slicePointer(data)
	n := stadoFSWrite(namePointer, nameLength, dataPointer, dataLength)
	if n < 0 {
		return errors.New(fsLastError())
	}
	if int(n) != len(data) {
		return fmt.Errorf("host reported %d of %d bytes written", n, len(data))
	}
	return nil
}

func fsLastError() string {
	buffer := make([]byte, 4096)
	n := stadoFSLastError(slicePointer(buffer))
	if n <= 0 || int(n) > len(buffer) {
		return "filesystem operation failed without a host diagnostic"
	}
	return string(buffer[:n])
}

type hostJSONCall func(uint32, uint32, uint32, uint32) int32

func callHostJSON(call hostJSONCall, request any) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	response := make([]byte, 1<<20)
	requestPointer, requestLength := slicePointer(raw)
	responsePointer, responseCapacity := slicePointer(response)
	n := call(requestPointer, requestLength, responsePointer, responseCapacity)
	if n < 0 {
		size := -int(n)
		if size > len(response) {
			size = len(response)
		}
		return nil, errors.New(string(response[:size]))
	}
	if int(n) > len(response) {
		return nil, errors.New("host response exceeds buffer")
	}
	return append([]byte(nil), response[:n]...), nil
}

func choose(prompt string, options []chooseOption) (chooseResponse, error) {
	raw, err := json.Marshal(map[string]any{"prompt": prompt, "options": options, "multi": false})
	if err != nil {
		return chooseResponse{}, err
	}
	response := make([]byte, 16<<10)
	requestPointer, requestLength := slicePointer(raw)
	responsePointer, responseCapacity := slicePointer(response)
	n := stadoUIChoose(requestPointer, requestLength, responsePointer, responseCapacity)
	if n < 0 {
		size := -int64(n)
		if size <= 0 || size > int64(len(response)) {
			return chooseResponse{}, errors.New("interactive task choice returned an invalid error length")
		}
		return chooseResponse{}, errors.New(string(response[:size]))
	}
	if int64(n) > int64(len(response)) {
		return chooseResponse{}, errors.New("interactive task choice returned an invalid response length")
	}
	var decoded chooseResponse
	if err := decodeStrict(response[:n], &decoded); err != nil {
		return chooseResponse{}, err
	}
	return decoded, nil
}

func formatTaskList(tasks []taskView, limit int) string {
	if len(tasks) == 0 {
		return "No tasks."
	}
	var out strings.Builder
	count := len(tasks)
	if count > limit {
		tasks = tasks[:limit]
	}
	for _, task := range tasks {
		fmt.Fprintf(&out, "[%s] %s  %s\n", task.Status, task.ID, task.Title)
	}
	if count > limit {
		fmt.Fprintf(&out, "… %d more task(s); use the model tasks tool for filtered access", count-limit)
	}
	return strings.TrimSpace(out.String())
}

func formatTask(task taskView) string {
	return fmt.Sprintf("[%s] %s\n%s\n\n%s", task.Status, task.ID, task.Title, task.Body)
}

func validateLogicalKey(key string) error {
	if key == "" || len(key) > 64 {
		return errors.New("create requires idempotency_key with 1..64 identifier characters")
	}
	for _, char := range key {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._:-", char)) {
			return errors.New("create idempotency_key may contain only letters, digits, dot, underscore, colon, and dash")
		}
	}
	return nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}

func wasmBytes(pointer, size int32) []byte {
	if pointer == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(pointer))), int(size))
}

func slicePointer(data []byte) (uint32, uint32) {
	if len(data) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&data[0]))), uint32(len(data))
}

func writeJSON(pointer, capacity int32, value any) int32 {
	raw, err := json.Marshal(value)
	if err != nil {
		return writeError(pointer, capacity, err.Error())
	}
	if len(raw) > int(capacity) {
		return writeError(pointer, capacity, "tasks result exceeds host buffer")
	}
	copy(wasmBytes(pointer, capacity), raw)
	return int32(len(raw))
}

func writeError(pointer, capacity int32, message string) int32 {
	data := []byte(message)
	if len(data) > int(capacity) {
		data = data[:capacity]
	}
	copy(wasmBytes(pointer, capacity), data)
	if len(data) == 0 {
		return -1
	}
	return -int32(len(data))
}

// Application commands use a strict JSON result envelope. Negative lengths
// are reserved for model-tool callbacks and are rejected by RunCommand before
// it can decode a useful diagnostic.
func writeCommandError(pointer, capacity int32, message string) int32 {
	return writeJSON(pointer, capacity, map[string]string{"status": "error", "message": message})
}
