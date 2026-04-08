#!/usr/bin/env python3
"""Build a platform-specific wheel for cc-sdk.

Copies a pre-built cc-sdk binary into the package directory, builds a
wheel with the correct platform tag, then cleans up.

Usage:
    python build_wheel.py <binary_path> <goos> <goarch>

Example:
    python build_wheel.py ./cc-sdk linux amd64
"""

import os
import shutil
import subprocess
import sys

PLATFORM_TAGS = {
    ("linux", "amd64"):   "manylinux_2_17_x86_64.manylinux2014_x86_64",
    ("linux", "arm64"):   "manylinux_2_17_aarch64.manylinux2014_aarch64",
    ("darwin", "amd64"):  "macosx_11_0_x86_64",
    ("darwin", "arm64"):  "macosx_11_0_arm64",
    ("windows", "amd64"): "win_amd64",
}


def main():
    if len(sys.argv) != 4:
        print(f"Usage: {sys.argv[0]} <binary_path> <goos> <goarch>")
        sys.exit(1)

    binary_path, goos, goarch = sys.argv[1], sys.argv[2], sys.argv[3]
    platform_tag = PLATFORM_TAGS.get((goos, goarch))
    if not platform_tag:
        print(f"Unsupported platform: {goos}/{goarch}")
        sys.exit(1)

    script_dir = os.path.dirname(os.path.abspath(__file__))
    dest_name = "cc-sdk.exe" if goos == "windows" else "cc-sdk"
    dest = os.path.join(script_dir, dest_name)

    # Copy binary into package directory
    shutil.copy2(binary_path, dest)
    os.chmod(dest, 0o755)

    try:
        # Build wheel
        subprocess.run(
            [sys.executable, "-m", "pip", "wheel", "--no-deps", "--wheel-dir", "dist/", "."],
            cwd=script_dir,
            check=True,
        )

        # Retag wheel with platform tag
        dist_dir = os.path.join(script_dir, "dist")
        for whl in os.listdir(dist_dir):
            if whl.endswith(".whl") and "any" in whl:
                new_name = whl.replace("py3-none-any", f"py3-none-{platform_tag}")
                os.rename(
                    os.path.join(dist_dir, whl),
                    os.path.join(dist_dir, new_name),
                )
                print(f"Built: {new_name}")
    finally:
        # Clean up binary from source tree
        if os.path.exists(dest):
            os.remove(dest)


if __name__ == "__main__":
    main()
