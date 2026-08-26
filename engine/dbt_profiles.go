package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Generating a dbt profile from a Brokoli connection (ADR-025 Phase 2,
// #353).
//
// Without this, a dbt project needs its own copy of the warehouse password
// in a profiles.yml the operator maintains by hand, next to the credentials
// Brokoli already holds and already resolves from env, Vault, or Kubernetes
// secrets. Two copies of a password is one too many, and the copy Brokoli
// does not manage is the one that gets committed.
//
// # Where the file goes, and why it matters more than the mapping
//
// A generated profile contains a plaintext password. So:
//
//   - It is written to a fresh temporary directory, never into the project.
//     A dbt project is usually a git checkout, and writing a credential
//     into one is how it ends up in a commit.
//   - The directory is 0700 and the file 0600, set at creation rather than
//     afterwards, so there is no window where it is readable.
//   - It is removed when the node finishes, including on failure.
//   - The password is never logged. Errors name the connection, never its
//     contents.
//
// # Adapters
//
// ADR-026 requires that a missing dbt adapter is named rather than
// discovered. Each connection type maps to a dbt adapter type and the
// distribution that provides it, so an absent one produces "install
// dbt-postgres" instead of a Python traceback.

// dbtAdapter describes how a Brokoli connection type appears to dbt.
type dbtAdapter struct {
	// Type is the value dbt expects in a profile's `type` field.
	Type string
	// Distribution is the pip package that provides it, named in errors so
	// an operator is told what to install rather than left to infer it.
	Distribution string
	// DefaultPort is used when the connection does not specify one.
	DefaultPort int
}

// dbtAdapters maps connection types Brokoli can drive to dbt adapters.
//
// Deliberately not a list of every adapter dbt has. A connection type is
// here only when Brokoli itself supports the backend, because a dbt run
// against a warehouse Brokoli cannot read back is a half-integration --
// ADR-025 lists exactly that as out of scope.
var dbtAdapters = map[models.ConnectionType]dbtAdapter{
	models.ConnTypePostgres: {Type: "postgres", Distribution: "dbt-postgres", DefaultPort: 5432},
	models.ConnTypeMySQL:    {Type: "mysql", Distribution: "dbt-mysql", DefaultPort: 3306},
}

// dbtProfile is a generated profile directory, and the means to remove it.
type dbtProfile struct {
	// Dir is what to pass as --profiles-dir.
	Dir string
	// ProfileName is what the project's dbt_project.yml must name in its
	// `profile:` field.
	ProfileName string
	// Target is the output name inside the profile.
	Target string
}

// Cleanup removes the generated directory and the credential in it. Safe to
// call twice.
func (p *dbtProfile) Cleanup() {
	if p == nil || p.Dir == "" {
		return
	}
	// Best-effort: a directory that cannot be removed is a leaked
	// credential worth no less than a returned error nobody would act on,
	// and Cleanup is called from a defer where there is nowhere to put one.
	_ = os.RemoveAll(p.Dir)
	p.Dir = ""
}

// generateDBTProfile writes a profiles.yml for one connection and returns
// where it went.
//
// profileName must match the project's own `profile:` field; dbt looks the
// profile up by that name and fails with its own error if it is absent,
// which is clearer than anything this could invent.
func generateDBTProfile(conn *models.Connection, profileName, targetSchema string, threads int) (*dbtProfile, error) {
	adapter, ok := dbtAdapters[conn.Type]
	if !ok {
		supported := make([]string, 0, len(dbtAdapters))
		for t := range dbtAdapters {
			supported = append(supported, string(t))
		}
		return nil, fmt.Errorf(
			"connection %q is type %q, which this build cannot generate a dbt profile for (supported: %s); "+
				"a dbt run against a warehouse Brokoli cannot read back would be a half-integration",
			conn.ConnID, conn.Type, strings.Join(supported, ", "))
	}
	if profileName == "" {
		return nil, fmt.Errorf("a dbt profile name is required; it must match the project's own profile: field")
	}
	if targetSchema == "" {
		return nil, fmt.Errorf(
			"a target schema is required: dbt needs somewhere to build models, and connection %q names a "+
				"database rather than a schema", conn.ConnID)
	}
	if conn.Schema == "" {
		return nil, fmt.Errorf("connection %q has no database name", conn.ConnID)
	}
	if threads <= 0 {
		threads = 4
	}
	port := conn.Port
	if port == 0 {
		port = adapter.DefaultPort
	}

	// os.MkdirTemp creates with 0700, so the credential is unreadable by
	// other users from the moment the directory exists rather than from
	// whenever a chmod lands. An explicit chmod here would be redundant,
	// and would also read as a too-permissive one to a static analyser
	// that cannot tell a directory needs its execute bit.
	dir, err := os.MkdirTemp("", "brokoli-dbt-profile-")
	if err != nil {
		return nil, fmt.Errorf("create profile directory: %w", err)
	}

	const target = "brokoli"
	body := fmt.Sprintf(`# Generated by Brokoli for one pipeline run. Do not edit, do not copy:
# it contains a resolved credential and is removed when the run finishes.
%s:
  target: %s
  outputs:
    %s:
      type: %s
      host: %s
      port: %d
      user: %s
      password: %s
      dbname: %s
      schema: %s
      threads: %d
`,
		yamlKey(profileName), target, target, adapter.Type,
		yamlString(conn.Host), port, yamlString(conn.Login), yamlString(conn.Password),
		yamlString(conn.Schema), yamlString(targetSchema), threads)

	path := filepath.Join(dir, "profiles.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write profile: %w", err)
	}
	return &dbtProfile{Dir: dir, ProfileName: profileName, Target: target}, nil
}

