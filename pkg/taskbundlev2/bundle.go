// Package taskbundlev2 hosts the task-bundle/v2 contract (ADR-033
// section 2): the deployment-scoped, content-addressed artifact a
// 'task' IR node references. Distinct from and additive alongside
// pkg/taskbundle's task-bundle/1 (ADR-031, frozen per ADR-035 Decision
// 1) — this package never reads or writes a v1 archive, and vice versa.
//
// This is Phase 2a of the ADR-033 rollout (issue #439 step 5): enough
// of the manifest contract and safe extraction to let a trusted worker
// stage a bundle's python payload on disk before launching a
// pkg/taskharness harness process against it. Payload selection today
// only resolves the "python" runtime class — every other class in the
// schema's enum parses and validates, but SelectPythonPayload only ever
// returns a python payload, since Phase 2a is trusted-profile Python
// only (ee/roadmap decisions recorded on issue #439).
package taskbundlev2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/archiveextract"
)

// Format is the manifest format value this package understands.
const Format = "brokoli.task-bundle/v2"

// MaxArchiveBytes caps an uploaded bundle before it is stored or
// extracted, mirroring task-bundle/1's own cap (pkg/taskbundle.MaxArchiveBytes)
// until real-world v2 bundle sizes say otherwise.
const MaxArchiveBytes = 64 << 20 // 64 MiB

// MaxArchiveEntries caps the number of tar entries Extract will walk,
// for the same reason pkg/taskbundle bounds it: tar headers compress far
// better than file content, so a small gzip could otherwise force
// millions of Next() calls before either byte cap is touched.
const MaxArchiveEntries = 10000

var (
	digestRE     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	fileDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`)
)

// IsDigest reports whether d is a well-formed "sha256:"-prefixed digest
// reference, matching docs/schema/task-bundle-v2.json's digest $def.
func IsDigest(d string) bool { return digestRE.MatchString(d) }

// DigestOf returns the canonical content address of archive bytes.
func DigestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Manifest mirrors docs/schema/task-bundle-v2.json's root object exactly
// -- field names, requiredness, and the container/image if-then rule.
type Manifest struct {
	Format          string      `json:"format"`
	Name            string      `json:"name"`
	InterfaceDigest string      `json:"interface_digest"`
	SourceDigest    string      `json:"source_digest"`
	Build           *Build      `json:"build,omitempty"`
	Payloads        []Payload   `json:"payloads"`
	Files           []FileEntry `json:"files"`
}

type Build struct {
	Builder     string `json:"builder"`
	Attestation string `json:"attestation,omitempty"`
}

// Payload is one runtime-specific way to execute this bundle's task.
type Payload struct {
	ID             string     `json:"id"`
	Runtime        string     `json:"runtime"`
	Requires       string     `json:"requires,omitempty"`
	OS             string     `json:"os"`
	Arch           string     `json:"arch"`
	Entrypoint     Entrypoint `json:"entrypoint"`
	Effects        string     `json:"effects"`
	DependencyLock string     `json:"dependency_lock,omitempty"`
	PayloadDigest  string     `json:"payload_digest"`
	Image          *OCIImage  `json:"image,omitempty"`
}

// RuntimeClass values the schema enumerates.
const (
	RuntimePython    = "python"
	RuntimeNode      = "node"
	RuntimeNative    = "native"
	RuntimeContainer = "container"
	RuntimeWASI      = "wasi"
	RuntimeJVM       = "jvm"
)

// Effect class values the schema enumerates (ADR-033 section 14).
const (
	EffectPure              = "pure"
	EffectIdempotentWithKey = "idempotent_with_key"
	EffectNonIdempotent     = "non_idempotent"
)

// Entrypoint is a oneOf over three grammars (docs/schema/task-bundle-v2.json's
// "entrypoint" $def): exactly one of {Module+Symbol}, {Executable}, or
// {Command[+Args]} may be populated.
type Entrypoint struct {
	Module     string   `json:"module,omitempty"`
	Symbol     string   `json:"symbol,omitempty"`
	Executable string   `json:"executable,omitempty"`
	Command    []string `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
}

type OCIImage struct {
	Digest    string `json:"digest"`
	Reference string `json:"reference,omitempty"`
}

