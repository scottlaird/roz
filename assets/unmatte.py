"""Recover alpha from two opaque renders of the same SVG.

qlmanage is the only rasteriser on this machine and it composites onto white,
so every exported PNG arrives with its transparent regions filled in. Render
the same artwork over white and over black and the alpha falls out:

    over white:  Cw = C*a + (1-a)
    over black:  Cb = C*a
    subtracting: a  = 1 - (Cw - Cb),  and then C = Cb / a

Only the standard library: zlib for the image data, struct for the chunks.
"""

import pathlib
import struct
import subprocess
import sys
import tempfile
import zlib


def read_png(path):
    """Return (width, height, list of RGBA rows as bytearrays)."""
    data = pathlib.Path(path).read_bytes()
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError(f"{path} is not a PNG")

    pos, idat, width = 8, bytearray(), None
    while pos < len(data):
        (length,) = struct.unpack(">I", data[pos : pos + 4])
        kind = data[pos + 4 : pos + 8]
        body = data[pos + 8 : pos + 8 + length]
        if kind == b"IHDR":
            width, height, depth, colour = struct.unpack(">IIBB", body[:10])
            if depth != 8 or colour not in (2, 6):
                raise ValueError(f"{path}: want 8-bit RGB or RGBA, got depth {depth} colour {colour}")
        elif kind == b"IDAT":
            idat += body
        elif kind == b"IEND":
            break
        pos += 12 + length

    channels = 3 if colour == 2 else 4
    raw = zlib.decompress(bytes(idat))
    stride = width * channels
    rows, prev, pos = [], bytearray(stride), 0

    for _ in range(height):
        filt = raw[pos]
        line = bytearray(raw[pos + 1 : pos + 1 + stride])
        pos += 1 + stride
        for i in range(stride):
            a = line[i - channels] if i >= channels else 0
            b = prev[i]
            c = prev[i - channels] if i >= channels else 0
            if filt == 1:
                line[i] = (line[i] + a) & 0xFF
            elif filt == 2:
                line[i] = (line[i] + b) & 0xFF
            elif filt == 3:
                line[i] = (line[i] + (a + b) // 2) & 0xFF
            elif filt == 4:
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                pred = a if (pa <= pb and pa <= pc) else (b if pb <= pc else c)
                line[i] = (line[i] + pred) & 0xFF
        prev = line
        if channels == 3:  # normalise to RGBA
            rgba = bytearray(width * 4)
            for x in range(width):
                rgba[x * 4 : x * 4 + 3] = line[x * 3 : x * 3 + 3]
                rgba[x * 4 + 3] = 255
            rows.append(rgba)
        else:
            rows.append(line)
    return width, height, rows


def write_png(path, width, height, rows):
    raw = bytearray()
    for row in rows:
        raw.append(0)  # filter: none
        raw += row

    def chunk(kind, body):
        return struct.pack(">I", len(body)) + kind + body + struct.pack(">I", zlib.crc32(kind + body))

    out = b"\x89PNG\r\n\x1a\n"
    out += chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0))
    out += chunk(b"IDAT", zlib.compress(bytes(raw), 9))
    out += chunk(b"IEND", b"")
    pathlib.Path(path).write_bytes(out)


def unmatte(on_white, on_black, out_path):
    w1, h1, white = read_png(on_white)
    w2, h2, black = read_png(on_black)
    if (w1, h1) != (w2, h2):
        raise ValueError("the two renders differ in size")

    rows = []
    for rw, rb in zip(white, black):
        row = bytearray(w1 * 4)
        for x in range(w1):
            i = x * 4
            # Alpha per channel is the same in theory; average for rounding noise.
            alpha = 0
            for c in range(3):
                alpha += 255 - (rw[i + c] - rb[i + c])
            alpha = max(0, min(255, alpha // 3))
            if alpha == 0:
                row[i : i + 4] = b"\x00\x00\x00\x00"
                continue
            for c in range(3):
                row[i + c] = max(0, min(255, rb[i + c] * 255 // alpha))
            row[i + 3] = alpha
        rows.append(row)
    write_png(out_path, w1, h1, rows)


def render(svg_path, size, background, out_png):
    """Rasterise svg_path at size with an opaque background rectangle."""
    svg = pathlib.Path(svg_path).read_text()
    head, rest = svg.split(">", 1)
    backed = f'{head}><rect x="-2000" y="-2000" width="8000" height="8000" fill="{background}"/>{rest}'
    with tempfile.TemporaryDirectory() as tmp:
        staged = pathlib.Path(tmp, "staged.svg")
        staged.write_text(backed)
        subprocess.run(
            ["qlmanage", "-t", "-s", str(size), "-o", tmp, str(staged)],
            capture_output=True,
            check=True,
        )
        produced = pathlib.Path(tmp, "staged.svg.png")
        if not produced.exists():
            raise RuntimeError(f"qlmanage produced nothing for {svg_path} at {size}")
        pathlib.Path(out_png).write_bytes(produced.read_bytes())


def export(svg_path, size, out_png):
    with tempfile.TemporaryDirectory() as tmp:
        w, b = pathlib.Path(tmp, "w.png"), pathlib.Path(tmp, "b.png")
        render(svg_path, size, "#FFFFFF", w)
        render(svg_path, size, "#000000", b)
        unmatte(w, b, out_png)


if __name__ == "__main__":
    export(sys.argv[1], int(sys.argv[2]), sys.argv[3])
    print(sys.argv[3])
