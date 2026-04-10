# cc-sdk (Python)

Python client for the [USACE Cloud Compute SDK](https://github.com/USACE/cc-go-sdk).

Requires Python 3.10+. The `cc-sdk` binary is bundled in the wheel and discovered
at runtime (or set `CC_SDK_BIN` to override). Zero Python dependencies.

## Install

```bash
pip install cc-sdk
```

## Usage

```python
from cc import CcSdk

with CcSdk() as sdk:
    payload = sdk.get_payload()

    # Typed dataclass access — payload fields are frozen dataclasses
    payload.attributes["catalog_id"]
    payload.inputs[0].paths["watershed"]
    payload.inputs[0].name

    # File operations — datakey is "" when not needed
    sdk.copy_to_local("ds-name", "pathkey", "", "/tmp/local.geojson")
    sdk.copy_to_remote("ds-name", "pathkey", "", "/tmp/output.dss")

    # Bytes in/out
    data = sdk.get("ds-name", "pathkey", "")
    sdk.put("ds-name", "pathkey", "", data)
```

The SDK spawns `cc-sdk serve` once per `CcSdk` instance and reuses the process
for every call — S3/FSB connections are cached across operations.
