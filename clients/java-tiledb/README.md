# cc-sdk-tiledb (Java)

TileDB event store integration for the [USACE Cloud Compute SDK](https://github.com/USACE/cc-go-sdk).

## Install

```groovy
implementation 'mil.army.usace:cc-sdk-tiledb:2.0.0'
```

## Usage

```java
import mil.army.usace.cc.CcSdk;
import mil.army.usace.cc.CcTileDb;
import io.tiledb.java.api.QueryType;

try (CcSdk sdk = new CcSdk()) {
    CcSdk.Payload payload = sdk.getPayload();
    CcTileDb.EventStore store = CcTileDb.openEventStore(payload, "event-store");

    store.openArray("flood-grid", QueryType.TILEDB_READ);
    String uri = store.uri("flood-grid");
}
```
