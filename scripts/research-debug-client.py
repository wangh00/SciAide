"""Local developer client. Never prints the connection token or retries POSTs."""
import argparse
import json
import re
import sys
import time
from datetime import datetime
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import quote, urlparse
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


MAX_ERROR_BYTES = 2 * 1024 * 1024


def rfc3339_cutoff(value):
    # Validate before reading connection state or sending a non-retried POST.
    # Preserve the original nanosecond string; datetime is only a validity check.
    try:
        if not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})", value, flags=re.ASCII):
            raise ValueError()
        datetime.fromisoformat(value.replace("Z", "+00:00"))
        if not value.endswith("Z") and (int(value[-5:-3]) > 23 or int(value[-2:]) > 59):
            raise ValueError()
    except ValueError:
        raise argparse.ArgumentTypeError("expires-at must be an RFC3339 timestamp with a timezone") from None
    return value


def http_error_payload(body, status):
    # A failed Facade action can include a full, large RunDetail. Do not cut it
    # at 64 KiB then mistake the partial JSON for the provider's error message.
    if len(body) > MAX_ERROR_BYTES:
        return {"error": {"code": "DEBUG_ERROR_RESPONSE_TOO_LARGE", "status": status, "truncated": True}}
    try:
        return json.loads(body)
    except (ValueError, UnicodeDecodeError):
        # Never echo malformed/truncated raw bodies (possibly sensitive data).
        return {"error": {"code": "DEBUG_HTTP_ERROR", "status": status, "bytes": len(body)}}


def compact(value):
    if not isinstance(value, dict) or "workflow" not in value:
        return value
    workflow = value["workflow"]
    run = workflow["run"]
    chats = []
    for chat in value.get("chats", []):
        r = chat["run"]
        chats.append({"id": r["id"], "status": r["status"], "errorCode": r.get("errorCode"),
                      "errorMessage": r.get("errorMessage"), "userInput": chat.get("userInput"),
                      "pendingApprovals": chat.get("pendingApprovals", [])})
    return {"case": value["case"], "activeConversationId": value.get("activeConversationId"), "state": value["state"], "workflowState": value.get("workflowState"), "discussionState": value.get("discussionState"), "modelCalls": value["modelCalls"],
            "session": value.get("session"),
            "workflow": {k: run.get(k) for k in ["id", "status", "workflowPurpose", "currentStep", "errorCode", "errorMessage"]},
            "steps": [{k: s.get(k) for k in ["id", "nodeId", "status", "attempt", "errorCode", "errorMessage"]} for s in workflow.get("steps", [])],
            "humanCheckpoints": [{k: s.get(k) for k in ["id", "nodeId", "nodeKind", "input", "inputSha256"]}
                                 for s in workflow.get("steps", []) if s.get("status") == "waiting_human_confirmation"],
            "pendingApprovals": workflow.get("pendingApprovals", []), "revisionProposals": workflow.get("revisionProposals", []), "chats": chats, "plan": value.get("plan")}


