package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness/jvmharness"
)

// bundle command group — packages compiled artifacts into a
// task-bundle/v2 (ADR-033 section 2) that a Brokoli server can run.
//
// This exists because a JVM developer already has a build tool that
// produces class files and jars. What they lacked was the step that
// turns those into a bundle: the Python SDK does its own packaging
// client-side, and there was no equivalent for anything else, so a Java,
// Kotlin, Scala or Groovy task had no route to a server at all even
// though the runtime adapter could run one (ADR-036 phases 1-5).
//
// Deliberately a CLI command rather than a language binding: the adapter
// loads BYTECODE, so one command serves every JVM language at once,
// and a user adds no dependency to their project to use it.
var bundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Package compiled code into a task bundle a Brokoli server can run",
	Long: `A task bundle is a content-addressed archive holding a task's
compiled artifacts plus a manifest describing how to run them.

Bundles are addressed by their sha256 digest, which this command prints.
A pipeline references that digest from a task node's task_bundle config,
so the bundle and the pipeline that uses it are versioned independently.`,
}

var (
	bundleJVMClasses    []string
	bundleJVMJars       []string
	bundleJVMEntrypoint string
	bundleJVMName       string
	bundleJVMOut        string
)

var bundleJVMCmd = &cobra.Command{
	Use:   "jvm",
	Short: "Package compiled JVM classes and jars into a task bundle",
	Long: `Packages compiled JVM bytecode into a task-bundle/v2 with a "jvm"
payload. Works with any JVM language -- Java, Kotlin, Scala, Groovy --
because the runtime adapter loads bytecode, not source.

The entrypoint is a public static method, written as Class#method:

  brokoli bundle jvm \
    --classes build/classes/java/main \
    --entrypoint com.example.tasks.Transforms#dailyRollup \
    --name daily-rollup \
    --out daily-rollup.tar.gz

A class compiled from a language other than Java needs that language's
runtime on the classpath at run time, so pass its jar too:

  brokoli bundle jvm --classes build/classes/groovy/main \
    --jar /usr/share/groovy/lib/groovy-4.0.0.jar ...

Every jar in the bundle is placed on the task's classpath automatically;
the bundle root comes first, so plain class files need no extra flags.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(bundleJVMClasses) == 0 && len(bundleJVMJars) == 0 {
			return fmt.Errorf("nothing to package: pass --classes and/or --jar")
		}
		className, methodName, err := jvmharness.ParseEntrypoint(bundleJVMEntrypoint)
		if err != nil {
			return err
		}
		if bundleJVMName == "" {
			return fmt.Errorf("--name is required (it labels the bundle in the manifest)")
		}

		files := map[string]string{}
		for _, dir := range bundleJVMClasses {
			if err := collectClassTree(dir, files); err != nil {
				return err
			}
		}
		for _, jar := range bundleJVMJars {
			data, readErr := os.ReadFile(jar) // #nosec G304 -- a path the operator passed on their own command line
			if readErr != nil {
				return fmt.Errorf("read jar %s: %w", jar, readErr)
			}
			files["lib/"+filepath.Base(jar)] = string(data)
		}
		if len(files) == 0 {
			return fmt.Errorf("no .class or .jar files found under %s", strings.Join(bundleJVMClasses, ", "))
		}

		manifest, err := jvmManifest(bundleJVMName, className, methodName, files)
		if err != nil {
			return err
		}
		archive, err := taskbundlev2.Assemble(files, manifest)
		if err != nil {
			return fmt.Errorf("assemble bundle: %w", err)
		}
		digest := taskbundlev2.DigestOf(archive)

		out := bundleJVMOut
		if out == "" {
			out = bundleJVMName + ".tar.gz"
		}
		if err := os.WriteFile(out, archive, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", out)
		fmt.Fprintf(cmd.OutOrStdout(), "digest: %s\n", digest)
		fmt.Fprintf(cmd.OutOrStdout(), "files:  %d\n", len(files))
		fmt.Fprintf(cmd.OutOrStdout(), "\nUpload it, then reference the digest from a task node:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "  curl -X POST --data-binary @%s $BROKOLI_URL/api/task-bundles/%s\n", out, digest)
		return nil
	},
}

// collectClassTree adds every .class file under dir to files, keyed by
// its path relative to dir.
//
// Relative to dir, not to the working directory: a JVM classloader finds
// com.example.Task at com/example/Task.class beneath a classpath root,
// so the package structure below the compiler's output directory is
// exactly what has to be preserved. Rooting the paths anywhere else
// would produce a bundle whose classes cannot be found.
func collectClassTree(dir string, files map[string]string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("read classes directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--classes %s is not a directory (point it at a compiler output root)", dir)
	}
	// Opened through an os.Root rather than by absolute path. WalkDir
	// reports what it saw; by the time the file is read, a symlink could
	// have been swapped in, and the bytes read would come from outside
	// the tree -- and then be packaged into a bundle and uploaded. Root
	// resolves every path beneath dir and refuses to follow a link that
	// escapes it, which closes the window rather than narrowing it.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open classes directory %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".class") {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		f, openErr := root.Open(filepath.ToSlash(rel))
		if openErr != nil {
			return fmt.Errorf("read %s: %w", rel, openErr)
		}
		data, readErr := io.ReadAll(f)
		_ = f.Close()
		if readErr != nil {
			return fmt.Errorf("read %s: %w", rel, readErr)
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
}

// jvmManifest builds the task-bundle/v2 manifest for a jvm payload.
//
// Module/Symbol carry the class and method: the managed-language
// entrypoint shape task-bundle/v2 already defines fits the JVM as-is,
// which is why packaging one needs no new manifest fields.
func jvmManifest(name, className, methodName string, files map[string]string) (*taskbundlev2.Manifest, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	entries := make([]taskbundlev2.FileEntry, 0, len(paths))
	contentHash := sha256.New()
	for _, p := range paths {
		body := files[p]
		sum := sha256.Sum256([]byte(body))
		entries = append(entries, taskbundlev2.FileEntry{
			Path:   p,
			Size:   int64(len(body)),
			SHA256: hex.EncodeToString(sum[:]),
		})
		contentHash.Write([]byte(p))
		contentHash.Write([]byte{0})
		contentHash.Write([]byte(body))
	}
	// source_digest identifies the packaged content. Derived from the
	// files themselves so repackaging identical input yields an identical
	// bundle -- the property that makes a content-addressed digest worth
	// having.
	sourceDigest := "sha256:" + hex.EncodeToString(contentHash.Sum(nil))

	return &taskbundlev2.Manifest{
		Format: taskbundlev2.Format,
		Name:   name,
		// No interface is declared: this command packages compiled
		// artifacts, and cannot see the type information an authoring SDK
		// would (ADR-032 section 6 -- absence is honest, never a guess).
		// The pipeline that references this bundle declares the node's
		// interface instead.
		InterfaceDigest: sourceDigest,
		SourceDigest:    sourceDigest,
		Payloads: []taskbundlev2.Payload{{
			ID:      "jvm-any",
			Runtime: taskbundlev2.RuntimeJVM,
			OS:      "any",
			Arch:    "any",
			Entrypoint: taskbundlev2.Entrypoint{
				Module: className,
				Symbol: methodName,
			},
			Effects:       taskbundlev2.EffectPure,
			PayloadDigest: sourceDigest,
		}},
		Files: entries,
	}, nil
}

func init() {
	bundleJVMCmd.Flags().StringSliceVar(&bundleJVMClasses, "classes", nil,
		"compiler output directory holding .class files (repeatable)")
	bundleJVMCmd.Flags().StringSliceVar(&bundleJVMJars, "jar", nil,
		"jar to include on the task's classpath, e.g. a language runtime (repeatable)")
	bundleJVMCmd.Flags().StringVar(&bundleJVMEntrypoint, "entrypoint", "",
		"public static method to call, as fully.qualified.Class#method")
	bundleJVMCmd.Flags().StringVar(&bundleJVMName, "name", "", "bundle name recorded in the manifest")
	bundleJVMCmd.Flags().StringVar(&bundleJVMOut, "out", "", "output path (default <name>.tar.gz)")
	_ = bundleJVMCmd.MarkFlagRequired("entrypoint")
	_ = bundleJVMCmd.MarkFlagRequired("name")

	bundleCmd.AddCommand(bundleJVMCmd)
	rootCmd.AddCommand(bundleCmd)
}
