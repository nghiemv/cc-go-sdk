package mil.army.usace.cc;

import java.io.*;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.locks.ReentrantLock;

/**
 * Thin Java client for the cc-sdk binary.
 *
 * <p>Drop this single file into any Java project to use the Cloud Compute SDK.
 * No external dependencies beyond the JDK.
 *
 * <h3>Usage (persistent mode — recommended):</h3>
 * <pre>{@code
 * try (CcSdk sdk = new CcSdk()) {
 *     Map<String, Object> payload = sdk.getPayload();
 *     sdk.copyToLocal("ds", "key", "/tmp/file.txt");
 *     sdk.copyToRemote("ds", "key", "/tmp/file.txt");
 * }
 * }</pre>
 *
 * <h3>Usage (one-shot — simpler, slower for multiple calls):</h3>
 * <pre>{@code
 * Map<String, Object> payload = CcSdk.oneShotGetPayload();
 * CcSdk.oneShotCopyToLocal("ds", "key", "/tmp/file.txt");
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

    /**
     * Start a cc-sdk serve process using the default binary path.
     */
    public CcSdk() throws IOException {
        this(getBin());
    }

    /**
     * Start a cc-sdk serve process using a custom binary path.
     */
    public CcSdk(String binPath) throws IOException {
        ProcessBuilder pb = new ProcessBuilder(binPath, "serve")
                .redirectErrorStream(false);
        this.process = pb.start();
        this.stdin = new BufferedWriter(
                new OutputStreamWriter(process.getOutputStream(), StandardCharsets.UTF_8));
        this.stdout = new BufferedReader(
                new InputStreamReader(process.getInputStream(), StandardCharsets.UTF_8));

        // Wait for ready signal
        String readyLine = this.stdout.readLine();
        if (readyLine == null) {
            String stderr = readStderr();
            throw new CcSdkException("cc-sdk failed to start: " + stderr);
        }
        Map<String, Object> ready = parseJson(readyLine);
        if (!Boolean.TRUE.equals(ready.get("ok")) || !"ready".equals(ready.get("cmd"))) {
            throw new CcSdkException("cc-sdk unexpected ready signal: " + readyLine);
        }

        Runtime.getRuntime().addShutdownHook(new Thread(this::shutdownQuietly));
    }

    // ----- Payload types (idiomatic Java access) -----

    /** Typed wrapper for a CC data source (input or output). */
    public static class DataSource {
        private final Map<String, Object> data;

        @SuppressWarnings("unchecked")
        DataSource(Map<String, Object> data) { this.data = data != null ? data : Map.of(); }

        public String getName() { return str("name"); }
        public String getId() { return str("id"); }
        public String getStoreName() { return str("store_name"); }

        @SuppressWarnings("unchecked")
        public Map<String, String> getPaths() {
            Object p = data.get("paths");
            return p instanceof Map ? (Map<String, String>) p : Map.of();
        }

        @SuppressWarnings("unchecked")
        public Map<String, String> getDataPaths() {
            Object p = data.get("data_paths");
            return p instanceof Map ? (Map<String, String>) p : Map.of();
        }

        /** Access the underlying raw map. */
        public Map<String, Object> toMap() { return data; }

        private String str(String key) {
            Object v = data.get(key);
            return v != null ? v.toString() : "";
        }
    }

    /** Typed wrapper for a CC data store. */
    public static class Store {
        private final Map<String, Object> data;

        @SuppressWarnings("unchecked")
        Store(Map<String, Object> data) { this.data = data != null ? data : Map.of(); }

        public String getName() { return str("name"); }
        public String getId() { return str("id"); }
        public String getStoreType() { return str("store_type"); }
        public String getProfile() { return str("profile"); }

        @SuppressWarnings("unchecked")
        public Map<String, Object> getParams() {
            Object p = data.get("params");
            return p instanceof Map ? (Map<String, Object>) p : Map.of();
        }

        public Map<String, Object> toMap() { return data; }

        private String str(String key) {
            Object v = data.get(key);
            return v != null ? v.toString() : "";
        }
    }

    /** Typed wrapper for a CC action. */
    public static class Action {
        private final Map<String, Object> data;

        @SuppressWarnings("unchecked")
        Action(Map<String, Object> data) { this.data = data != null ? data : Map.of(); }

        public String getName() { return str("name"); }
        public String getType() { return str("type"); }
        public String getDescription() { return str("description"); }

        @SuppressWarnings("unchecked")
        public Map<String, Object> getAttributes() {
            Object a = data.get("attributes");
            return a instanceof Map ? (Map<String, Object>) a : Map.of();
        }

        @SuppressWarnings("unchecked")
        public java.util.List<Store> getStores() {
            return toStores((java.util.List<Map<String, Object>>) data.getOrDefault("stores", java.util.List.of()));
        }

        @SuppressWarnings("unchecked")
        public java.util.List<DataSource> getInputs() {
            return toDataSources((java.util.List<Map<String, Object>>) data.getOrDefault("inputs", java.util.List.of()));
        }

        @SuppressWarnings("unchecked")
        public java.util.List<DataSource> getOutputs() {
            return toDataSources((java.util.List<Map<String, Object>>) data.getOrDefault("outputs", java.util.List.of()));
        }

        public Map<String, Object> toMap() { return data; }

        private String str(String key) {
            Object v = data.get(key);
            return v != null ? v.toString() : "";
        }
    }

    /** Typed wrapper for the full CC payload. */
    public static class Payload {
        private final Map<String, Object> data;

        @SuppressWarnings("unchecked")
        Payload(Map<String, Object> data) { this.data = data != null ? data : Map.of(); }

        @SuppressWarnings("unchecked")
        public Map<String, Object> getAttributes() {
            Object a = data.get("attributes");
            return a instanceof Map ? (Map<String, Object>) a : Map.of();
        }

        @SuppressWarnings("unchecked")
        public java.util.List<Store> getStores() {
            return toStores((java.util.List<Map<String, Object>>) data.getOrDefault("stores", java.util.List.of()));
        }

        @SuppressWarnings("unchecked")
        public java.util.List<DataSource> getInputs() {
            return toDataSources((java.util.List<Map<String, Object>>) data.getOrDefault("inputs", java.util.List.of()));
        }

        @SuppressWarnings("unchecked")
        public java.util.List<DataSource> getOutputs() {
            return toDataSources((java.util.List<Map<String, Object>>) data.getOrDefault("outputs", java.util.List.of()));
        }

        @SuppressWarnings("unchecked")
        public java.util.List<Action> getActions() {
            return toActions((java.util.List<Map<String, Object>>) data.getOrDefault("actions", java.util.List.of()));
        }

        /** Access the underlying raw map. */
        public Map<String, Object> toMap() { return data; }

        /** Find a store by name. */
        public Store getStore(String name) {
            for (Store s : getStores()) {
                if (name.equals(s.getName())) return s;
            }
            throw new CcSdkException("Store '" + name + "' not found");
        }

        /** Resolve credentials for a named store using its CC profile. */
        public StoreCredentials getStoreCredentials(String storeName) {
            Store store = getStore(storeName);
            return new StoreCredentials(store.getProfile());
        }
    }

    @SuppressWarnings("unchecked")
    private static java.util.List<DataSource> toDataSources(java.util.List<Map<String, Object>> list) {
        java.util.List<DataSource> result = new java.util.ArrayList<>(list.size());
        for (Map<String, Object> m : list) result.add(new DataSource(m));
        return result;
    }

    @SuppressWarnings("unchecked")
    private static java.util.List<Store> toStores(java.util.List<Map<String, Object>> list) {
        java.util.List<Store> result = new java.util.ArrayList<>(list.size());
        for (Map<String, Object> m : list) result.add(new Store(m));
        return result;
    }

    @SuppressWarnings("unchecked")
    private static java.util.List<Action> toActions(java.util.List<Map<String, Object>> list) {
        java.util.List<Action> result = new java.util.ArrayList<>(list.size());
        for (Map<String, Object> m : list) result.add(new Action(m));
        return result;
    }

    /** Resolved credentials for a CC data store profile. */
    public static class StoreCredentials {
        private final String profile;
        public final String awsAccessKeyId;
        public final String awsSecretAccessKey;
        public final String region;
        public final String bucket;
        public final String endpoint;

        StoreCredentials(String profile) {
            this.profile = profile;
            this.awsAccessKeyId = env(profile + "_AWS_ACCESS_KEY_ID");
            this.awsSecretAccessKey = env(profile + "_AWS_SECRET_ACCESS_KEY");
            this.region = env(profile + "_AWS_DEFAULT_REGION");
            this.bucket = env(profile + "_AWS_S3_BUCKET");
            this.endpoint = env(profile + "_AWS_ENDPOINT");
        }

        public String getProfile() { return profile; }

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
                if (ep.contains("://")) {
                    String[] parts = ep.split("://", 2);
                    scheme = parts[0];
                    ep = parts[1];
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

    // ----- Persistent mode (serve) -----

    /**
     * Fetch the payload with typed accessors.
     */
    @SuppressWarnings("unchecked")
    public Payload getPayload() throws IOException {
        Map<String, Object> resp = request("get-payload");
        return new Payload((Map<String, Object>) resp.get("data"));
    }

    /**
     * Download a remote object to a local file.
     */
    public void copyToLocal(String dsName, String pathkey, String localpath)
            throws IOException {
        copyToLocal(dsName, pathkey, "", localpath);
    }

    /**
     * Download a remote object to a local file with datakey.
     */
    public void copyToLocal(String dsName, String pathkey, String datakey, String localpath)
            throws IOException {
        request("copy-to-local",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey,
                "localpath", localpath);
    }

    /**
     * Upload a local file to the remote store.
     */
    public void copyToRemote(String dsName, String pathkey, String localpath)
            throws IOException {
        copyToRemote(dsName, pathkey, "", localpath);
    }

    /**
     * Upload a local file to the remote store with datakey.
     */
    public void copyToRemote(String dsName, String pathkey, String datakey, String localpath)
            throws IOException {
        request("copy-to-remote",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey,
                "localpath", localpath);
    }

    /**
     * Recursively upload a local directory.
     */
    public void copyFolderToRemote(String dsName, String pathkey, String localpath)
            throws IOException {
        copyFolderToRemote(dsName, pathkey, "", localpath);
    }

    /**
     * Recursively upload a local directory with datakey.
     */
    public void copyFolderToRemote(String dsName, String pathkey, String datakey, String localpath)
            throws IOException {
        request("copy-folder-to-remote",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey,
                "localpath", localpath);
    }

    /**
     * Fetch remote object as raw bytes.
     */
    public byte[] get(String dsName, String pathkey) throws IOException {
        return get(dsName, pathkey, "");
    }

    /**
     * Fetch remote object as raw bytes with datakey.
     */
    public byte[] get(String dsName, String pathkey, String datakey) throws IOException {
        Map<String, Object> resp = request("get",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey);
        String b64 = (String) resp.get("data_b64");
        return Base64.getDecoder().decode(b64);
    }

    /**
     * Fetch remote object as an InputStream.
     */
    public InputStream getReader(String dsName, String pathkey) throws IOException {
        return new ByteArrayInputStream(get(dsName, pathkey));
    }

    /**
     * Upload bytes to a remote object.
     */
    public void put(String dsName, String pathkey, byte[] data) throws IOException {
        put(dsName, pathkey, "", data);
    }

    /**
     * Upload bytes to a remote object with datakey.
     */
    public void put(String dsName, String pathkey, String datakey, byte[] data)
            throws IOException {
        String b64 = Base64.getEncoder().encodeToString(data);
        request("put",
                "ds_name", dsName,
                "pathkey", pathkey,
                "datakey", datakey,
                "data_b64", b64);
    }

    /**
     * Copy between data sources.
     */
    public void copy(String srcDs, String srcPathkey, String dstDs, String dstPathkey)
            throws IOException {
        copy(srcDs, srcPathkey, "", dstDs, dstPathkey, "");
    }

    /**
     * Copy between data sources with datakeys.
     */
    public void copy(String srcDs, String srcPathkey, String srcDatakey,
                     String dstDs, String dstPathkey, String dstDatakey)
            throws IOException {
        request("copy",
                "src_ds", srcDs,
                "src_pathkey", srcPathkey,
                "src_datakey", srcDatakey,
                "dst_ds", dstDs,
                "dst_pathkey", dstPathkey,
                "dst_datakey", dstDatakey);
    }

    /**
     * Shut down the cc-sdk serve process.
     */
    public void shutdown() throws IOException {
        if (!process.isAlive()) return;
        try {
            lock.lock();
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

    // ----- One-shot convenience methods (static) -----

    /**
     * One-shot: fetch the payload with typed accessors.
     */
    @SuppressWarnings("unchecked")
    public static Payload oneShotGetPayload() throws IOException {
        String output = runOneShot("get-payload");
        return new Payload((Map<String, Object>) parseJson(output));
    }

    /**
     * One-shot: download a remote object to a local file.
     */
    public static void oneShotCopyToLocal(String dsName, String pathkey, String localpath)
            throws IOException {
        runOneShot("copy-to-local", dsName, pathkey, localpath);
    }

    /**
     * One-shot: upload a local file to the remote store.
     */
    public static void oneShotCopyToRemote(String dsName, String pathkey, String localpath)
            throws IOException {
        runOneShot("copy-to-remote", dsName, pathkey, localpath);
    }

    // ----- Internal -----

    private Map<String, Object> request(String cmd, String... kvPairs) throws IOException {
        if (!process.isAlive()) {
            throw new CcSdkException("cc-sdk process exited unexpectedly: " + readStderr());
        }

        StringBuilder sb = new StringBuilder();
        sb.append("{\"id\":\"").append(counter.incrementAndGet())
          .append("\",\"cmd\":\"").append(escapeJson(cmd)).append("\"");

        for (int i = 0; i < kvPairs.length; i += 2) {
            String key = kvPairs[i];
            String val = kvPairs[i + 1];
            if (val != null && !val.isEmpty()) {
                sb.append(",\"").append(escapeJson(key))
                  .append("\":\"").append(escapeJson(val)).append("\"");
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

        Map<String, Object> resp = parseJson(respLine);
        if (!Boolean.TRUE.equals(resp.get("ok"))) {
            String error = resp.containsKey("error") ? resp.get("error").toString() : "unknown error";
            throw new CcSdkException(error);
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
                sb.append(line).append("\n");
            }
            return sb.toString().trim();
        } catch (IOException e) {
            return "(could not read stderr)";
        }
    }

    private static String getBin() {
        // 1. Explicit override via env var
        String bin = System.getenv(CC_SDK_BIN_ENV);
        if (bin != null && !bin.isEmpty()) return bin;

        // 2. Bundled binary from JAR resources
        try {
            return extractBundledBinary();
        } catch (Exception ignored) {
            // Fall through to PATH
        }

        // 3. Fall back to PATH
        return DEFAULT_BIN;
    }

    private static String extractBundledBinary() throws IOException {
        String os = System.getProperty("os.name", "").toLowerCase();
        String arch = System.getProperty("os.arch", "").toLowerCase();

        String goos, goarch;
        if (os.contains("linux")) goos = "linux";
        else if (os.contains("mac") || os.contains("darwin")) goos = "darwin";
        else if (os.contains("win")) goos = "windows";
        else throw new IOException("Unsupported OS: " + os);

        if (arch.equals("amd64") || arch.equals("x86_64")) goarch = "amd64";
        else if (arch.equals("aarch64") || arch.equals("arm64")) goarch = "arm64";
        else throw new IOException("Unsupported arch: " + arch);

        String ext = goos.equals("windows") ? ".exe" : "";
        String resourceName = "/natives/cc-sdk-" + goos + "-" + goarch + ext;

        try (InputStream in = CcSdk.class.getResourceAsStream(resourceName)) {
            if (in == null) throw new IOException("Binary not found in JAR: " + resourceName);

            // Extract to a stable cache directory (not temp — survives restarts)
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

    private static String runOneShot(String... args) throws IOException {
        String[] cmd = new String[args.length + 1];
        cmd[0] = getBin();
        System.arraycopy(args, 0, cmd, 1, args.length);

        Process proc = new ProcessBuilder(cmd).redirectErrorStream(false).start();
        String output = new String(proc.getInputStream().readAllBytes(), StandardCharsets.UTF_8);
        String stderr = new String(proc.getErrorStream().readAllBytes(), StandardCharsets.UTF_8);

        try {
            int exitCode = proc.waitFor();
            if (exitCode != 0) {
                throw new CcSdkException("cc-sdk " + String.join(" ", args) + " failed: " + stderr.trim());
            }
        } catch (InterruptedException e) {
            proc.destroyForcibly();
            Thread.currentThread().interrupt();
            throw new CcSdkException("interrupted waiting for cc-sdk");
        }
        return output;
    }

    // Minimal JSON parser — no external dependencies.
    // Handles the flat/nested objects cc-sdk produces.
    // For production use, consider replacing with Jackson/Gson.

    @SuppressWarnings("unchecked")
    private static Map<String, Object> parseJson(String json) {
        // Use built-in javax.script or simple recursive descent
        // For JDK 11+, we can use a simple approach
        try {
            return (Map<String, Object>) new SimpleJsonParser(json.trim()).parseValue();
        } catch (Exception e) {
            throw new CcSdkException("Failed to parse JSON: " + e.getMessage());
        }
    }

    private static String escapeJson(String s) {
        if (s == null) return "";
        return s.replace("\\", "\\\\")
                .replace("\"", "\\\"")
                .replace("\n", "\\n")
                .replace("\r", "\\r")
                .replace("\t", "\\t");
    }

    /**
     * Minimal recursive-descent JSON parser. No dependencies.
     * Supports: objects, arrays, strings, numbers, booleans, null.
     */
    private static class SimpleJsonParser {
        private final String input;
        private int pos;

        SimpleJsonParser(String input) {
            this.input = input;
            this.pos = 0;
        }

        Object parseValue() {
            skipWhitespace();
            if (pos >= input.length()) throw new RuntimeException("unexpected end of input");
            char c = input.charAt(pos);
            if (c == '{') return parseObject();
            if (c == '[') return parseArray();
            if (c == '"') return parseString();
            if (c == 't' || c == 'f') return parseBoolean();
            if (c == 'n') return parseNull();
            return parseNumber();
        }

        private Map<String, Object> parseObject() {
            Map<String, Object> map = new HashMap<>();
            expect('{');
            skipWhitespace();
            if (pos < input.length() && input.charAt(pos) == '}') { pos++; return map; }
            while (true) {
                skipWhitespace();
                String key = parseString();
                skipWhitespace();
                expect(':');
                Object value = parseValue();
                map.put(key, value);
                skipWhitespace();
                if (pos < input.length() && input.charAt(pos) == ',') { pos++; continue; }
                break;
            }
            expect('}');
            return map;
        }

        private java.util.List<Object> parseArray() {
            java.util.List<Object> list = new java.util.ArrayList<>();
            expect('[');
            skipWhitespace();
            if (pos < input.length() && input.charAt(pos) == ']') { pos++; return list; }
            while (true) {
                list.add(parseValue());
                skipWhitespace();
                if (pos < input.length() && input.charAt(pos) == ',') { pos++; continue; }
                break;
            }
            expect(']');
            return list;
        }

        private String parseString() {
            expect('"');
            StringBuilder sb = new StringBuilder();
            while (pos < input.length()) {
                char c = input.charAt(pos++);
                if (c == '"') return sb.toString();
                if (c == '\\') {
                    if (pos >= input.length()) break;
                    char esc = input.charAt(pos++);
                    switch (esc) {
                        case '"': sb.append('"'); break;
                        case '\\': sb.append('\\'); break;
                        case '/': sb.append('/'); break;
                        case 'n': sb.append('\n'); break;
                        case 'r': sb.append('\r'); break;
                        case 't': sb.append('\t'); break;
                        case 'u':
                            String hex = input.substring(pos, pos + 4);
                            sb.append((char) Integer.parseInt(hex, 16));
                            pos += 4;
                            break;
                        default: sb.append(esc);
                    }
                } else {
                    sb.append(c);
                }
            }
            throw new RuntimeException("unterminated string");
        }

        private Number parseNumber() {
            int start = pos;
            if (pos < input.length() && input.charAt(pos) == '-') pos++;
            while (pos < input.length() && Character.isDigit(input.charAt(pos))) pos++;
            boolean isFloat = false;
            if (pos < input.length() && input.charAt(pos) == '.') {
                isFloat = true; pos++;
                while (pos < input.length() && Character.isDigit(input.charAt(pos))) pos++;
            }
            if (pos < input.length() && (input.charAt(pos) == 'e' || input.charAt(pos) == 'E')) {
                isFloat = true; pos++;
                if (pos < input.length() && (input.charAt(pos) == '+' || input.charAt(pos) == '-')) pos++;
                while (pos < input.length() && Character.isDigit(input.charAt(pos))) pos++;
            }
            String num = input.substring(start, pos);
            if (isFloat) return Double.parseDouble(num);
            long l = Long.parseLong(num);
            if (l >= Integer.MIN_VALUE && l <= Integer.MAX_VALUE) return (int) l;
            return l;
        }

        private Boolean parseBoolean() {
            if (input.startsWith("true", pos)) { pos += 4; return true; }
            if (input.startsWith("false", pos)) { pos += 5; return false; }
            throw new RuntimeException("expected boolean at position " + pos);
        }

        private Object parseNull() {
            if (input.startsWith("null", pos)) { pos += 4; return null; }
            throw new RuntimeException("expected null at position " + pos);
        }

        private void expect(char c) {
            skipWhitespace();
            if (pos >= input.length() || input.charAt(pos) != c) {
                throw new RuntimeException("expected '" + c + "' at position " + pos);
            }
            pos++;
        }

        private void skipWhitespace() {
            while (pos < input.length() && Character.isWhitespace(input.charAt(pos))) pos++;
        }
    }

    /**
     * Exception thrown when cc-sdk returns an error.
     */
    public static class CcSdkException extends RuntimeException {
        public CcSdkException(String message) {
            super(message);
        }
    }
}