type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Validate checks the manifest is internally consistent and within what
// this package can select a payload from. It does not inspect the
// filesystem -- Extract does that separately, against the manifest this
// returned no error for.
func (m *Manifest) Validate() error {
	if m.Format != Format {
		return fmt.Errorf("unsupported task bundle format %q (this package understands %q)", m.Format, Format)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("task bundle manifest declares no name")
	}
	if !IsDigest(m.InterfaceDigest) {
		return fmt.Errorf("task bundle interface_digest %q is not a well-formed sha256 digest", m.InterfaceDigest)
	}
	if !IsDigest(m.SourceDigest) {
		return fmt.Errorf("task bundle source_digest %q is not a well-formed sha256 digest", m.SourceDigest)
	}
	if m.Build != nil && strings.TrimSpace(m.Build.Builder) == "" {
		return fmt.Errorf("task bundle build.builder must not be empty when build provenance is present")
	}
	if len(m.Payloads) == 0 {
		return fmt.Errorf("task bundle manifest declares no payloads")
	}
	seenPayload := make(map[string]bool, len(m.Payloads))
	for i := range m.Payloads {
		if err := m.Payloads[i].validate(); err != nil {
			return fmt.Errorf("payload %d: %w", i, err)
		}
		if seenPayload[m.Payloads[i].ID] {
			return fmt.Errorf("payload id %q is declared more than once", m.Payloads[i].ID)
		}
		seenPayload[m.Payloads[i].ID] = true
	}
	seenFile := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		rel, ok := cleanRelativePath(f.Path)
		if !ok {
			return fmt.Errorf("task bundle file %q must be a clean relative path inside the bundle", f.Path)
		}
		if seenFile[rel] {
			return fmt.Errorf("task bundle file %q is listed twice", rel)
		}
		seenFile[rel] = true
		if !fileDigestRE.MatchString(f.SHA256) {
			return fmt.Errorf("task bundle file %q has a malformed sha256 %q", f.Path, f.SHA256)
		}
	}
	return nil
}

func (p *Payload) validate() error {
	if !identifierRE.MatchString(p.ID) {
		return fmt.Errorf("id %q is not a valid identifier", p.ID)
	}
	switch p.Runtime {
	case RuntimePython, RuntimeNode, RuntimeNative, RuntimeContainer, RuntimeWASI, RuntimeJVM:
	default:
		return fmt.Errorf("unknown runtime class %q", p.Runtime)
	}
	if strings.TrimSpace(p.OS) == "" {
		return fmt.Errorf("os must not be empty (use \"any\" for platform-independent payloads)")
	}
	if strings.TrimSpace(p.Arch) == "" {
		return fmt.Errorf("arch must not be empty (use \"any\" for platform-independent payloads)")
	}
	switch p.Effects {
	case EffectPure, EffectIdempotentWithKey, EffectNonIdempotent:
	default:
		return fmt.Errorf("unknown effect class %q", p.Effects)
	}
	if !IsDigest(p.PayloadDigest) {
		return fmt.Errorf("payload_digest %q is not a well-formed sha256 digest", p.PayloadDigest)
	}
	if err := p.Entrypoint.validate(); err != nil {
		return fmt.Errorf("entrypoint: %w", err)
	}
	if p.Runtime == RuntimeContainer && p.Image == nil {
		return fmt.Errorf("runtime \"container\" requires an image reference")
	}
	return nil
}

func (e *Entrypoint) validate() error {
	managed := e.Module != "" || e.Symbol != ""
	native := e.Executable != ""
	oci := len(e.Command) > 0
	count := 0
	for _, v := range []bool{managed, native, oci} {
		if v {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("must set exactly one of {module+symbol}, {executable}, or {command}")
	}
	if managed && (e.Module == "" || e.Symbol == "") {
		return fmt.Errorf("a managed-language entrypoint requires both module and symbol")
	}
	return nil
}

// cleanRelativePath validates a slash path as relative and inside the
// bundle root, returning the normalized slash form. Identical policy to
// pkg/taskbundle's own helper -- kept as an unexported duplicate rather
// than shared, since task-bundle/1 stays frozen and independent.
func cleanRelativePath(name string) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if clean == "." {
		return "", false
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", false
	}
	return clean, true
}