// yamlString quotes a scalar so that a password containing a colon, a hash,
// a quote, or a leading zero cannot change the document's shape.
//
// Double quotes with backslash escaping, because a password containing a
// single quote is ordinary and single-quoted YAML would need doubling
// instead -- two rules where one will do. The characters escaped are the
// ones YAML's double-quoted style gives meaning to.
func yamlString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// yamlKey quotes a mapping key the same way. A profile name comes from a
// project's own configuration and is not a credential, but it is still
// untrusted text in a structural position.
func yamlKey(s string) string { return yamlString(s) }

// dbtAdapterFor reports the adapter a connection type needs, for an error
// that tells an operator what to install.
func dbtAdapterFor(t models.ConnectionType) (dbtAdapter, bool) {
	a, ok := dbtAdapters[t]
	return a, ok
}

// generateDBTProfileForNode resolves a node's connection and writes a
// profile for it.
//
// The profile name defaults to the project's own, read from
// dbt_project.yml, because dbt matches them by name and a mismatch is a
// confusing failure rather than an obvious one. A node may override it for
// a project whose profile field is set unusually.
func (r *Runner) generateDBTProfileForNode(node models.Node, connID string) (*dbtProfile, error) {
	if r.connResolver == nil {
		return nil, fmt.Errorf("conn_id %q given but this engine has no connection resolver", connID)
	}
	conn, err := r.connResolver.ResolveConnection(connID)
	if err != nil {
		return nil, err
	}

	projectDir, _ := node.Config["project_dir"].(string)
	if projectDir == "" {
		projectDir = "."
	}
	profileName, _ := node.Config["profile"].(string)
	if profileName == "" {
		profileName, err = dbtProjectProfileName(projectDir)
		if err != nil {
			return nil, err
		}
	}
	targetSchema, _ := node.Config["target_schema"].(string)
	threads := 0
	if t, ok := node.Config["threads"].(float64); ok {
		threads = int(t)
	}
	return generateDBTProfile(conn, profileName, targetSchema, threads)
}

// dbtProjectProfileName reads the profile a project asks for.
//
// Parsed with a line scan rather than a YAML dependency: this reads one
// scalar from a file dbt itself validates, and adding a YAML library to the
// engine for it would be a larger commitment than the problem.
func dbtProjectProfileName(projectDir string) (string, error) {
	path := filepath.Join(projectDir, "dbt_project.yml")
	// #nosec G304 -- projectDir is operator-supplied node configuration
	// naming a dbt project on this host, which is the whole point of the
	// node; there is no untrusted request input on this path, and the file
	// read is a fixed name inside it.
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s to find which profile the project wants: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "profile:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "profile:"))
		value = strings.Trim(value, `"'`)
		if value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf(
		"%s does not name a profile, so there is nothing to generate credentials for; "+
			"set profile: in the project or profile on the node", path)
}
