package mil.army.usace.cc;

import java.io.BufferedReader;
import java.io.BufferedWriter;
import java.io.ByteArrayInputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStreamWriter;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Base64;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.locks.ReentrantLock;
import java.util.function.Function;

/**
 * Thin Java client for the cc-sdk binary.
 *
 * <p>Drop this single file into any Java project to use the Cloud Compute SDK.
 * No external dependencies beyond the JDK.
 *
 * <pre>{@code
 * try (CcSdk sdk = new CcSdk()) {
 *     Payload payload = sdk.getPayload();
 *     DataSource input = payload.inputs().get(0);
 *     String watershed = input.paths().get("watershed");
 *
 *     sdk.copyToLocal("ds", "key", "", "/tmp/file.txt");
 *     sdk.copyToRemote("ds", "key", "", "/tmp/file.txt");
 * }
 * }</pre>
 */
public class CcSdk implements AutoCloseable {

    private static final String CC_SDK_BIN_ENV = "CC_SDK_BIN";
    private static final String DEFAULT_BIN = "cc-sdk";

    private final Process process;
    private final BufferedWriter stdin;
    private final BufferedReader stdout;
    private final AtomicInteger counter = new AtomicInteger(0);
    private final ReentrantLock lock = new ReentrantLock();

    public CcSdk() throws IOException {
        this(resolveBin());
    }

    @SuppressWarnings("this-escape")
    public CcSdk(String binPath) throws IOException {
        ProcessBuilder pb = new ProcessBuilder(binPath, "serve").redirectErrorStream(false);
        this.process = pb.start();
        this.stdin = new BufferedWriter(
                new OutputStreamWriter(process.getOutputStream(), StandardCharsets.UTF_8));
        this.stdout = new BufferedReader(
                new InputStreamReader(process.getInputStream(), StandardCharsets.UTF_8));

        String readyLine = stdout.readLine();
        if (readyLine == null) {
            throw new CcSdkException("cc-sdk failed to start: " + readStderr());
        }
        Map<String, Object> ready = Json.parseObject(readyLine);
        if (!Boolean.TRUE.equals(ready.get("ok")) || !"ready".equals(ready.get("cmd"))) {
            throw new CcSdkException("cc-sdk unexpected ready signal: " + readyLine);
        }

        Runtime.getRuntime().addShutdownHook(new Thread(this::shutdownQuietly));
    }

    // ---------------------------------------------------------------------
    // Payload types — records mirror the Go CLI's JSON schema
    // ---------------------------------------------------------------------

    /** Typed view of a CC data source (input or output). */
    public record DataSource(
            String name,
            String id,
            String storeName,
            Map<String, String> paths,
            Map<String, String> dataPaths) {

        static DataSource fromMap(Map<String, Object> m) {
            return new DataSource(
                    Maps.str(m, "name"),
                    Maps.str(m, "id"),
                    Maps.str(m, "store_name"),
                    Maps.stringMap(m, "paths"),
                    Maps.stringMap(m, "data_paths"));
        }
    }

    /** Typed view of a CC data store. */
    public record Store(
            String name,
            String id,
            String storeType,
            String profile,
            Map<String, Object> params) {

        static Store fromMap(Map<String, Object> m) {
            return new Store(
                    Maps.str(m, "name"),
                    Maps.str(m, "id"),
                    Maps.str(m, "store_type"),
                    Maps.str(m, "profile"),
                    Maps.objMap(m, "params"));
        }
    }

    /** Typed view of a CC action. */
    public record Action(
            String name,
            String type,
            String description,
            Map<String, Object> attributes,
            List<Store> stores,
            List<DataSource> inputs,
            List<DataSource> outputs) {

        static Action fromMap(Map<String, Object> m) {
            return new Action(
                    Maps.str(m, "name"),
                    Maps.str(m, "type"),
                    Maps.str(m, "description"),
                    Maps.objMap(m, "attributes"),
                    Maps.list(m, "stores", Store::fromMap),
                    Maps.list(m, "inputs", DataSource::fromMap),
                    Maps.list(m, "outputs", DataSource::fromMap));
        }
    }

