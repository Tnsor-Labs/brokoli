import java.io.*;
import java.lang.reflect.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.util.*;

/** The JVM reference adapter's harness side of brokoli.task-runtime/v1
 * (ADR-036, ADR-033 sections 3 and 7).
 *
 * Covers the handshake (start -> ready -> completed|failed), invoking a
 * declared static entrypoint, reading staged NDJSON input, and all four
 * ADR-032 section 6 output kinds.
 *
 * A mid-task 'cancel' frame is deliberately NOT read, matching both
 * existing reference adapters: the task call is a single blocking
 * invocation, and the worker's SIGTERM/SIGKILL escalation after the
 * cancellation grace period is what actually stops it.
 *
 * Not one file, deliberately. ADR-036 describes the harness as "one
 * dependency-free .java file", which was a constraint of the JEP 330
 * source-launch option that ADR rejected on measurement. Compiling with
 * javac -- what it chose instead -- lifts it, so Json lives in its own
 * file rather than being inlined here to satisfy a constraint that no
 * longer applies. Dependency-free still holds and is the part that
 * mattered: a JSON library here would land in every JVM task's
 * classpath. */
public final class Harness {

    private static final String PROTOCOL = "brokoli.task-runtime/v1";
    private static final String ADAPTER = "brokoli-jvm-taskharness";
    private static final String ADAPTER_VERSION = "0.1.0";

    private static final PrintStream OUT = new PrintStream(new FileOutputStream(FileDescriptor.out), true, StandardCharsets.UTF_8);

    public static void main(String[] args) {
        try {
            run();
        } catch (Throwable fatal) {
            // Nothing above this may throw without a terminal frame: the
            // worker treats an exit with no terminal frame as a protocol
            // violation it has to synthesize a failure for, which loses
            // whatever the harness actually knew.
            fail("platform", "harness_crashed", stack(fatal));
            System.exit(1);
        }
    }

    private static void run() throws Exception {
        Map<String, Object> start = readStartFrame();
        Map<String, Object> inv = readInvocation(str(start.get("invocation_path")));

        // Exactly the fields task-runtime-v1.json's "ready" allows, and
        // all of the ones it requires: it sets additionalProperties
        // false, so inventing a runtime/runtime_version pair here (as an
        // earlier draft did) is a protocol violation, not extra
        // diagnostics. The worker derives runtime identity from the
        // payload it selected and what it launched (ADR-033 section 7
        // rule 10) -- a harness cannot attest its own.
        Map<String, Object> ready = new LinkedHashMap<>();
        ready.put("type", "ready");
        ready.put("protocol", PROTOCOL);
        ready.put("adapter", ADAPTER);
        ready.put("adapter_version", ADAPTER_VERSION);
        ready.put("capabilities", new ArrayList<String>());
        emit(ready);

        Object value;
        try {
            value = invokeEntrypoint(inv);
        } catch (InvocationTargetException e) {
            // The task itself threw: user_code, and the cause is what the
            // author needs, not the reflection wrapper around it.
            Throwable cause = e.getCause() == null ? e : e.getCause();
            fail("user_code", "task_raised", stack(cause));
            System.exit(1);
            return;
        }

        try {
            writeResult(str(start.get("result_path")), str(inv.get("interface_digest")), value,
                str(inv.get("output_kind")), str(inv.get("output_media_type")),
                str(start.get("output_staging_dir")));
        } catch (IllegalArgumentException contract) {
            // The task returned something its DECLARED port cannot carry.
            // contract_violation, not user_code: the task ran fine, it
            // just does not match what it said it produces.
            fail("contract_violation", "output_shape", contract.getMessage());
            System.exit(1);
            return;
        }
        emit(Map.of("type", "completed"));
    }

