# cc-sdk

Universal CLI for the Cloud Compute SDK. Replaces language-specific SDKs
(cc-py-sdk, cc-java-sdk) with a single binary that any language can use.

- **Go plugins** can import `cc-go-sdk` as a library OR use `cc-sdk`
- **Python, Java, Rust, etc.** plugins use `cc-sdk` via subprocess
- Thin language clients in [`clients/`](../../clients/) for convenience (optional)

## Build

```bash
cd /path/to/cc-go-sdk
go build -o cc-sdk ./cmd/cc-sdk
```

## Modes

### One-shot CLI (for scripting and simple plugins)

Each invocation initializes the PluginManager, runs the command, and exits.

```bash
cc-sdk get-payload
cc-sdk copy-to-local  <ds> <pathkey> [datakey] <localpath>
cc-sdk copy-to-remote <ds> <pathkey> [datakey] <localpath>
cc-sdk copy-folder-to-remote <ds> <pathkey> [datakey] <localpath>
cc-sdk get   <ds> <pathkey> [datakey]
cc-sdk put   <ds> <pathkey> [datakey]       # reads data from stdin
cc-sdk copy  <src_ds> <src_pathkey> [src_datakey] <dst_ds> <dst_pathkey> [dst_datakey]
```

### Serve mode (for plugins with many I/O calls)

Starts a long-running process that initializes the PluginManager **once** and
communicates via a JSON-line protocol over stdin/stdout. Persistent S3/FSB
connections are reused across calls.

```bash
cc-sdk serve
```

After initialization, writes a ready signal to stdout:

```json
{"ok": true, "cmd": "ready"}
```

Then reads one JSON request per line from stdin, writes one JSON response per
line to stdout. All logging goes to stderr.

#### Request format

```json
{"id": "1", "cmd": "get-payload"}
{"id": "2", "cmd": "copy-to-local", "ds_name": "x", "pathkey": "y", "localpath": "/tmp/f"}
{"id": "3", "cmd": "copy-to-remote", "ds_name": "x", "pathkey": "y", "localpath": "/tmp/f"}
{"id": "4", "cmd": "copy-folder-to-remote", "ds_name": "x", "pathkey": "y", "localpath": "/tmp/dir"}
{"id": "5", "cmd": "get", "ds_name": "x", "pathkey": "y"}
{"id": "6", "cmd": "put", "ds_name": "x", "pathkey": "y", "data_b64": "<base64>"}
{"id": "7", "cmd": "copy", "src_ds": "a", "src_pathkey": "b", "dst_ds": "c", "dst_pathkey": "d"}
{"id": "8", "cmd": "shutdown"}
```

Optional fields: `datakey`, `src_datakey`, `dst_datakey`.

#### Response format

```json
{"id": "1", "ok": true, "data": {"attributes": {}, "stores": [], ...}}
{"id": "2", "ok": true}
{"id": "5", "ok": true, "data_b64": "<base64 encoded bytes>"}
{"id": "8", "ok": false, "error": "message"}
```

Malformed requests receive an error response without crashing the process.

## Language clients

Optional thin clients live in [`clients/`](../../clients/). They manage the
`cc-sdk serve` process and expose idiomatic APIs. They are convenience
wrappers, not SDKs — the binary is the SDK.

### Python

Copy [`clients/python/cc.py`](../../clients/python/cc.py) into your project:

```python
from cc import CcSdk

with CcSdk() as sdk:
    payload = sdk.get_payload()      # plain dict
    sdk.copy_to_local("ds", "key", "/tmp/file.txt")
    sdk.copy_to_remote("ds", "key", "/tmp/file.txt")
```

Or use one-shot functions for simple scripts:

```python
from cc import get_payload, copy_to_local
payload = get_payload()
copy_to_local("ds", "key", "/tmp/file.txt")
```

### Java

Copy [`clients/java/.../CcSdk.java`](../../clients/java/src/main/java/mil/army/usace/cc/CcSdk.java)
into your project:

```java
try (CcSdk sdk = new CcSdk()) {
    Map<String, Object> payload = sdk.getPayload();
    sdk.copyToLocal("ds", "key", "/tmp/file.txt");
    sdk.copyToRemote("ds", "key", "/tmp/file.txt");
}
```

Or use one-shot static methods:

```java
Map<String, Object> payload = CcSdk.oneShotGetPayload();
CcSdk.oneShotCopyToLocal("ds", "key", "/tmp/file.txt");
```

## Environment variables