    /** Typed view of the full CC payload. */
    public record Payload(
            Map<String, Object> attributes,
            List<Store> stores,
            List<DataSource> inputs,
            List<DataSource> outputs,
            List<Action> actions) {

        static Payload fromMap(Map<String, Object> m) {
            return new Payload(
                    Maps.objMap(m, "attributes"),
                    Maps.list(m, "stores", Store::fromMap),
                    Maps.list(m, "inputs", DataSource::fromMap),
                    Maps.list(m, "outputs", DataSource::fromMap),
                    Maps.list(m, "actions", Action::fromMap));
        }

        /** Find a store by name. */
        public Store getStore(String storeName) {
            for (Store s : stores) {
                if (storeName.equals(s.name())) return s;
            }
            throw new CcSdkException("Store '" + storeName + "' not found");
        }

        /** Resolve credentials for a named store using its CC profile. */
        public StoreCredentials getStoreCredentials(String storeName) {
            return new StoreCredentials(getStore(storeName).profile());
        }
    }

    /** Resolved credentials for a CC data store profile. */
    public record StoreCredentials(
            String profile,
            String awsAccessKeyId,
            String awsSecretAccessKey,
            String region,
            String bucket,
            String endpoint) {

        public StoreCredentials(String profile) {
            this(profile,
                    env(profile + "_AWS_ACCESS_KEY_ID"),
                    env(profile + "_AWS_SECRET_ACCESS_KEY"),
                    env(profile + "_AWS_DEFAULT_REGION"),
                    env(profile + "_AWS_S3_BUCKET"),
                    env(profile + "_AWS_ENDPOINT"));
        }

        /** Build an S3 URI from bucket + root + optional suffix. */
        public String s3Uri(String root, String suffix) {
            String base = "s3://" + bucket + "/" + root;
            return (suffix != null && !suffix.isEmpty()) ? base + "/" + suffix : base;
        }

        /** Return a Map suitable for TileDB Config. */
        public Map<String, String> tileDbConfig() {
            Map<String, String> cfg = new HashMap<>();
            cfg.put("vfs.s3.aws_access_key_id", awsAccessKeyId);
            cfg.put("vfs.s3.aws_secret_access_key", awsSecretAccessKey);
            cfg.put("vfs.s3.region", region);
            if (endpoint != null && !endpoint.isEmpty()) {
                String scheme = "https";
                String ep = endpoint;
                int sep = ep.indexOf("://");
                if (sep >= 0) {
                    scheme = ep.substring(0, sep);
                    ep = ep.substring(sep + 3);
                }
                cfg.put("vfs.s3.scheme", scheme);
                cfg.put("vfs.s3.endpoint_override", ep);
                cfg.put("vfs.s3.use_virtual_addressing", "false");
            }
            return cfg;
        }

        private static String env(String key) {
            String val = System.getenv(key);
            return val != null ? val : "";
        }
    }

    // ---------------------------------------------------------------------
    // Operations — each maps 1:1 to a cc-sdk serve command
    // ---------------------------------------------------------------------

    public Payload getPayload() throws IOException {
        Map<String, Object> resp = request("get-payload");
        return Payload.fromMap(Maps.objMap(resp, "data"));
    }

    public void copyToLocal(String dsName, String pathkey, String datakey, String localpath)
            throws IOException {
        request("copy-to-local",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey,
                "localpath", localpath);
    }

    public void copyToRemote(String dsName, String pathkey, String datakey, String localpath)
            throws IOException {
        request("copy-to-remote",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey,
                "localpath", localpath);
    }

    public void copyFolderToRemote(String dsName, String pathkey, String datakey, String localpath)
            throws IOException {
        request("copy-folder-to-remote",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey,
                "localpath", localpath);
    }

    public byte[] get(String dsName, String pathkey, String datakey) throws IOException {
        Map<String, Object> resp = request("get",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey);
        return Base64.getDecoder().decode((String) resp.get("data_b64"));
    }

    public InputStream getReader(String dsName, String pathkey, String datakey) throws IOException {
        return new ByteArrayInputStream(get(dsName, pathkey, datakey));
    }

