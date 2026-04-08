# cc-sdk (Python)

Python client for the [USACE Cloud Compute SDK](https://github.com/USACE/cc-go-sdk).

Requires the `cc-sdk` binary on PATH. Zero Python dependencies.

## Install

```bash
pip install cc-sdk
```

## Usage

```python
from cc import CcSdk

with CcSdk() as sdk:
    payload = sdk.get_payload()

    # Dot-access on all payload fields
    payload.attributes["catalog_id"]
    payload.inputs[0].paths["watershed"]
    payload.inputs[0].name

    # File operations
    sdk.copy_to_local("ds-name", "pathkey", "/tmp/local.geojson")
    sdk.copy_to_remote("ds-name", "pathkey", "/tmp/output.dss")
```

## One-shot mode

For simple scripts with few I/O calls:

```python
from cc import get_payload, copy_to_local, copy_to_remote

payload = get_payload()
copy_to_local("ds-name", "pathkey", "/tmp/file.txt")
```
