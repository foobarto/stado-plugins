// distcheck is a public-key-only integrity gate for committed plugin bundles.
// It deliberately has no third-party dependencies so CI never needs a signing
// secret or a second trust bootstrap merely to verify release artifacts.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxManifestBytes = 1 << 20

var pluginPathRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type inventory struct {
	SchemaVersion int               `json:"schema_version"`
	Plugins       []inventoryPlugin `json:"plugins"`
}

type inventoryPlugin struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Version string `json:"version"`
}

type manifestEnvelope struct {
	Name            string          `json:"name"`
	Version         string          `json:"version"`
	AuthorPubkeyFpr string          `json:"author_pubkey_fpr"`
	WASMSHA256      string          `json:"wasm_sha256"`
	Capabilities    []string        `json:"capabilities"`
	Tools           []manifestTool  `json:"tools"`
	Lifecycle       json.RawMessage `json:"lifecycle"`
}

type manifestTool struct {
	Name         string    `json:"name"`
	Capabilities *[]string `json:"capabilities"`
}

type decodedManifest struct {
	Envelope manifestEnvelope
	Tree     map[string]any
}

func main() {
	root := "."
	if len(os.Args) > 2 {
		fatalf("usage: go run ./tools/distcheck/main.go [repo-root]")
	}
	if len(os.Args) == 2 {
		root = os.Args[1]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}

	anchor, fingerprint, err := loadAnchor(filepath.Join(absRoot, ".stado", "author.pub"))
	if err != nil {
		fatalf("load public trust anchor: %v", err)
	}
	inv, err := loadInventory(filepath.Join(absRoot, "plugin-inventory.json"))
	if err != nil {
		fatalf("load inventory: %v", err)
	}

	var failures []error
	seen := make(map[string]bool, len(inv.Plugins))
	previous := ""
	released := 0
	for i, plugin := range inv.Plugins {
		if !pluginPathRE.MatchString(plugin.Path) {
			failures = append(failures, fmt.Errorf("inventory plugin %d has invalid path %q", i, plugin.Path))
			continue
		}
		if seen[plugin.Path] {
			failures = append(failures, fmt.Errorf("inventory repeats plugin %q", plugin.Path))
			continue
		}
		seen[plugin.Path] = true
		if previous != "" && plugin.Path <= previous {
			failures = append(failures, fmt.Errorf("inventory is not strictly path-sorted at %q", plugin.Path))
		}
		previous = plugin.Path
		if plugin.Version == "" {
			failures = append(failures, fmt.Errorf("inventory plugin %q has empty version", plugin.Path))
		}
		switch plugin.Status {
		case "released":
			released++
		case "staged", "source-only":
		default:
			failures = append(failures, fmt.Errorf("inventory plugin %q has invalid status %q", plugin.Path, plugin.Status))
			continue
		}

		pluginRoot := filepath.Join(absRoot, plugin.Path)
		templatePath := filepath.Join(pluginRoot, "plugin.manifest.template.json")
		template, loadErr := loadManifest(templatePath)
		if loadErr != nil {
			failures = append(failures, fmt.Errorf("%s: template: %w", plugin.Path, loadErr))
			continue
		}
		if err := validateManifestShape(template.Envelope); err != nil {
			failures = append(failures, fmt.Errorf("%s: template: %w", plugin.Path, err))
		}
		if template.Envelope.Version != plugin.Version {
			failures = append(failures, fmt.Errorf("%s: inventory version %q != template version %q", plugin.Path, plugin.Version, template.Envelope.Version))
		}
		if template.Envelope.AuthorPubkeyFpr != "" || template.Envelope.WASMSHA256 != "" {
			failures = append(failures, fmt.Errorf("%s: template must leave signer fingerprint and wasm digest empty", plugin.Path))
		}

		distRoot := filepath.Join(pluginRoot, "dist")
		if plugin.Status != "released" {
			if files := presentBundleFiles(distRoot); len(files) != 0 {
				failures = append(failures, fmt.Errorf("%s: %s plugin carries release bundle files: %s", plugin.Path, plugin.Status, strings.Join(files, ", ")))
			}
			continue
		}
		if err := verifyReleased(plugin, template, distRoot, anchor, fingerprint); err != nil {
			failures = append(failures, err)
		}
	}

	templates, err := filepath.Glob(filepath.Join(absRoot, "*", "plugin.manifest.template.json"))
	if err != nil {
		failures = append(failures, fmt.Errorf("discover templates: %w", err))
	} else {
		for _, path := range templates {
			name := filepath.Base(filepath.Dir(path))
			if !seen[name] {
				failures = append(failures, fmt.Errorf("plugin %q has a template but no inventory entry", name))
			}
		}
		if len(templates) != len(seen) {
			failures = append(failures, fmt.Errorf("inventory/template count mismatch: %d entries, %d templates", len(seen), len(templates)))
		}
	}

	if len(failures) != 0 {
		for _, failure := range failures {
			fmt.Fprintf(os.Stderr, "distcheck: %v\n", failure)
		}
		os.Exit(1)
	}
	fmt.Printf("signed plugin integrity passed: %d inventoried, %d released, anchor=%s\n", len(inv.Plugins), released, fingerprint)
}

