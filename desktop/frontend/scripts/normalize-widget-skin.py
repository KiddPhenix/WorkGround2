"""Normalize a transparent widget shell into the shared nine-slice canvas."""

from __future__ import annotations

import argparse
from pathlib import Path

from PIL import Image


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--width", type=int, default=2132)
    parser.add_argument("--height", type=int, default=512)
    parser.add_argument("--alpha-threshold", type=int, default=8)
    parser.add_argument("--allow-opaque-corners", action="store_true")
    args = parser.parse_args()

    source = Image.open(args.input).convert("RGBA")
    alpha = source.getchannel("A").point(lambda value: 255 if value > args.alpha_threshold else 0)
    bounds = alpha.getbbox()
    if bounds is None:
        raise SystemExit("input contains no visible pixels")

    shell = source.crop(bounds).resize((args.width, args.height), Image.Resampling.LANCZOS)
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    shell.save(output, optimize=True)

    corner_alpha = [
        shell.getpixel((0, 0))[3],
        shell.getpixel((args.width - 1, 0))[3],
        shell.getpixel((0, args.height - 1))[3],
        shell.getpixel((args.width - 1, args.height - 1))[3],
    ]
    if not args.allow_opaque_corners and max(corner_alpha) > 16:
        raise SystemExit(f"normalized shell corners are not transparent: {corner_alpha}")

    print(f"Wrote {output} from bounds={bounds} corners={corner_alpha}")


if __name__ == "__main__":
    main()