    public void put(String dsName, String pathkey, String datakey, byte[] data) throws IOException {
        request("put",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey,
                "data_b64", Base64.getEncoder().encodeToString(data));
    }

    public void copy(String srcDs, String srcPathkey, String srcDatakey,
                     String dstDs, String dstPathkey, String dstDatakey) throws IOException {
        request("copy",
                "src_ds", srcDs,
                "src_pathkey", srcPathkey,
                "src_datakey", srcDatakey,
                "dst_ds", dstDs,
                "dst_pathkey", dstPathkey,
                "dst_datakey", dstDatakey);
    }

    // ---------------------------------------------------------------------
    // Lifecycle
    // ---------------------------------------------------------------------

    public void shutdown() throws IOException {
        if (!process.isAlive()) return;
        lock.lock();
        try {
            stdin.write("{\"id\":\"shutdown\",\"cmd\":\"shutdown\"}\n");
            stdin.flush();
        } finally {
            lock.unlock();
        }
        try {
            if (!process.waitFor(5, TimeUnit.SECONDS)) {
                process.destroyForcibly();
                process.waitFor(2, TimeUnit.SECONDS);
            }
        } catch (InterruptedException e) {
            process.destroyForcibly();
            Thread.currentThread().interrupt();
        }
    }

    @Override
    public void close() throws IOException {
        shutdown();
    }

    // ---------------------------------------------------------------------
    // Internal: request/response
    // ---------------------------------------------------------------------

    private Map<String, Object> request(String cmd, String... kvPairs) throws IOException {
        if (!process.isAlive()) {
            throw new CcSdkException("cc-sdk process exited unexpectedly: " + readStderr());
        }

        StringBuilder sb = new StringBuilder(64 + kvPairs.length * 16);
        sb.append("{\"id\":\"").append(counter.incrementAndGet())
          .append("\",\"cmd\":\"").append(Json.escape(cmd)).append('"');
        for (int i = 0; i < kvPairs.length; i += 2) {
            String val = kvPairs[i + 1];
            if (val != null && !val.isEmpty()) {
                sb.append(",\"").append(Json.escape(kvPairs[i]))
                  .append("\":\"").append(Json.escape(val)).append('"');
            }
        }
        sb.append("}\n");

        String respLine;
        lock.lock();
        try {
            stdin.write(sb.toString());
            stdin.flush();
            respLine = stdout.readLine();
        } finally {
            lock.unlock();
        }

        if (respLine == null) {
            throw new CcSdkException("cc-sdk returned empty response: " + readStderr());
        }

        Map<String, Object> resp = Json.parseObject(respLine);
        if (!Boolean.TRUE.equals(resp.get("ok"))) {
            Object err = resp.get("error");
            throw new CcSdkException(err != null ? err.toString() : "unknown error");
        }
        return resp;
    }

    private void shutdownQuietly() {
        try { shutdown(); } catch (Exception ignored) { }
    }

    private String readStderr() {
        try {
            BufferedReader err = new BufferedReader(
                    new InputStreamReader(process.getErrorStream(), StandardCharsets.UTF_8));
            StringBuilder sb = new StringBuilder();
            String line;
            while (err.ready() && (line = err.readLine()) != null) {
                sb.append(line).append('\n');
            }
            return sb.toString().trim();
        } catch (IOException e) {
            return "(could not read stderr)";
        }
    }

    // ---------------------------------------------------------------------
    // Binary discovery: $CC_SDK_BIN > bundled-in-jar > PATH
    // ---------------------------------------------------------------------

    private static String resolveBin() {
        String bin = System.getenv(CC_SDK_BIN_ENV);
        if (bin != null && !bin.isEmpty()) return bin;
        try {
            return extractBundledBinary();
        } catch (IOException ignored) {
            return DEFAULT_BIN;
        }
    }

