"""Pack PNGs into an .ico.

The favicon is three of the generated PNGs in a container, and nothing here
built it: the logo section claimed everything in assets/ was generated from the
artwork, and this one file was not. Regenerating the set left it showing the
previous drawing, which is the kind of thing nobody notices until a browser
tab does.

An .ico is a six-byte header, a sixteen-byte directory entry per image, and
then the image data. Modern readers accept PNG data verbatim, which is what the
existing file already used, so there is nothing to re-encode.

Standard library only, like unmatte.py beside it.
"""

import pathlib
import struct
import sys


def size_of(png):
    """Width and height from the IHDR, which is always the first chunk."""
    data = pathlib.Path(png).read_bytes()
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError(f"{png} is not a PNG")
    width, height = struct.unpack(">II", data[16:24])
    return width, height, data


def build(out_ico, pngs):
    images = [size_of(p) for p in pngs]
    for width, height, _ in images:
        if width > 256 or height > 256:
            raise ValueError(f"{width}x{height} does not fit an .ico directory entry")

    # Header, then one directory entry each, then the data after all of them.
    offset = 6 + 16 * len(images)
    header = struct.pack("<HHH", 0, 1, len(images))
    entries, blobs = bytearray(), bytearray()
    for width, height, data in images:
        entries += struct.pack(
            "<BBBBHHII",
            width % 256,   # 0 means 256, which is why this is modulo
            height % 256,
            0,             # palette size, 0 for a true-colour image
            0,             # reserved
            1,             # colour planes
            32,            # bits per pixel
            len(data),
            offset,
        )
        blobs += data
        offset += len(data)

    pathlib.Path(out_ico).write_bytes(header + bytes(entries) + bytes(blobs))


if __name__ == "__main__":
    build(sys.argv[1], sys.argv[2:])
    print(sys.argv[1])
