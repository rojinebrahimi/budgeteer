"""Render the real budgeteer binary's output as an animated terminal GIF.

Usage: render.py BINARY LOGS_HOME OUT.gif
Needs Pillow and macOS's Menlo font (override with $DEMO_FONT / $DEMO_FONT_BOLD).
"""
import os, re, subprocess, sys
from PIL import Image, ImageDraw, ImageFont

binary, home, out = sys.argv[1:4]
COLS, ROWS, FS, PAD, BAR = 104, 30, 15, 18, 34
BG, FG, DIM = (30, 30, 36), (212, 212, 218), (122, 122, 135)
COL = {"31": (255, 107, 107), "32": (90, 220, 140), "36": (100, 200, 235)}
font = ImageFont.truetype(os.environ.get("DEMO_FONT", "/System/Library/Fonts/Menlo.ttc"), FS, index=0)
bold = ImageFont.truetype(os.environ.get("DEMO_FONT_BOLD", "/System/Library/Fonts/Menlo.ttc"), FS, index=1)
CW = round(font.getlength("M")); LH = FS + 6
W, H = COLS * CW + 2 * PAD, ROWS * LH + 2 * PAD + BAR

def run(args):
    env = {**os.environ, "FORCE_COLOR": "1", "COLUMNS": str(COLS - 2)}
    env.pop("NO_COLOR", None)
    return subprocess.run([binary, *args, "--claude-dir", f"{home}/.claude", "--codex-dir", f"{home}/.codex"],
                          capture_output=True, text=True, env=env, check=True).stdout

SGR = re.compile(r"\x1b\[([0-9;]*)m")
def parse(line):
    """ANSI line -> [(char, style)] with style = (color, bold)."""
    cells, color, b = [], FG, False
    pos = 0
    for m in SGR.finditer(line):
        cells += [(c, (color, b)) for c in line[pos:m.start()]]
        pos = m.end()
        code = m.group(1)
        if code in ("0", ""): color, b = FG, False
        elif code == "1": b = True
        elif code == "2": color = DIM
        elif code in COL: color = COL[code]
    cells += [(c, (color, b)) for c in line[pos:]]
    return cells

def wrap(cells):
    return [cells[i:i + COLS] for i in range(0, max(len(cells), 1), COLS)] or [[]]

PROMPT = [(c, (COL["32"], True)) for c in "~/app "] + [("$", (FG, True)), (" ", (FG, False))]

def draw(buf, cursor=True):
    im = Image.new("RGB", (W, H), BG)
    d = ImageDraw.Draw(im)
    d.rectangle([0, 0, W, BAR], fill=(44, 44, 54))
    for i, c in enumerate([(255, 95, 86), (255, 189, 46), (39, 201, 63)]):
        d.ellipse([16 + i * 22, 11, 28 + i * 22, 23], fill=c)
    d.text((W // 2, BAR // 2), "budgeteer — demo (synthetic data)", fill=DIM, font=font, anchor="mm")
    view = buf[-ROWS:]
    for r, cells in enumerate(view):
        x, y = PAD, BAR + PAD + r * LH
        for c, (col, b) in cells:
            d.text((x, y), c, fill=col, font=bold if b else font)
            x += CW
        if cursor and r == len(view) - 1:
            d.rectangle([x, y + 1, x + CW - 2, y + LH - 3], fill=FG)
    return im

frames, durs = [], []
def emit(buf, ms, cursor=True):
    frames.append(draw(buf, cursor)); durs.append(ms)

def scene(cmd_args, pause_after_blocks):
    buf = [list(PROMPT)]
    emit(buf, 500)
    text = "budgeteer " + " ".join(cmd_args)
    for ch in text:
        buf[-1].append((ch, (FG, False))); emit(buf, 26)
    emit(buf, 350)
    lines = run(cmd_args).rstrip("\n").split("\n")
    buf.append([])
    seen_do = False
    for i, ln in enumerate(lines):
        cells = parse(ln)
        rows = wrap(cells)
        buf[-1:] = [rows[0]]
        for extra in rows[1:]: buf.append(extra)
        buf.append([])
        if i % 2 == 1 or i == len(lines) - 1: emit(buf[:-1], 60, cursor=False)
        plain = SGR.sub("", ln)
        if plain.lstrip().startswith("do:"): seen_do = True
        if pause_after_blocks and plain == "" and seen_do:
            emit(buf[:-1], 2300, cursor=False); seen_do = False
    buf[-1] = list(PROMPT)
    emit(buf, 2800)
    return buf

scene(["summary", "--since", "7d"], False)
scene(["suggest", "--top", "3"], True)

# One shared palette keeps the GIF small and avoids per-frame colour flicker.
pal = frames[len(frames) // 2].quantize(colors=48, method=Image.MEDIANCUT)
q = [f.quantize(palette=pal, dither=Image.Dither.NONE) for f in frames]
q[0].save(out, save_all=True, append_images=q[1:], duration=durs, loop=0, optimize=True, disposal=1)
print(f"{out}: {len(q)} frames, {os.path.getsize(out)/1e6:.2f} MB, {sum(durs)/1000:.0f}s")
