#!/usr/bin/env python3
"""
compress_session.py
Helper script to execute context compression for a given session ID in Hermes state.db.
Outputs clean JSON to stdout for consumption by hermes-tele bot.
"""

import sys
import json
import argparse
from pathlib import Path

# Add hermes-agent to python path
sys.path.insert(0, "/usr/local/lib/hermes-agent")

def main():
    parser = argparse.ArgumentParser(description="Compress Hermes session context")
    parser.add_argument("--session-id", required=True, help="Session ID to compress")
    parser.add_argument("--db-path", default="/root/.hermes/state.db", help="Path to state.db")
    parser.add_argument("compress_args", nargs="*", help="Optional compression arguments")
    args = parser.parse_args()

    session_id = args.session_id.strip()
    raw_args = " ".join(args.compress_args).strip()

    try:
        from hermes_state import SessionDB
        from run_agent import AIAgent
        from agent.conversation_compression_manual import (
            MIN_MESSAGES, compress_now, parse_compress_args, render_compress_result
        )

        db = SessionDB(Path(args.db_path))
        sess = db.get_session(session_id)
        if not sess:
            print(json.dumps({
                "success": False,
                "status": "error",
                "message": f"Sesi `{session_id}` tidak ditemukan di database."
            }))
            sys.exit(0)

        messages = db.get_messages(session_id)
        msg_count = len(messages)
        if msg_count < MIN_MESSAGES:
            print(json.dumps({
                "success": False,
                "status": "not_enough_messages",
                "message": f"Jumlah pesan ({msg_count}) belum cukup untuk dikompres (minimal {MIN_MESSAGES} pesan)."
            }))
            sys.exit(0)

        # Model for compression
        model = sess.get("model") or "Top"
        req = parse_compress_args(raw_args)

        agent = AIAgent(
            model=model,
            quiet_mode=True,
            skip_memory=True,
            enabled_toolsets=["memory"],
            session_id=session_id,
            session_db=db
        )
        if getattr(agent, "context_compressor", None):
            agent.context_compressor.summary_model = "geminiflash"

        import io
        import contextlib
        stdout_buf = io.StringIO()
        with contextlib.redirect_stdout(stdout_buf):
            result = compress_now(agent, messages, req, task_id=session_id)
            lines = render_compress_result(result)
            new_sid = getattr(agent, "session_id", session_id)

        print(json.dumps({
            "success": True,
            "status": result.status,
            "original_session_id": session_id,
            "new_session_id": new_sid,
            "before_messages": len(result.before_messages),
            "after_messages": len(result.after_messages),
            "before_tokens": result.before_tokens,
            "after_tokens": result.after_tokens,
            "removed_messages": result.removed,
            "lines": lines
        }))

    except Exception as e:
        print(json.dumps({
            "success": False,
            "status": "error",
            "message": str(e)
        }))
        sys.exit(0)

if __name__ == "__main__":
    main()