func verifyReleased(plugin inventoryPlugin, template decodedManifest, distRoot string, anchor ed25519.PublicKey, fingerprint string) error {
	manifestPath := filepath.Join(distRoot, "plugin.manifest.json")
	dist, err := loadManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("%s: signed manifest: %w", plugin.Path, err)
	}
	if err := validateManifestShape(dist.Envelope); err != nil {
		return fmt.Errorf("%s: signed manifest: %w", plugin.Path, err)
	}
	if dist.Envelope.Version != plugin.Version {
		return fmt.Errorf("%s: signed version %q != inventory version %q", plugin.Path, dist.Envelope.Version, plugin.Version)
	}
	if dist.Envelope.Name != template.Envelope.Name {
		return fmt.Errorf("%s: signed name %q != template name %q", plugin.Path, dist.Envelope.Name, template.Envelope.Name)
	}
	if dist.Envelope.AuthorPubkeyFpr != fingerprint {
		return fmt.Errorf("%s: signed fingerprint %q != repository anchor %q", plugin.Path, dist.Envelope.AuthorPubkeyFpr, fingerprint)
	}
	if !sameUnsignedManifest(template.Tree, dist.Tree) {
		return fmt.Errorf("%s: signed manifest does not match its source template after removing signer-derived fields", plugin.Path)
	}

	wasmPath := filepath.Join(distRoot, "plugin.wasm")
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("%s: read wasm: %w", plugin.Path, err)
	}
	digest := sha256.Sum256(wasm)
	if got := hex.EncodeToString(digest[:]); got != dist.Envelope.WASMSHA256 {
		return fmt.Errorf("%s: wasm digest %s != signed digest %s", plugin.Path, got, dist.Envelope.WASMSHA256)
	}

	sidecar, err := os.ReadFile(filepath.Join(distRoot, "author.pubkey"))
	if err != nil {
		return fmt.Errorf("%s: read dist author.pubkey: %w", plugin.Path, err)
	}
	if strings.TrimSpace(string(sidecar)) != hex.EncodeToString(anchor) {
		return fmt.Errorf("%s: dist author.pubkey does not match repository anchor", plugin.Path)
	}

	sigText, err := os.ReadFile(filepath.Join(distRoot, "plugin.manifest.sig"))
	if err != nil {
		return fmt.Errorf("%s: read signature: %w", plugin.Path, err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil {
		return fmt.Errorf("%s: decode signature: %w", plugin.Path, err)
	}
	canonical, err := json.Marshal(dist.Tree)
	if err != nil {
		return fmt.Errorf("%s: canonicalize signed manifest: %w", plugin.Path, err)
	}
	if !ed25519.Verify(anchor, canonical, sig) {
		return fmt.Errorf("%s: Ed25519 signature is invalid", plugin.Path)
	}
	return nil
}

func loadInventory(path string) (inventory, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return inventory{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var inv inventory
	if err := decoder.Decode(&inv); err != nil {
		return inventory{}, err
	}
	if err := requireEOF(decoder); err != nil {
		return inventory{}, err
	}
	if inv.SchemaVersion != 1 {
		return inventory{}, fmt.Errorf("unsupported schema_version %d", inv.SchemaVersion)
	}
	return inv, nil
}

func loadAnchor(path string) (ed25519.PublicKey, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, "", err
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, "", fmt.Errorf("anchor is %d bytes, want %d", len(decoded), ed25519.PublicKeySize)
	}
	sum := sha256.Sum256(decoded)
	return ed25519.PublicKey(decoded), hex.EncodeToString(sum[:8]), nil
}

func loadManifest(path string) (decodedManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return decodedManifest{}, err
	}
	if len(raw) > maxManifestBytes {
		return decodedManifest{}, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var tree map[string]any
	if err := decoder.Decode(&tree); err != nil {
		return decodedManifest{}, err
	}
	if err := requireEOF(decoder); err != nil {
		return decodedManifest{}, err
	}
	var envelope manifestEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return decodedManifest{}, err
	}
	return decodedManifest{Envelope: envelope, Tree: tree}, nil
}