    private static String extractBundledBinary() throws IOException {
        String os = System.getProperty("os.name", "").toLowerCase();
        String arch = System.getProperty("os.arch", "").toLowerCase();

        String goos;
        if (os.contains("linux")) goos = "linux";
        else if (os.contains("mac") || os.contains("darwin")) goos = "darwin";
        else if (os.contains("win")) goos = "windows";
        else throw new IOException("Unsupported OS: " + os);

        String goarch;
        if (arch.equals("amd64") || arch.equals("x86_64")) goarch = "amd64";
        else if (arch.equals("aarch64") || arch.equals("arm64")) goarch = "arm64";
        else throw new IOException("Unsupported arch: " + arch);

        String ext = goos.equals("windows") ? ".exe" : "";
        String resourceName = "/natives/cc-sdk-" + goos + "-" + goarch + ext;

        try (InputStream in = CcSdk.class.getResourceAsStream(resourceName)) {
            if (in == null) throw new IOException("Binary not found in JAR: " + resourceName);

            String cacheDir = System.getProperty("user.home") + File.separator
                    + ".cc-sdk" + File.separator + "bin";
            new File(cacheDir).mkdirs();

            File target = new File(cacheDir, "cc-sdk" + ext);
            if (!target.exists() || target.length() == 0) {
                try (FileOutputStream out = new FileOutputStream(target)) {
                    byte[] buf = new byte[8192];
                    int n;
                    while ((n = in.read(buf)) != -1) out.write(buf, 0, n);
                }
                target.setExecutable(true);
            }
            return target.getAbsolutePath();
        }
    }

    // ---------------------------------------------------------------------
    // Map helpers — defensive casts in one place instead of scattered
    // ---------------------------------------------------------------------

    private static final class Maps {
        @SuppressWarnings("unchecked")
        static Map<String, Object> objMap(Map<String, Object> m, String key) {
            Object v = m.get(key);
            return v instanceof Map ? (Map<String, Object>) v : Map.of();
        }

        @SuppressWarnings("unchecked")
        static Map<String, String> stringMap(Map<String, Object> m, String key) {
            Object v = m.get(key);
            if (!(v instanceof Map<?, ?> raw)) return Map.of();
            Map<String, String> out = new HashMap<>(raw.size());
            for (Map.Entry<?, ?> e : raw.entrySet()) {
                out.put(String.valueOf(e.getKey()),
                        e.getValue() != null ? e.getValue().toString() : "");
            }
            return out;
        }

        static String str(Map<String, Object> m, String key) {
            Object v = m.get(key);
            return v != null ? v.toString() : "";
        }

        @SuppressWarnings("unchecked")
        static <T> List<T> list(Map<String, Object> m, String key,
                                Function<Map<String, Object>, T> mapper) {
            Object v = m.get(key);
            if (!(v instanceof List<?> raw)) return List.of();
            List<T> out = new ArrayList<>(raw.size());
            for (Object item : raw) {
                if (item instanceof Map) out.add(mapper.apply((Map<String, Object>) item));
            }
            return out;
        }
    }

    // ---------------------------------------------------------------------
    // Minimal JSON — recursive-descent parser + string escaper. No deps.
    // ---------------------------------------------------------------------

    private static final class Json {
        @SuppressWarnings("unchecked")
        static Map<String, Object> parseObject(String json) {
            Object v = new Parser(json.trim()).parseValue();
            if (!(v instanceof Map)) {
                throw new CcSdkException("expected JSON object, got: " + json);
            }
            return (Map<String, Object>) v;
        }

        static String escape(String s) {
            if (s == null) return "";
            StringBuilder sb = new StringBuilder(s.length() + 4);
            for (int i = 0; i < s.length(); i++) {
                char c = s.charAt(i);
                switch (c) {
                    case '\\': sb.append("\\\\"); break;
                    case '"':  sb.append("\\\""); break;
                    case '\n': sb.append("\\n"); break;
                    case '\r': sb.append("\\r"); break;
                    case '\t': sb.append("\\t"); break;
                    default:
                        if (c < 0x20) sb.append(String.format("\\u%04x", (int) c));
                        else sb.append(c);
                }
            }
            return sb.toString();
        }

        private static final class Parser {
            private final String in;
            private int pos;

            Parser(String in) { this.in = in; }

