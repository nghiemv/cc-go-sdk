# cc-sdk (Java)

Java client for the [USACE Cloud Compute SDK](https://github.com/USACE/cc-go-sdk).

Requires the `cc-sdk` binary on PATH. Zero external dependencies.

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

try (CcSdk sdk = new CcSdk()) {
    Payload payload = sdk.getPayload();

    // Typed accessors
    payload.getAttributes().get("catalog_id");
    payload.getInputs().get(0).getPaths().get("watershed");
    payload.getInputs().get(0).getName();

    // File operations
    sdk.copyToLocal("ds-name", "pathkey", "/tmp/local.geojson");
    sdk.copyToRemote("ds-name", "pathkey", "/tmp/output.dss");
}
```

## One-shot mode

For simple scripts with few I/O calls:

```java
Payload payload = CcSdk.oneShotGetPayload();
CcSdk.oneShotCopyToLocal("ds-name", "pathkey", "/tmp/file.txt");
```
