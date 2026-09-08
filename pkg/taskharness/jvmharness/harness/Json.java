import java.util.*;

/** Minimal JSON reader/writer for the brokoli.task-runtime/v1 frame
 * vocabulary. No dependencies on purpose: a JSON library here would land
 * in every JVM task's classpath and eventually conflict with the task's
 * own.
 *
 * The numeric contract is the whole risk. brokoli#479 lost 64-bit
 * integers because a decoder turned every JSON number into a double;
 * 9007199254740993 became ...992, silently. So: an integral literal with
 * no fraction/exponent decodes to Long, and only a genuinely fractional
 * one decodes to Double. */
final class Json {
    private final String s;
    private int i;
    private Json(String s) { this.s = s; }

    static Object parse(String text) {
        Json p = new Json(text);
        p.ws();
        Object v = p.value();
        p.ws();
        if (p.i != text.length()) throw new IllegalArgumentException("trailing input at " + p.i);
        return v;
    }

    private void ws() { while (i < s.length() && Character.isWhitespace(s.charAt(i))) i++; }

    private Object value() {
        if (i >= s.length()) throw new IllegalArgumentException("unexpected end of input");
        char c = s.charAt(i);
        switch (c) {
            case '{': return object();
            case '[': return array();
            case '"': return string();
            case 't': expect("true");  return Boolean.TRUE;
            case 'f': expect("false"); return Boolean.FALSE;
            case 'n': expect("null");  return null;
            default:  return number();
        }
    }

    private void expect(String lit) {
        if (!s.startsWith(lit, i)) throw new IllegalArgumentException("bad literal at " + i);
        i += lit.length();
    }

    private Map<String,Object> object() {
        Map<String,Object> m = new LinkedHashMap<>();
        i++; ws();
        if (i < s.length() && s.charAt(i) == '}') { i++; return m; }
        while (true) {
            ws();
            String k = string();
            ws();
            if (s.charAt(i) != ':') throw new IllegalArgumentException("expected ':' at " + i);
            i++; ws();
            m.put(k, value());
            ws();
            char c = s.charAt(i++);
            if (c == '}') return m;
            if (c != ',') throw new IllegalArgumentException("expected ',' or '}' at " + (i-1));
        }
    }

    private List<Object> array() {
        List<Object> l = new ArrayList<>();
        i++; ws();
        if (i < s.length() && s.charAt(i) == ']') { i++; return l; }
        while (true) {
            ws();
            l.add(value());
            ws();
            char c = s.charAt(i++);
            if (c == ']') return l;
            if (c != ',') throw new IllegalArgumentException("expected ',' or ']' at " + (i-1));
        }
    }

    private String string() {
        if (s.charAt(i) != '"') throw new IllegalArgumentException("expected string at " + i);
        i++;
        StringBuilder b = new StringBuilder();
        while (true) {
            char c = s.charAt(i++);
            if (c == '"') return b.toString();
            if (c != '\\') { b.append(c); continue; }
            char e = s.charAt(i++);
            switch (e) {
                case '"': b.append('"'); break;
                case '\\': b.append('\\'); break;
                case '/': b.append('/'); break;
                case 'b': b.append('\b'); break;
                case 'f': b.append('\f'); break;
                case 'n': b.append('\n'); break;
                case 'r': b.append('\r'); break;
                case 't': b.append('\t'); break;
                case 'u':
                    b.append((char) Integer.parseInt(s.substring(i, i + 4), 16));
                    i += 4;
                    break;
                default: throw new IllegalArgumentException("bad escape \\" + e);
            }
        }
    }

    /** Integral -> Long, fractional -> Double. The distinction is the
     * point: a double cannot hold an integer above 2^53, so decoding
     * every number as one silently alters 64-bit ids. */
    private Object number() {
        int start = i;
        if (i < s.length() && (s.charAt(i) == '-' || s.charAt(i) == '+')) i++;
        boolean fractional = false;
        while (i < s.length()) {
            char c = s.charAt(i);
            if (c >= '0' && c <= '9') { i++; continue; }
            if (c == '.' || c == 'e' || c == 'E') { fractional = true; i++; continue; }
            if ((c == '-' || c == '+') && (s.charAt(i-1) == 'e' || s.charAt(i-1) == 'E')) { i++; continue; }
            break;
        }
        String lit = s.substring(start, i);
        if (lit.isEmpty()) throw new IllegalArgumentException("expected a number at " + start);
        if (fractional) return Double.parseDouble(lit);
        try {
            return Long.parseLong(lit);
        } catch (NumberFormatException overflow) {
            // Beyond long range: refuse rather than silently narrowing to
            // a double, which is exactly the #479 failure.
            throw new IllegalArgumentException("integer literal out of 64-bit range: " + lit);
        }
    }

    static String write(Object v) {
        StringBuilder b = new StringBuilder();
        writeTo(b, v);
        return b.toString();
    }

    private static void writeTo(StringBuilder b, Object v) {
        if (v == null) { b.append("null"); return; }
        if (v instanceof String) { writeString(b, (String) v); return; }
        if (v instanceof Boolean || v instanceof Long || v instanceof Integer) { b.append(v); return; }
        if (v instanceof Double || v instanceof Float) {
            double d = ((Number) v).doubleValue();
            if (Double.isNaN(d) || Double.isInfinite(d))
                throw new IllegalArgumentException("JSON has no representation for " + d);
            b.append(v);
            return;
        }
        if (v instanceof Map) {
            b.append('{');
            boolean first = true;
            for (Map.Entry<?,?> e : ((Map<?,?>) v).entrySet()) {
                if (!first) b.append(',');
                first = false;
                writeString(b, String.valueOf(e.getKey()));
                b.append(':');
                writeTo(b, e.getValue());
            }
            b.append('}');
            return;
        }
        if (v instanceof Iterable) {
            b.append('[');
            boolean first = true;
            for (Object o : (Iterable<?>) v) {
                if (!first) b.append(',');
                first = false;
                writeTo(b, o);
            }
            b.append(']');
            return;
        }
        throw new IllegalArgumentException("no JSON encoding for " + v.getClass());
    }

    private static void writeString(StringBuilder b, String s) {
        b.append('"');
        for (int j = 0; j < s.length(); j++) {
            char c = s.charAt(j);
            switch (c) {
                case '"': b.append("\\\""); break;
                case '\\': b.append("\\\\"); break;
                case '\n': b.append("\\n"); break;
                case '\r': b.append("\\r"); break;
                case '\t': b.append("\\t"); break;
                case '\b': b.append("\\b"); break;
                case '\f': b.append("\\f"); break;
                default:
                    if (c < 0x20) b.append(String.format("\\u%04x", (int) c));
                    else b.append(c);
            }
        }
        b.append('"');
    }
}
