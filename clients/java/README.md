# cc-sdk (Java)

Java client for the [USACE Cloud Compute SDK](https://github.com/USACE/cc-go-sdk).

Requires Java 17+. The `cc-sdk` binary is bundled in the JAR and extracted at runtime
(or set `CC_SDK_BIN` to override). Zero external dependencies beyond the JDK.

## Install

**Gradle:**
```groovy
implementation 'mil.army.usace:cc-sdk:2.0.0'
```

**Maven:**
```xml
<dependency>
    <groupId>mil.army.usace</groupId>
    <artifactId>cc-sdk</artifactId>
    <version>2.0.0</version>
</dependency>
```

## Usage

```java
import mil.army.usace.cc.CcSdk;
import mil.army.usace.cc.CcSdk.Payload;
import mil.army.usace.cc.CcSdk.DataSource;

try (CcSdk sdk = new CcSdk()) {
    Payload payload = sdk.getPayload();

    // Record accessors — payload types are Java records
    payload.attributes().get("catalog_id");
    DataSource input = payload.inputs().get(0);
    input.paths().get("watershed");
    input.name();

    // File operations — datakey is "" when not needed
    sdk.copyToLocal("ds-name", "pathkey", "", "/tmp/local.geojson");
    sdk.copyToRemote("ds-name", "pathkey", "", "/tmp/output.dss");

    // Bytes in/out
    byte[] data = sdk.get("ds-name", "pathkey", "");
    sdk.put("ds-name", "pathkey", "", data);
}
```

The SDK spawns `cc-sdk serve` once per `CcSdk` instance and reuses the process
for every call — S3/FSB connections are cached across operations.