            Object parseValue() {
                skipWs();
                if (pos >= in.length()) throw new CcSdkException("unexpected end of JSON");
                char c = in.charAt(pos);
                if (c == '{') return parseObject();
                if (c == '[') return parseArray();
                if (c == '"') return parseString();
                if (c == 't' || c == 'f') return parseBool();
                if (c == 'n') return parseNull();
                return parseNumber();
            }

            private Map<String, Object> parseObject() {
                Map<String, Object> map = new HashMap<>();
                expect('{');
                skipWs();
                if (peek() == '}') { pos++; return map; }
                while (true) {
                    skipWs();
                    String key = parseString();
                    skipWs();
                    expect(':');
                    map.put(key, parseValue());
                    skipWs();
                    if (peek() == ',') { pos++; continue; }
                    break;
                }
                expect('}');
                return map;
            }

            private List<Object> parseArray() {
                List<Object> list = new ArrayList<>();
                expect('[');
                skipWs();
                if (peek() == ']') { pos++; return list; }
                while (true) {
                    list.add(parseValue());
                    skipWs();
                    if (peek() == ',') { pos++; continue; }
                    break;
                }
                expect(']');
                return list;
            }

            private String parseString() {
                expect('"');
                StringBuilder sb = new StringBuilder();
                while (pos < in.length()) {
                    char c = in.charAt(pos++);
                    if (c == '"') return sb.toString();
                    if (c == '\\') {
                        if (pos >= in.length()) break;
                        char esc = in.charAt(pos++);
                        switch (esc) {
                            case '"':  sb.append('"'); break;
                            case '\\': sb.append('\\'); break;
                            case '/':  sb.append('/'); break;
                            case 'b':  sb.append('\b'); break;
                            case 'f':  sb.append('\f'); break;
                            case 'n':  sb.append('\n'); break;
                            case 'r':  sb.append('\r'); break;
                            case 't':  sb.append('\t'); break;
                            case 'u':
                                sb.append((char) Integer.parseInt(in.substring(pos, pos + 4), 16));
                                pos += 4;
                                break;
                            default: sb.append(esc);
                        }
                    } else {
                        sb.append(c);
                    }
                }
                throw new CcSdkException("unterminated string at " + pos);
            }

            private Number parseNumber() {
                int start = pos;
                if (peek() == '-') pos++;
                while (pos < in.length() && Character.isDigit(in.charAt(pos))) pos++;
                boolean isFloat = false;
                if (peek() == '.') {
                    isFloat = true; pos++;
                    while (pos < in.length() && Character.isDigit(in.charAt(pos))) pos++;
                }
                char p = peek();
                if (p == 'e' || p == 'E') {
                    isFloat = true; pos++;
                    char s = peek();
                    if (s == '+' || s == '-') pos++;
                    while (pos < in.length() && Character.isDigit(in.charAt(pos))) pos++;
                }
                String num = in.substring(start, pos);
                if (isFloat) return Double.parseDouble(num);
                long l = Long.parseLong(num);
                return (l >= Integer.MIN_VALUE && l <= Integer.MAX_VALUE) ? (int) l : (Number) l;
            }

            private Boolean parseBool() {
                if (in.startsWith("true", pos))  { pos += 4; return Boolean.TRUE; }
                if (in.startsWith("false", pos)) { pos += 5; return Boolean.FALSE; }
                throw new CcSdkException("expected boolean at " + pos);
            }

            private Object parseNull() {
                if (in.startsWith("null", pos)) { pos += 4; return null; }
                throw new CcSdkException("expected null at " + pos);
            }

            private void expect(char c) {
                skipWs();
                if (pos >= in.length() || in.charAt(pos) != c) {
                    throw new CcSdkException("expected '" + c + "' at " + pos);
                }
                pos++;
            }

            private char peek() {
                return pos < in.length() ? in.charAt(pos) : '\0';
            }

            private void skipWs() {
                while (pos < in.length() && Character.isWhitespace(in.charAt(pos))) pos++;
            }
        }
    }

    /** Exception thrown when cc-sdk returns an error or the client fails. */
    public static class CcSdkException extends RuntimeException {
        private static final long serialVersionUID = 1L;
        public CcSdkException(String message) { super(message); }
    }
}