// SelectPythonPayload returns the manifest's python payload matching
// this host's OS/arch (or declared "any"), or an error naming why none
// qualifies. Phase 2a is trusted-profile Python only -- selecting any
// other runtime class is Phase 4's job (the Node reference adapter) and
// beyond.
func SelectPythonPayload(m *Manifest) (*Payload, error) {
	var reasons []string
	for i := range m.Payloads {
		p := &m.Payloads[i]
		if p.Runtime != RuntimePython {
			reasons = append(reasons, fmt.Sprintf("payload %q: runtime %q, not python", p.ID, p.Runtime))
			continue
		}
		if !PlatformMatches(p.OS, p.Arch) {
			reasons = append(reasons, fmt.Sprintf("payload %q: built for %s/%s, host is %s/%s", p.ID, p.OS, p.Arch, runtime.GOOS, runtime.GOARCH))
			continue
		}
		return p, nil
	}
	if len(reasons) == 0 {
		return nil, fmt.Errorf("manifest has no payloads")
	}
	return nil, fmt.Errorf("no python payload is selectable on this host (%s/%s):\n  %s", runtime.GOOS, runtime.GOARCH, strings.Join(reasons, "\n  "))
}

// FindPayload returns the manifest's payload with the given ID, or
// ok=false if none matches -- used to re-locate a previously resolved
// and pinned payload (ADR-033 section 4) rather than re-selecting one
// fresh, and to check whether that pinned payload is still runnable on
// the current host via PlatformMatches.
func FindPayload(m *Manifest, id string) (*Payload, bool) {
	for i := range m.Payloads {
		if m.Payloads[i].ID == id {
			return &m.Payloads[i], true
		}
	}
	return nil, false
}

// PlatformMatches reports whether a payload declared for osName/arch
// ("any" or empty meaning platform-independent) can run on this host.
func PlatformMatches(osName, arch string) bool {
	osOK := osName == "" || osName == "any" || osName == runtime.GOOS
	archOK := arch == "" || arch == "any" || arch == runtime.GOARCH
	return osOK && archOK
}

// Extract unpacks a task-bundle/v2 archive into destRoot and returns its
// validated manifest, having confirmed every declared file is present
// on disk with the size and digest the manifest claims (section 2 rule
// 3's digest coverage) -- not merely present, unlike task-bundle/1's
// weaker existence-only check.
func Extract(b []byte, destRoot string) (*Manifest, error) {
	if int64(len(b)) > MaxArchiveBytes {
		return nil, fmt.Errorf("task bundle archive %d bytes exceeds the %d-byte cap", len(b), int64(MaxArchiveBytes))
	}
	if err := archiveextract.Extract(bytes.NewReader(b), destRoot, archiveextract.Options{
		MaxTotalBytes: MaxArchiveBytes,
		MaxEntries:    MaxArchiveEntries,
	}); err != nil {
		return nil, fmt.Errorf("extract task bundle: %w", err)
	}

	raw, err := os.ReadFile(filepath.Join(destRoot, "manifest.json")) // #nosec G304 -- inside the just-extracted destRoot
	if err != nil {
		return nil, fmt.Errorf("task bundle archive contains no manifest.json at its root: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse task bundle manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	for _, f := range m.Files {
		rel, _ := cleanRelativePath(f.Path)
		full := filepath.Join(destRoot, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("task bundle manifest declares file %q but it is not present in the archive", f.Path)
		}
		if info.Size() != f.Size {
			return nil, fmt.Errorf("task bundle file %q is %d bytes, manifest declares %d", f.Path, info.Size(), f.Size)
		}
		got, err := hashFile(full)
		if err != nil {
			return nil, fmt.Errorf("hash task bundle file %q: %w", f.Path, err)
		}
		if !strings.EqualFold(got, f.SHA256) {
			return nil, fmt.Errorf("task bundle file %q failed integrity verification: manifest says sha256 %s, content hashes to %s", f.Path, f.SHA256, got)
		}
	}
	return &m, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- path is enumerated from the manifest of the archive just extracted under our own destRoot
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
