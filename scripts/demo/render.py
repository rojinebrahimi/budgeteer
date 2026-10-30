"""Render the real budgeteer binary's output as an animated terminal GIF.

Usage: render.py BINARY LOGS_HOME OUT.gif [--linkedin]

--linkedin makes a square 1080x1080 cut for social feeds: bigger type, a title
card, only the `suggest` run, and a closing call-to-action card.
Needs Pillow and macOS's Menlo font (override with $DEMO_FONT / $DEMO_FONT_BOLD).
"""
import os, re, subprocess, sys
from PIL import Image, ImageDraw, ImageFont

LI = "--linkedin" in sys.argv
binary, home, out = [a for a in sys.argv[1:] if not a.startswith("--")][:3]
COLS, ROWS, FS, PAD, BAR = (0, 0, 17, 26, 38) if LI else (104, 30, 15, 18, 34)
BG, FG, DIM = (30, 30, 36), (212, 212, 218), (122, 122, 135)
COL = {"31": (255, 107, 107), "32": (90, 220, 140), "36": (100, 200, 235)}
font = ImageFont.truetype(os.environ.get("DEMO_FONT", "/System/Library/Fonts/Menlo.ttc"), FS, index=0)
bold = ImageFont.truetype(os.environ.get("DEMO_FONT_BOLD", "/System/Library/Fonts/Menlo.ttc"), FS, index=1)
CW = round(font.getlength("M")); LH = FS + 6
if LI:
    W = H = 1080
    COLS, ROWS = (W - 2 * PAD) // CW, (H - 2 * PAD - BAR) // LH
else:
    W, H = COLS * CW + 2 * PAD, ROWS * LH + 2 * PAD + BAR
TITLE_FONT = "/System/Library/Fonts/Helvetica.ttc"

def run(args):
    env = {**os.environ, "FORCE_COLOR": "1", "COLUMNS": str(min(COLS - 2, 100))}
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
        buf[-1].append((ch, (FG, False))); emit(buf, 22)
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
        if i % 3 == 2 or i == len(lines) - 1: emit(buf[:-1], 70, cursor=False)
        plain = SGR.sub("", ln)
        if plain.lstrip().startswith("do "): seen_do = True
        if pause_after_blocks and plain == "" and seen_do:
            emit(buf[:-1], 2300, cursor=False); seen_do = False
    buf[-1] = list(PROMPT)
    emit(buf, 2800)
    return buf

def card(lines, ms):
    """Full-frame title card. lines = [(text, size, colour, bold)]."""
    im = Image.new("RGB", (W, H), BG)
    d = ImageDraw.Draw(im)
    heights = []
    fonts = []
    for text, size, colour, b in lines:
        try:
            f = ImageFont.truetype(TITLE_FONT, size, index=1 if b else 0)
        except OSError:
            f = ImageFont.truetype("/System/Library/Fonts/Menlo.ttc", size, index=1 if b else 0)
        fonts.append(f); heights.append(int(size * 1.55))
    y = (H - sum(heights)) // 2
    for (text, size, colour, b), f, h in zip(lines, fonts, heights):
        d.text((W // 2, y + h // 2), text, fill=colour, font=f, anchor="mm")
        y += h
    frames.append(im); durs.append(ms)

if LI:
    card([("Where did my", 76, FG, True), ("AI coding budget go?", 76, COL["36"], True),
          ("", 30, FG, False), ("I built a tiny Go CLI that reads my", 32, DIM, False),
          ("Claude Code + Codex logs and tells me.", 32, DIM, False)], 2600)
    scene(["suggest", "--top", "3"], True)
    card([("budgeteer", 84, COL["36"], True), ("", 24, FG, False),
          ("The best moves to cut your", 38, FG, False), ("Claude Code + Codex spend.", 38, FG, False),
          ("", 24, FG, False), ("Local. Open source. Zero dependencies.", 28, DIM, False),
          ("", 24, FG, False), ("github.com/rojinebrahimi/budgeteer", 32, COL["32"], True)], 4200)
else:
    scene(["summary", "--since", "7d"], False)
    scene(["suggest", "--top", "3"], True)

# One shared palette keeps the GIF small and avoids colour flicker. Build it
# from a montage of many frames: a single frame can lack accent colours
# (cyan bars, red flags) and quantisation would silently turn them grey.
sample = frames[:: max(1, len(frames) // 12)]
montage = Image.new("RGB", (W, H * len(sample)))
for i, f in enumerate(sample):
    montage.paste(f, (0, i * H))
pal = montage.quantize(colors=56, method=Image.MEDIANCUT)
q = [f.quantize(palette=pal, dither=Image.Dither.NONE) for f in frames]
q[0].save(out, save_all=True, append_images=q[1:], duration=durs, loop=0, optimize=True, disposal=1)
print(f"{out}: {len(q)} frames, {os.path.getsize(out)/1e6:.2f} MB, {sum(durs)/1000:.0f}s")