    private static Map<String, Object> readStartFrame() throws IOException {
        BufferedReader in = new BufferedReader(new InputStreamReader(System.in, StandardCharsets.UTF_8));
        String line = in.readLine();
        if (line == null) {
            // No frame to report into -- ready was never sent, so the
            // worker's own "exited before any terminal frame" detection
            // is what reports this.
            System.exit(1);
        }
        Object parsed;
        try {
            parsed = Json.parse(line);
        } catch (RuntimeException e) {
            fail("contract_violation", "malformed_start", "start frame is not valid JSON: " + e.getMessage());
            System.exit(1);
            return null;
        }
        if (!(parsed instanceof Map)) {
            fail("contract_violation", "unexpected_start", "start frame is not a JSON object");
            System.exit(1);
        }
        @SuppressWarnings("unchecked")
        Map<String, Object> start = (Map<String, Object>) parsed;
        if (!"start".equals(start.get("type")) || !PROTOCOL.equals(start.get("protocol"))) {
            fail("contract_violation", "unexpected_start", "first frame was not a valid start frame");
            System.exit(1);
        }
        return start;
    }

    private static Map<String, Object> readInvocation(String path) {
        try {
            String text = new String(Files.readAllBytes(Paths.get(path)), StandardCharsets.UTF_8);
            Object parsed = Json.parse(text);
            if (!(parsed instanceof Map)) throw new IllegalArgumentException("invocation is not a JSON object");
            @SuppressWarnings("unchecked")
            Map<String, Object> inv = (Map<String, Object>) parsed;
            return inv;
        } catch (Exception e) {
            fail("contract_violation", "invalid_invocation", e.getMessage());
            System.exit(1);
            return null;
        }
    }

    /** Resolves and calls the declared entrypoint.
     *
     * Explicit, never inferred: the class and method are named in the
     * invocation, loaded through a classloader over the declared
     * classpath, and nothing scans for annotations or falls back to "the
     * only public method". A missing or ambiguous entrypoint is a
     * contract_violation, because inferring one would make adding a
     * second method to a class a silent behaviour change. */
    private static Object invokeEntrypoint(Map<String, Object> inv) throws Exception {
        String className = str(inv.get("class_name"));
        String methodName = str(inv.get("method_name"));
        if (className == null || className.isEmpty() || methodName == null || methodName.isEmpty()) {
            fail("contract_violation", "invalid_invocation", "invocation must name class_name and method_name");
            System.exit(1);
        }

        List<URL> urls = new ArrayList<>();
        Object cp = inv.get("classpath");
        if (cp instanceof List) {
            for (Object entry : (List<?>) cp) {
                urls.add(Paths.get(String.valueOf(entry)).toUri().toURL());
            }
        }

        Class<?> cls;
        try (URLClassLoader loader = new URLClassLoader(urls.toArray(new URL[0]), Harness.class.getClassLoader())) {
            try {
                cls = Class.forName(className, true, loader);
            } catch (ClassNotFoundException e) {
                fail("contract_violation", "class_not_found",
                    "entrypoint class '" + className + "' was not found on the declared classpath " + urls);
                System.exit(1);
                return null;
            } catch (LinkageError e) {
                // The class exists but something it depends on does not.
                // For a non-Java JVM language this is the common case and
                // has one cause: the language's runtime jar is missing
                // from the bundle (a Groovy class needs
                // groovy/lang/GroovyObject, Kotlin needs kotlin-stdlib,
                // Scala needs scala-library).
                //
                // contract_violation, not platform: the bundle is
                // incomplete, the server is fine. Reporting this as
                // platform would send an operator looking for a broken
                // worker.
                fail("contract_violation", "missing_dependency",
                    "entrypoint class '" + className + "' could not be linked: " + e
                        + ". A class compiled from a JVM language other than Java needs that language's "
                        + "runtime on the classpath; add its jar to the task bundle. Classpath was " + urls);
                System.exit(1);
                return null;
            }

            Map<String, Object> kwargs = new LinkedHashMap<>();
            Object k = inv.get("kwargs");
            if (k instanceof Map) {
                for (Map.Entry<?, ?> e : ((Map<?, ?>) k).entrySet()) {
                    kwargs.put(String.valueOf(e.getKey()), e.getValue());
                }
            }

            // Java has no keyword arguments, so kwargs travel as ONE Map
            // argument -- the same forced (not chosen) shape the Node
            // adapter documents for the identical reason. A no-arg method
            // is accepted too, so a task taking nothing needs no
            // ceremony.
            // Staged input rows reach the task under the port's own
            // name, so a task's signature names its input the way the
            // interface does -- the same convention pyharness uses.
            String inputPath = str(inv.get("input_path"));
            if (inputPath != null && !inputPath.isEmpty()) {
                kwargs.put("input", readInputRows(inputPath));
            }

            Method m = findStatic(cls, methodName, kwargs.isEmpty());
            if (m == null) {
                fail("contract_violation", "symbol_not_found",
                    "class '" + className + "' has no public static method '" + methodName
                        + "' taking (java.util.Map) or ()");
                System.exit(1);
                return null;
            }
            return m.getParameterCount() == 0 ? m.invoke(null) : m.invoke(null, kwargs);
        }
    }