def wait_exit(state):
    if state in ["chat_failed", "chat_interrupted", "workflow_failed", "initialization_pending", "workflow_interrupted"]:
        return 2
    if state in ["workflow_completed", "workflow_cancelled", "chat_cancelled", "workflow_waiting_human_confirmation",
                 "workflow_paused", "waiting_user_input", "waiting_approval", "plan_available", "revision_available", "discussion_idle"]:
        return 0
    return None


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--root", required=True, type=Path)
    sub = p.add_subparsers(dest="command", required=True)
    for name in ["status", "models", "cases", "stop"]:
        sub.add_parser(name)
    limits = sub.add_parser("limits", help="apply an explicitly authorized grant without restarting or resetting Used")
    limits.add_argument("--max-requests", required=True, type=int)
    limits.add_argument("--expires-at", required=True, type=rfc3339_cutoff)
    limits.add_argument("--expected-revision", required=True, type=int)
    limits.add_argument("--session-id", required=True, help="explicit session.id from the confirmed authorization; never inferred from a new descriptor")
    limits.add_argument("--confirm", required=True, action="store_true")
    new = sub.add_parser("new")
    new.add_argument("--name", default="developer case")
    new.add_argument("--prompt-file", required=True, type=Path)
    new.add_argument("--profile", required=True)
    new.add_argument("--model", required=True)
    new.add_argument("--request-id", required=True, help="save this stable ID before calling; reuse it with identical inputs to recover pending initialization")
    # Let the debug server choose its research-mode default: full_access only
    # when the isolated server was explicitly started with --allow-full-access,
    # otherwise the server keeps the conservative Plan mode.
    new.add_argument("--permission-mode", choices=["plan", "full_access"], default=None)
    for name in ["snapshot", "wait"]:
        cmd = sub.add_parser(name)
        cmd.add_argument("--case", required=True)
        cmd.add_argument("--full", action="store_true")
        if name == "wait":
            cmd.add_argument("--timeout", type=float, default=60)
    action = sub.add_parser("action")
    action.add_argument("--case", required=True)
    action.add_argument("--name", required=True, choices=["message", "answer", "adopt", "start-route", "retry", "confirm-revision", "pause", "resume", "cancel", "cancel-chat", "decide", "approve", "approve-workflow", "clarify", "replan"])
    action.add_argument("--json-file", required=True, type=Path)
    args = p.parse_args()
    descriptor = json.loads((args.root / "debug-connection.json").read_text(encoding="utf-8"))
    url = urlparse(descriptor["url"])
    if url.scheme != "http" or url.hostname != "127.0.0.1" or not url.port or url.username or url.password or url.query or url.fragment or url.path not in ["", "/"]:
        raise ValueError("connection descriptor must address the local loopback server")
    opener = build_opener(ProxyHandler({}), NoRedirect())

    def call(method, path, body=None):
        data = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
        request = Request(descriptor["url"] + path, data=data, method=method,
                          headers={"Authorization": "Bearer " + descriptor["token"], "Content-Type": "application/json"})
        try:
            with opener.open(request, timeout=65) as response:
                return json.load(response)
        except HTTPError as error:
            payload = http_error_payload(error.read(MAX_ERROR_BYTES + 1), error.code)
            print(json.dumps(payload, ensure_ascii=True, indent=2))
            raise SystemExit(2)

    code = 0
    if args.command in ["status", "models", "cases"]:
        value = call("GET", "/v1/" + args.command)
    elif args.command == "stop":
        value = call("POST", "/v1/shutdown", {})
    elif args.command == "limits":
        value = call("POST", "/v1/limits", {"maxRequests": args.max_requests, "expiresAt": args.expires_at,
                     "sessionId": args.session_id, "expectedRevision": args.expected_revision, "confirmed": args.confirm})
    elif args.command == "new":
        body = {"name": args.name, "prompt": args.prompt_file.read_text(encoding="utf-8-sig"),
                "modelProfileId": args.profile, "modelId": args.model, "requestId": args.request_id}
        if args.permission_mode is not None:
            body["permissionMode"] = args.permission_mode
        value = call("POST", "/v1/cases", body)
    elif args.command == "action":
        value = call("POST", "/v1/cases/" + quote(args.case, safe="") + "/actions/" + args.name,
                     json.loads(args.json_file.read_text(encoding="utf-8-sig")))
    else:
        deadline = time.monotonic() + (args.timeout if args.command == "wait" else 0)
        while True:
            value = call("GET", "/v1/cases/" + quote(args.case, safe=""))
            state = value["state"]
            ready = wait_exit(state)
            if args.command != "wait" or ready is not None:
                code = ready if ready is not None else 0
                break
            if time.monotonic() >= deadline:
                code = 3
                break
            time.sleep(1)
        if not args.full:
            value = compact(value)
    print(json.dumps(value, ensure_ascii=True, indent=2))
    return code


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, KeyError) as error:
        # Do not echo descriptor contents, headers or credential material.
        print(json.dumps({"error": {"code": "DEBUG_CLIENT_FAILED", "kind": type(error).__name__}}))
        sys.exit(2)
