# cc-sdk-tiledb (Python)

TileDB event store integration for the [USACE Cloud Compute SDK](https://github.com/USACE/cc-go-sdk).

## Install

```bash
pip install cc-sdk-tiledb
```

## Usage

```python
from cc import CcSdk
from cc_tiledb import open_event_store

with CcSdk() as sdk:
    payload = sdk.get_payload()
    store = open_event_store(payload, "event-store")

    with store.open("flood-grid") as array:
        data = array[:]

    with store.open("flood-grid", "w") as array:
        array[:] = my_data

    store.array_exists("flood-grid")
    uri = store.uri("flood-grid")
```