    private static Method findStatic(Class<?> cls, String name, boolean preferNoArg) {
        Method oneMap = null, noArg = null;
        for (Method m : cls.getMethods()) {
            if (!m.getName().equals(name) || !Modifier.isStatic(m.getModifiers())) continue;
            if (m.getParameterCount() == 0) noArg = m;
            else if (m.getParameterCount() == 1 && Map.class.isAssignableFrom(m.getParameterTypes()[0])) oneMap = m;
        }
        if (preferNoArg && noArg != null) return noArg;
        if (oneMap != null) return oneMap;
        return noArg;
    }

    private static final String DATASET_FILENAME = "result.ndjson";
    private static final String ARTIFACT_FILENAME = "result.bin";

    /** Reads the NDJSON rows the trusted worker staged for this attempt.
     *
     * Written by the worker, not the task, so this is a cooperating file
     * rather than untrusted input -- a malformed line is a bug in the
     * worker, reported as contract_violation because the harness cannot
     * tell the task anything useful about it.
     *
     * Json.parse decodes an integral literal to a long, so a 64-bit id
     * arrives intact. That is the difference brokoli#492 records between
     * this adapter and the Node one, whose number type cannot hold one. */
    private static List<Object> readInputRows(String path) throws IOException {
        List<Object> rows = new ArrayList<>();
        int lineNo = 0;
        for (String line : Files.readAllLines(Paths.get(path), StandardCharsets.UTF_8)) {
            lineNo++;
            String trimmed = line.trim();
            if (trimmed.isEmpty()) continue;
            try {
                rows.add(Json.parse(trimmed));
            } catch (RuntimeException e) {
                throw new IOException("input row " + lineNo + " is not valid JSON: " + e.getMessage());
            }
        }
        return rows;
    }

    /** Serializes rows to NDJSON in the staging dir and describes them by
     * reference.
     *
     * The DECLARED interface, not the value's runtime shape, is what says
     * a port is a dataset, so a task that declared one and returned
     * something that is not a list of row maps is a contract violation
     * with a precise message rather than a confusing serialization error.
     *
     * Size and checksum come from the bytes actually written, in the same
     * pass, so the worker's own verification (ADR-033 section 7 rule 6)
     * compares against what is really on disk. */
    private static Map<String, Object> writeDatasetOutput(String stagingDir, Object rows) throws Exception {
        if (!(rows instanceof Iterable)) {
            throw new IllegalArgumentException(
                "task declares a dataset output but returned " + typeName(rows) + "; expected a list of row objects");
        }
        Path path = Paths.get(stagingDir, DATASET_FILENAME);
        java.security.MessageDigest sha = java.security.MessageDigest.getInstance("SHA-256");
        long size = 0;
        int i = 0;
        try (OutputStream f = Files.newOutputStream(path)) {
            for (Object row : (Iterable<?>) rows) {
                if (!(row instanceof Map)) {
                    throw new IllegalArgumentException(
                        "task declares a dataset output but row " + i + " is " + typeName(row)
                            + "; every row must be an object");
                }
                byte[] line = (Json.write(row) + "\n").getBytes(StandardCharsets.UTF_8);
                f.write(line);
                sha.update(line);
                size += line.length;
                i++;
            }
        }
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("kind", "dataset");
        out.put("path", DATASET_FILENAME);
        out.put("codec", "ndjson/v1");
        out.put("size_bytes", size);
        out.put("checksum", "sha256:" + hex(sha.digest()));
        return out;
    }

