import java.io.*;
import java.lang.reflect.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import java.util.*;

/** The JVM reference adapter's harness side of brokoli.task-runtime/v1
 * (ADR-036, ADR-033 sections 3 and 7).
 *
 * Phase 1 scope: the handshake (start -> ready -> completed|failed) and
 * invoking a declared static entrypoint. The remaining frame vocabulary
 * (log/progress/warning/metric), the dataset/artifact/collection output
 * kinds and input reading are phase 2, alongside conformance against
 * docs/schema/task-runtime-v1.json.
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

        emit(Map.of(
            "type", "ready",
            "adapter", ADAPTER,
            "adapter_version", ADAPTER_VERSION,
            "runtime", "jvm",
            "runtime_version", System.getProperty("java.version", "unknown")));

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

        writeResult(str(start.get("result_path")), str(inv.get("interface_digest")), value);
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

    /** Writes a task-result-v1 candidate. Phase 1 emits the scalar kind
     * only; the other three are phase 2. */
    private static void writeResult(String resultPath, String interfaceDigest, Object value) throws IOException {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("kind", "scalar");
        out.put("value", normalize(value));
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