func validateManifestShape(manifest manifestEnvelope) error {
	if manifest.Name == "" || manifest.Version == "" {
		return errors.New("name and version are required")
	}
	packageCaps := make(map[string]bool, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		if capability == "" || strings.TrimSpace(capability) != capability || packageCaps[capability] {
			return fmt.Errorf("invalid or duplicate package capability %q", capability)
		}
		packageCaps[capability] = true
	}
	lifecycle := len(manifest.Lifecycle) != 0 && string(manifest.Lifecycle) != "null"
	toolNames := make(map[string]bool, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		if tool.Name == "" || toolNames[tool.Name] {
			return fmt.Errorf("empty or duplicate tool name %q", tool.Name)
		}
		toolNames[tool.Name] = true
		if lifecycle {
			if tool.Capabilities != nil {
				return fmt.Errorf("lifecycle tool %q must omit per-tool capabilities", tool.Name)
			}
			continue
		}
		if tool.Capabilities == nil {
			return fmt.Errorf("ordinary tool %q must explicitly declare capabilities", tool.Name)
		}
		seen := make(map[string]bool, len(*tool.Capabilities))
		for _, capability := range *tool.Capabilities {
			if capability == "" || strings.TrimSpace(capability) != capability || seen[capability] {
				return fmt.Errorf("tool %q has invalid or duplicate capability %q", tool.Name, capability)
			}
			seen[capability] = true
			if !packageCaps[capability] {
				return fmt.Errorf("tool %q capability %q is absent from package authority", tool.Name, capability)
			}
		}
	}
	return nil
}

func sameUnsignedManifest(template, signed map[string]any) bool {
	templateCopy := copyTree(template)
	signedCopy := copyTree(signed)
	templateCopy["author_pubkey_fpr"] = ""
	templateCopy["wasm_sha256"] = ""
	signedCopy["author_pubkey_fpr"] = ""
	signedCopy["wasm_sha256"] = ""
	left, leftErr := json.Marshal(templateCopy)
	right, rightErr := json.Marshal(signedCopy)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func copyTree(tree map[string]any) map[string]any {
	raw, _ := json.Marshal(tree)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var copied map[string]any
	_ = decoder.Decode(&copied)
	return copied
}

func presentBundleFiles(root string) []string {
	names := []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig", "author.pubkey"}
	var present []string
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			present = append(present, name)
		}
	}
	sort.Strings(present)
	return present
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "distcheck: "+format+"\n", args...)
	os.Exit(1)
}