    /** Writes opaque bytes and describes them by reference.
     *
     * A task declaring an artifact output returns the bytes themselves --
     * byte[], or a String which is encoded UTF-8. Anything else is a
     * contract violation named precisely, since "expected bytes, got Map"
     * is the only form of that error an author can act on. */
    private static Map<String, Object> writeArtifactOutput(String stagingDir, Object payload, String mediaType) throws Exception {
        byte[] bytes;
        if (payload instanceof String) bytes = ((String) payload).getBytes(StandardCharsets.UTF_8);
        else if (payload instanceof byte[]) bytes = (byte[]) payload;
        else {
            throw new IllegalArgumentException(
                "task declares an artifact output but returned " + typeName(payload) + "; expected byte[] or String");
        }
        Files.write(Paths.get(stagingDir, ARTIFACT_FILENAME), bytes);
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("kind", "artifact");
        out.put("path", ARTIFACT_FILENAME);
        // The manifest has no media_type field, so an artifact states its
        // media type in codec -- see the engine's artifactMediaType.
        out.put("codec", mediaType == null || mediaType.isEmpty() ? "application/octet-stream" : mediaType);
        out.put("size_bytes", (long) bytes.length);
        out.put("checksum", "sha256:" + hex(sha256(bytes)));
        return out;
    }

    /** Describes separately addressable items, each by its own contract.
     *
     * A task declaring a collection output returns a Map. The key is what
     * makes each item separately addressable (ADR-032 section 6), so it
     * is required rather than derived from position -- positional
     * identity is exactly what a key exists to replace.
     *
     * byte[] items become artifacts, each staged as its own file with its
     * own checksum; everything else is an inline scalar. */
    private static Map<String, Object> writeCollectionOutput(String stagingDir, Object items, String mediaType) throws Exception {
        if (!(items instanceof Map)) {
            throw new IllegalArgumentException(
                "task declares a collection output but returned " + typeName(items)
                    + "; expected a Map of key to value");
        }
        List<Object> out = new ArrayList<>();
        int i = 0;
        for (Map.Entry<?, ?> e : ((Map<?, ?>) items).entrySet()) {
            Object value = e.getValue();
            Map<String, Object> item = new LinkedHashMap<>();
            if (value instanceof byte[]) {
                byte[] bytes = (byte[]) value;
                String name = "item-" + i + ".bin";
                Files.write(Paths.get(stagingDir, name), bytes);
                item.put("kind", "artifact");
                item.put("path", name);
                item.put("codec", mediaType == null || mediaType.isEmpty() ? "application/octet-stream" : mediaType);
                item.put("size_bytes", (long) bytes.length);
                item.put("checksum", "sha256:" + hex(sha256(bytes)));
            } else {
                item.put("kind", "scalar");
                item.put("value", normalize(value));
            }
            item.put("item_key", String.valueOf(e.getKey()));
            out.add(item);
            i++;
        }
        Map<String, Object> res = new LinkedHashMap<>();
        res.put("kind", "collection");
        res.put("items", out);
        return res;
    }

