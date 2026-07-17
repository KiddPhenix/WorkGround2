"""Validate production widget skins and exact nine-slice reconstruction."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from PIL import Image


SKINS = ("bp", "instant", "pet", "recorder")
SHELL_SIZE = (2132, 512)
PREVIEW_SIZE = (640, 154)
CAPS = {"left": 576, "top": 112, "right": 800, "bottom": 160}


def validate_skin(root: Path, skin: str) -> None:
    directory = root / skin
    shell = Image.open(directory / "shell.png").convert("RGBA")
    preview = Image.open(directory / "preview.png")
    if shell.size != SHELL_SIZE:
        raise ValueError(f"{skin}: shell size {shell.size}, want {SHELL_SIZE}")
    if preview.size != PREVIEW_SIZE:
        raise ValueError(f"{skin}: preview size {preview.size}, want {PREVIEW_SIZE}")
    corners = [
        shell.getpixel((0, 0))[3],
        shell.getpixel((SHELL_SIZE[0] - 1, 0))[3],
        shell.getpixel((0, SHELL_SIZE[1] - 1))[3],
        shell.getpixel((SHELL_SIZE[0] - 1, SHELL_SIZE[1] - 1))[3],
    ]
    if max(corners) > 16:
        raise ValueError(f"{skin}: opaque shell corners {corners}")

    manifest = json.loads((directory / "shell.9.json").read_text(encoding="utf-8-sig"))
    if manifest.get("sourceSize") != {"width": SHELL_SIZE[0], "height": SHELL_SIZE[1]}:
        raise ValueError(f"{skin}: invalid sourceSize")
    if manifest.get("capInsets") != CAPS:
        raise ValueError(f"{skin}: invalid capInsets")

    rebuilt = Image.new("RGBA", SHELL_SIZE)
    for tile in manifest["tiles"].values():
        image = Image.open(directory / tile["file"]).convert("RGBA")
        expected = (tile["width"], tile["height"])
        if image.size != expected:
            raise ValueError(f"{skin}: {tile['file']} size {image.size}, want {expected}")
        rebuilt.paste(image, (tile["x"], tile["y"]))
    if rebuilt.tobytes() != shell.tobytes():
        raise ValueError(f"{skin}: nine-slice reconstruction differs from shell.png")
    print(f"{skin}: ok shell={shell.size} preview={preview.size} corners={corners}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--assets-root", required=True)
    args = parser.parse_args()
    root = Path(args.assets_root)
    for skin in SKINS:
        validate_skin(root, skin)


if __name__ == "__main__":
    main()
