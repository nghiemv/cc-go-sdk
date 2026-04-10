"""
cc_tiledb — TileDB event store integration for the Cloud Compute SDK.

    pip install cc-sdk-tiledb

    from cc import CcSdk
    from cc_tiledb import open_event_store

    with CcSdk() as sdk:
        payload = sdk.get_payload()
        store = open_event_store(payload, "event-store")
        with store.open("flood-grid") as array:
            data = array[:]
"""

from __future__ import annotations

import tiledb

from cc import Payload, StoreCredentials

__version__ = "2.0.0"


class EventStore:
    """A connected TileDB event store backed by a CC data store."""

    def __init__(self, credentials: StoreCredentials, root: str):
        self._creds = credentials
        self._root = root
        self.ctx = tiledb.Ctx(tiledb.Config(credentials.tiledb_config()))

    def uri(self, array_name: str = "") -> str:
        """Build the TileDB URI for an array under this event store."""
        base = self._creds.s3_uri(self._root, "eventdb")
        if array_name:
            return f"{base}/{array_name}"
        return base

    def open(self, array_name: str, mode: str = "r") -> tiledb.Array:
        """Open a TileDB array by name."""
        return tiledb.open(self.uri(array_name), mode=mode, ctx=self.ctx)

    def array_exists(self, array_name: str) -> bool:
        """Check if a TileDB array exists at the given name."""
        return tiledb.array_exists(self.uri(array_name), ctx=self.ctx)


def open_event_store(payload: Payload, store_name: str) -> EventStore:
    """Open a TileDB event store from a CC payload.

    Args:
        payload: The CC payload (from sdk.get_payload()).
        store_name: Name of the store in the payload.

    Returns:
        An EventStore with credentials and TileDB context pre-configured.
    """
    creds = payload.get_store_credentials(store_name)
    store = payload.get_store(store_name)
    root = store.params.get("root", "") if hasattr(store.params, "get") else store.params["root"]
    return EventStore(creds, root)