    private static byte[] sha256(byte[] b) throws Exception {
        return java.security.MessageDigest.getInstance("SHA-256").digest(b);
    }

    private static String hex(byte[] b) {
        StringBuilder sb = new StringBuilder(b.length * 2);
        for (byte x : b) sb.append(String.format("%02x", x));
        return sb.toString();
    }

    private static String typeName(Object o) {
        return o == null ? "null" : o.getClass().getSimpleName();
    }

    /** Writes a task-result-v1 candidate in the DECLARED output kind.
     *
     * The interface is authoritative, never the returned value's runtime
     * shape: a task declaring a dataset that returns a String is a
     * contract violation, not an artifact. */
    private static void writeResult(String resultPath, String interfaceDigest, Object value,
                                    String outputKind, String mediaType, String stagingDir) throws Exception {
        Map<String, Object> out;
        String kind = outputKind == null || outputKind.isEmpty() ? "scalar" : outputKind;
        // The harness creates its own staging directory, as both other
        // reference adapters do: the worker names it in the start frame
        // but does not guarantee it exists.
        if (!"scalar".equals(kind) && stagingDir != null && !stagingDir.isEmpty()) {
            Files.createDirectories(Paths.get(stagingDir));
        }
        switch (kind) {
            case "dataset":
                out = writeDatasetOutput(stagingDir, value);
                break;
            case "artifact":
                out = writeArtifactOutput(stagingDir, value, mediaType);
                break;
            case "collection":
                out = writeCollectionOutput(stagingDir, value, mediaType);
                break;
            case "scalar":
                out = new LinkedHashMap<>();
                out.put("kind", "scalar");
                out.put("value", normalize(value));
                break;
            default:
                throw new IllegalArgumentException("unrecognized declared output kind '" + kind + "'");
        }
        Map<String, Object> outputs = new LinkedHashMap<>();
        outputs.put("result", out);
        Map<String, Object> doc = new LinkedHashMap<>();
        doc.put("contract", "brokoli.task-result/v1");
        doc.put("interface_digest", interfaceDigest == null ? "" : interfaceDigest);
        doc.put("outputs", outputs);
        Files.write(Paths.get(resultPath), Json.write(doc).getBytes(StandardCharsets.UTF_8));
    }

    /** Narrows a returned value to what JSON can carry, refusing rather
     * than guessing. An Integer/Short/Byte becomes a Long so the wire
     * carries one integer type; a Float becomes a Double for the same
     * reason. */
    private static Object normalize(Object v) {
        if (v == null || v instanceof String || v instanceof Boolean || v instanceof Long || v instanceof Double) return v;
        if (v instanceof Integer || v instanceof Short || v instanceof Byte) return ((Number) v).longValue();
        if (v instanceof Float) return ((Number) v).doubleValue();
        if (v instanceof Map || v instanceof Iterable) return v;
        throw new IllegalArgumentException(
            "task returned " + v.getClass().getName() + ", which has no portable representation; "
                + "return a String, a number, a boolean, a Map or a List");
    }

    private static void emit(Map<String, Object> frame) {
        OUT.println(Json.write(frame));
    }

    /** Emits a terminal failed frame. Callers must exit afterwards --
     * emitting anything after a terminal frame is a protocol violation
     * the worker rejects. */
    private static void fail(String category, String code, String message) {
        Map<String, Object> failure = new LinkedHashMap<>();
        failure.put("category", category);
        failure.put("code", code);
        failure.put("message", message == null ? "" : message);
        failure.put("retryable", Boolean.FALSE);
        Map<String, Object> frame = new LinkedHashMap<>();
        frame.put("type", "failed");
        frame.put("failure", failure);
        emit(frame);
    }

    private static String str(Object o) { return o == null ? null : String.valueOf(o); }

    private static String stack(Throwable t) {
        StringWriter w = new StringWriter();
        t.printStackTrace(new PrintWriter(w));
        return w.toString();
    }

    private Harness() {}
}
