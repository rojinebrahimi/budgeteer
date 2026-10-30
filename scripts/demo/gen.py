"""Generate anonymised synthetic Claude Code + Codex logs for the README demo.

Usage: gen.py OUTPUT_DIR   (creates OUTPUT_DIR/.claude and OUTPUT_DIR/.codex)
"""
import json, os, random, shutil, sys, uuid
from datetime import datetime, timedelta, timezone
root = sys.argv[1]
shutil.rmtree(root, ignore_errors=True)
rng = random.Random(42)
J = lambda o: json.dumps(o, separators=(",", ":"))
now = datetime.now(timezone.utc)
iso = lambda t: t.strftime("%Y-%m-%dT%H:%M:%S.000Z")
n = [0]
def cl_session(project, model, start, turns, ctx0, ctx_step, out_rng, think, effort="high", sub=False, gap_at=None):
    sid = str(uuid.UUID(int=rng.getrandbits(128)))
    d = f"{root}/.claude/projects/{project.strip('/').replace('/','-')}"
    os.makedirs(d, exist_ok=True)
    t, ctx, lines = start, ctx0, []
    for i in range(turns):
        gap = timedelta(seconds=rng.randint(20, 80))
        wr = rng.randint(500, 3000)
        cr = ctx
        if gap_at is not None and i == gap_at:
            gap = timedelta(hours=2); cr, wr = 0, ctx
        t += gap; ctx += ctx_step + rng.randint(0, ctx_step)
        out = rng.randint(*out_rng); n[0] += 1
        usage = {"input_tokens": 3, "cache_creation_input_tokens": wr, "cache_read_input_tokens": cr,
                 "output_tokens": out, "output_tokens_details": {"thinking_tokens": int(out * think)},
                 "cache_creation": {"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": wr}}
        lines.append(J({"type": "assistant", "timestamp": iso(t), "sessionId": sid,
            "requestId": f"req_{n[0]}", "uuid": f"u{n[0]}", "cwd": project, "isSidechain": sub, "effort": effort,
            "message": {"id": f"msg_{n[0]}", "model": model, "usage": usage}}))
    open(f"{d}/{sid}.jsonl", "w").write("\n".join(lines) + "\n")
def cx_session(project, model, start, turns, ctx0, ctx_step, out_rng, effort="high"):
    sid = str(uuid.UUID(int=rng.getrandbits(128)))
    d = f"{root}/.codex/sessions/{start:%Y/%m/%d}"; os.makedirs(d, exist_ok=True)
    t, ctx, tot = start, ctx0, 0
    L = [J({"timestamp": iso(t), "type": "session_meta", "payload": {"id": sid, "cwd": project}}),
         J({"timestamp": iso(t), "type": "turn_context", "payload": {"model": model, "effort": effort}})]
    for i in range(turns):
        t += timedelta(seconds=rng.randint(20, 90)); ctx += ctx_step + rng.randint(0, ctx_step)
        out = rng.randint(*out_rng); cached = int(ctx * 0.9); tot += ctx
        last = {"input_tokens": ctx, "cached_input_tokens": cached, "output_tokens": out, "reasoning_output_tokens": out // 2, "total_tokens": ctx + out}
        L.append(J({"timestamp": iso(t), "type": "event_msg", "payload": {"type": "token_count",
            "info": {"total_token_usage": {**last, "input_tokens": tot}, "last_token_usage": last}}}))
    open(f"{d}/rollout-{sid}.jsonl", "w").write("\n".join(L) + "\n")
def at(days_ago, hour): return (now - timedelta(days=days_ago)).replace(hour=hour % 24, minute=rng.randint(0, 59))
P = ["/home/dev/web-app", "/home/dev/api-server", "/home/dev/mobile-app"]
O, SN = "claude-opus-4-5", "claude-sonnet-4-5"
# ---- this week
for d, h in [(5, 9), (3, 10), (1, 13)]:                         # heavy marathon Opus sessions
    cl_session(P[0], O, at(d, h), 260, 30_000, 1_200, (700, 3200), 0.55, gap_at=130)
for i in range(9):                                              # light chores on Opus
    cl_session(P[1 + i % 2], O, at(rng.uniform(0.2, 6.5), 9 + i), rng.randint(20, 45), 20_000, 400, (60, 260), 0.1)
for i in range(7):                                              # light chores on Sonnet
    cl_session(P[i % 3], SN, at(rng.uniform(0.2, 6.5), 10 + i), rng.randint(25, 70), 18_000, 350, (60, 280), 0.1)
for i in range(6):                                              # subagent exploration on Opus
    cl_session(P[0], O, at(rng.uniform(0.2, 6.5), 11 + i), rng.randint(15, 40), 25_000, 600, (80, 300), 0.05, sub=True)
for d, h in [(4, 15), (2, 16)]:                                 # codex marathons + chores
    cx_session(P[1], "gpt-5", at(d, h), 220, 25_000, 1_000, (500, 2500))
for i in range(4):
    cx_session(P[2], "gpt-5", at(rng.uniform(0.3, 6.5), 9 + i), rng.randint(30, 60), 15_000, 400, (60, 220))
# ---- previous week (smaller, so the trend arrow shows a rise)
for d, h in [(10, 10), (8.5, 11), (12, 14)]:
    cl_session(P[0], O, at(d, h), 240, 30_000, 1_000, (700, 2800), 0.55)
for i in range(6):
    cl_session(P[i % 3], SN, at(rng.uniform(7.3, 13.5), 10 + i), rng.randint(25, 60), 18_000, 350, (60, 280), 0.1)
print("ok", n[0], "claude calls")