| Variable | Description |
|---|---|
| `CC_PAYLOAD_ID` | Payload identifier |
| `CC_MANIFEST_ID` | Manifest identifier |
| `CC_ROOT` | Root path in the store for payload lookup |
| `CC_AWS_ACCESS_KEY_ID` | AWS access key (CC profile) |
| `CC_AWS_SECRET_ACCESS_KEY` | AWS secret key (CC profile) |
| `CC_AWS_DEFAULT_REGION` | AWS region |
| `CC_AWS_S3_BUCKET` | S3 bucket name |
| `CC_AWS_ENDPOINT` | Optional S3-compatible endpoint |
| `FSB_ROOT_PATH` | Filesystem backing store root (replaces S3/MinIO for local dev) |

Additional store profiles follow the pattern `<PROFILE>_AWS_ACCESS_KEY_ID`, etc.

## Using TileDB and other native data stores

cc-sdk handles file I/O and payload management. For data-plane operations
like TileDB arrays, database connections, or other native stores, use each
store's native library directly. The payload contains the store configuration,
and credentials follow the CC profile convention.

### Credential convention

Each store in the payload has a `profile` field (e.g. `"FFRD"`, `"CC"`).
Credentials are in environment variables prefixed with that profile:

```
<PROFILE>_AWS_ACCESS_KEY_ID
<PROFILE>_AWS_SECRET_ACCESS_KEY
<PROFILE>_AWS_DEFAULT_REGION
<PROFILE>_AWS_S3_BUCKET
<PROFILE>_AWS_ENDPOINT          (optional, for S3-compatible endpoints)
```

### Python + TileDB

```python
import os
import tiledb
from cc import CcSdk

with CcSdk() as sdk:
    payload = sdk.get_payload()

    # Find the event store
    store = next(s for s in payload.stores if s.name == "event-store")
    profile = store.profile   # e.g. "FFRD"

    # Resolve credentials using the CC profile convention
    config = tiledb.Config({
        "vfs.s3.aws_access_key_id":     os.environ[f"{profile}_AWS_ACCESS_KEY_ID"],
        "vfs.s3.aws_secret_access_key": os.environ[f"{profile}_AWS_SECRET_ACCESS_KEY"],
        "vfs.s3.region":                os.environ[f"{profile}_AWS_DEFAULT_REGION"],
    })

    # Optional: S3-compatible endpoint (MinIO, LocalStack, etc.)
    endpoint = os.environ.get(f"{profile}_AWS_ENDPOINT")
    if endpoint:
        config["vfs.s3.endpoint_override"] = endpoint
        config["vfs.s3.use_virtual_addressing"] = "false"

    ctx = tiledb.Ctx(config)
    uri = f"s3://{os.environ[f'{profile}_AWS_S3_BUCKET']}/{store.params['root']}/eventdb"
    array = tiledb.open(uri, ctx=ctx)
```

### Java + TileDB

```java
import io.tiledb.java.api.*;

CcSdk.Payload payload = sdk.getPayload();
Map<String, Object> store = payload.getStores().stream()
    .filter(s -> s.getName().equals("event-store"))
    .findFirst().orElseThrow();

String profile = store.getProfile();
String key    = System.getenv(profile + "_AWS_ACCESS_KEY_ID");
String secret = System.getenv(profile + "_AWS_SECRET_ACCESS_KEY");
String region = System.getenv(profile + "_AWS_DEFAULT_REGION");
String bucket = System.getenv(profile + "_AWS_S3_BUCKET");

Config config = new Config();
config.set("vfs.s3.aws_access_key_id", key);
config.set("vfs.s3.aws_secret_access_key", secret);
config.set("vfs.s3.region", region);

Context ctx = new Context(config);
String uri = "s3://" + bucket + "/" + store.getParams().get("root") + "/eventdb";
Array array = new Array(ctx, uri, QueryType.TILEDB_READ);
```

### Go (direct SDK)

```go
import (
    cc "github.com/usace-cloud-compute/cc-go-sdk"
    tiledbstore "github.com/usace-cloud-compute/cc-go-sdk/tiledb-store"
)

pm, _ := cc.InitPluginManager()
store, _ := pm.GetStore("event-store")

// TileDB store is already connected via PluginManager
if mds, ok := store.Session.(cc.MultiDimensionalArrayStore); ok {
    result, _ := mds.GetArray(cc.GetArrayInput{
        DataPath: "my-array",
        Attrs:    []string{"temperature"},
    })
}
```

### Why this approach?

| Concern | Who handles it |
|---|---|
| Payload retrieval | cc-sdk (one implementation) |
| File copy (S3/FSB) | cc-sdk (one implementation) |
| Credential resolution | CC env var convention (documented, language-agnostic) |
| TileDB array I/O | TileDB Inc's native libraries (maintained by TileDB) |
| Database connections | Native drivers (JDBC, psycopg2, etc.) |

cc-sdk owns the control plane. Native libraries own the data plane.
No serialization overhead. No drift — credentials follow one convention,
data libraries are maintained by their respective vendors.
