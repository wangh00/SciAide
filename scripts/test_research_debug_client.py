import importlib.util
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch
from contextlib import redirect_stdout, redirect_stderr
from io import StringIO, BytesIO


spec = importlib.util.spec_from_file_location("research_debug_client", Path(__file__).with_name("research-debug-client.py"))
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)


class DebugClientTests(unittest.TestCase):
    def test_error_keeps_large_structured_facade_code(self):
        payload = {"error": {"code": "WORKFLOW_PAUSE_REQUIRES_CHECKPOINT"}, "result": {"large": "x" * 100000}}
        self.assertEqual(client.http_error_payload(json.dumps(payload).encode(), 400), payload)

    def test_invalid_or_oversized_error_never_echoes_raw_body(self):
        for body in [b'{"error":"PRIVATE', b"PRIVATE" + b"x" * client.MAX_ERROR_BYTES]:
            result = client.http_error_payload(body, 400)
            self.assertNotIn("PRIVATE", json.dumps(result))
            self.assertEqual(result["error"]["status"], 400)

    def test_wait_does_not_timeout_at_real_human_checkpoint(self):
        for state in ["workflow_waiting_human_confirmation", "workflow_paused", "waiting_user_input", "waiting_approval", "plan_available", "revision_available", "chat_cancelled"]:
            with self.subTest(state=state):
                self.assertEqual(client.wait_exit(state), 0)
        for state in ["workflow_running", "planning_running", "discussion"]:
            self.assertIsNone(client.wait_exit(state))
        for state in ["workflow_failed", "chat_failed", "chat_interrupted", "workflow_interrupted", "initialization_pending"]:
            self.assertEqual(client.wait_exit(state), 2)

    def test_compact_retains_pending_confirmation_input(self):
        v = {"case": {}, "state": "workflow_waiting_human_confirmation", "modelCalls": {},
             "workflow": {"run": {}, "steps": [{"id": "step", "nodeId": "candidate_review", "nodeKind": "candidate_selection",
                            "status": "waiting_human_confirmation", "input": {"candidates": [{"id": "actual-record"}]}, "inputSha256": "digest"}]}}
        v["session"] = {"id": "grant-id", "revision": 3, "secondsRemaining": 50}
        result = client.compact(v)
        self.assertEqual(result["session"], v["session"])
        self.assertEqual(result["humanCheckpoints"][0]["input"]["candidates"][0]["id"], "actual-record")
        self.assertEqual(result["humanCheckpoints"][0]["inputSha256"], "digest")

    def test_limits_requires_explicit_confirmation_and_session_before_any_io(self):
        base = ["client", "--root", "unused", "limits", "--max-requests", "105", "--expires-at", "2026-11-01T15:00:00+08:00", "--expected-revision", "1"]
        for extra in [[], ["--confirm"], ["--session-id", "old-session"]]:
            with self.subTest(extra=extra), patch.object(sys, "argv", base+extra), patch.object(client, "build_opener") as opener, redirect_stderr(StringIO()):
                with self.assertRaises(SystemExit):
                    client.main()
                opener.assert_not_called()

    def test_limits_keeps_explicit_session_and_never_retries_conflict(self):
        with tempfile.TemporaryDirectory() as root:
            (Path(root) / "debug-connection.json").write_text(json.dumps({"url": "http://127.0.0.1:12345", "token": "fixture-only", "sessionId": "new-session"}), encoding="utf-8")
            argv = ["client", "--root", root, "limits", "--max-requests", "105", "--expires-at", "2026-11-01T15:00:00+08:00", "--expected-revision", "1", "--session-id", "confirmed-old-session", "--confirm"]
            with patch.object(sys, "argv", argv), patch.object(client, "build_opener") as build, redirect_stdout(StringIO()):
                build.return_value.open.side_effect = client.HTTPError("http://127.0.0.1:12345/v1/limits", 400, "fixture conflict", {}, BytesIO(b'{"error":{"code":"DEBUG_LIMITS_CONFLICT"}}'))
                with self.assertRaises(SystemExit) as result:
                    client.main()
                self.assertEqual(result.exception.code, 2)
                build.return_value.open.assert_called_once()
                request = build.return_value.open.call_args.args[0]
                sent = json.loads(request.data)
                self.assertEqual(sent["sessionId"], "confirmed-old-session")
                self.assertEqual(sent["expectedRevision"], 1)
                self.assertTrue(sent["confirmed"])
                self.assertEqual(request.method, "POST")

    def test_limits_preserves_fractional_cutoff_and_rejects_bad_dates_before_io(self):
        cutoff = "2026-11-01T15:20:11.0526451+08:00"
        self.assertEqual(client.rfc3339_cutoff(cutoff), cutoff)
        long_fraction = "2026-11-01T15:20:11.0526451000+08:00"
        self.assertEqual(client.rfc3339_cutoff(long_fraction), long_fraction)
        for invalid in ["11/01/2026 15:20:11", "2026-11-01T15:20:11", "2026-02-30T15:20:11Z", "2026-11-01T15:20:11+24:00", "2026-11-01T15:20:11+08:60"]:
            argv = ["client", "--root", "unused", "limits", "--max-requests", "105", "--expires-at", invalid, "--expected-revision", "1", "--session-id", "explicit-session", "--confirm"]
            with self.subTest(invalid=invalid), patch.object(sys, "argv", argv), patch.object(client, "build_opener") as opener, redirect_stderr(StringIO()):
                with self.assertRaises(SystemExit):
                    client.main()
                opener.assert_not_called()

    def test_cli_wait_returns_actual_checkpoint_not_completed(self):
        for state, code in [("workflow_waiting_human_confirmation", 0), ("workflow_paused", 0), ("workflow_failed", 2)]:
            with self.subTest(state=state), tempfile.TemporaryDirectory() as root:
                value = {"case": {}, "state": state, "modelCalls": {},
                         "workflow": {"run": {}, "steps": []}}

                class Handler(BaseHTTPRequestHandler):
                    def do_GET(self):
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(json.dumps(value).encode("utf-8"))

                    def log_message(self, *args):
                        pass

                server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    (Path(root) / "debug-connection.json").write_text(json.dumps({"url": f"http://127.0.0.1:{server.server_port}", "token": "fixture-only"}), encoding="utf-8")
                    result = subprocess.run([sys.executable, "-B", str(Path(__file__).with_name("research-debug-client.py")), "--root", root,
                                             "wait", "--case", "fixture", "--timeout", "0.01"], capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode, code, result.stderr)
                    self.assertEqual(json.loads(result.stdout)["state"], state)
                finally:
                    server.shutdown()
                    server.server_close()
                    thread.join(timeout=2)


if __name__ == "__main__":
    unittest.main()
