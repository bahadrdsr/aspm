"""Explicit local-only application smoke test with synthetic persisted records."""

import argparse
import datetime
import http.cookiejar
import ipaddress
import json
import os
from pathlib import Path
import secrets
import ssl
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", type=Path, required=True)
    parser.add_argument("--origin", default="https://127.0.0.1:18443")
    parser.add_argument("--enroll", action="store_true", help="Explicitly enroll the local synthetic administrator if no saved fixture account exists")
    parser.add_argument("--reports", action="store_true", help="Also require the independently running snapshot worker")
    args = parser.parse_args()
    origin = urllib.parse.urlsplit(args.origin)
    loopback = origin.hostname == "localhost"
    if not loopback:
        try:
            loopback = ipaddress.ip_address(origin.hostname or "").is_loopback
        except ValueError:
            loopback = False
    if origin.scheme != "https" or not loopback or origin.username is not None or origin.path or origin.query or origin.fragment:
        raise SystemExit("This fixture only accepts a literal local HTTPS origin.")
    runtime = args.runtime.resolve()
    config = json.loads((runtime / "runtime.json").read_text(encoding="utf-8-sig"))
    opener = urllib.request.build_opener(
        urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(runtime / "development-ca.pem"))),
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
        NoRedirect(),
    )

    def call(method, path, body=None, headers=None):
        request_headers = {"Content-Type": "application/json", "Origin": args.origin, "Accept": "application/json"}
        request_headers.update(headers or {})
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(args.origin + path, data=data, headers=request_headers, method=method)
        try:
            with opener.open(request, timeout=15) as response:
                raw = response.read(2 * 1024 * 1024)
                return response.status, json.loads(raw) if raw else {}
        except urllib.error.HTTPError as error:
            return error.code, {"failed": True}

    def require(condition, message):
        if not condition:
            raise SystemExit(message)

    status, _ = call("GET", "/api/v1/work")
    require(status == 401, f"Anonymous finding access returned {status}.")
    account_path = runtime / "development-account.json"
    if account_path.exists():
        user = json.loads(account_path.read_text(encoding="utf-8"))
    else:
        require(args.enroll, "A fixture account is absent; explicit --enroll is required to create one.")
        user = {"email": "local-admin@aspm.invalid", "password": "Aa9!" + secrets.token_urlsafe(40)}
        status, _ = call(
            "POST", "/api/v1/bootstrap",
            dict(user, workspaceName="Local validation", name="Local administrator"),
            {"X-ASPM-Bootstrap-Token": config["bootstrapToken"]},
        )
        require(status == 201, f"Bootstrap returned {status}; no existing account was reset.")
        fd = os.open(account_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as file:
            json.dump(user, file)
    status, session = call("POST", "/api/v1/login", user)
    require(status == 200, f"Login returned {status}.")
    workspaces = [item for item in session["workspaces"] if item["name"] == "Local validation"]
    require(len(workspaces) == 1, "The dedicated Local validation workspace is missing or ambiguous; no other workspace was selected.")
    workspace = workspaces[0]["id"]
    headers = {"X-ASPM-Workspace-ID": workspace}
    status, assets = call("GET", "/api/v1/assets", headers=headers)
    require(status == 200, f"Asset list returned {status}.")
    asset = next((item for item in assets["items"] if item["name"] == "ASPM synthetic validation fixture"), None)
    if asset is None:
        status, reply = call("POST", "/api/v1/assets", {
            "name": "ASPM synthetic validation fixture", "kind": "repository",
            "environment": "test", "criticality": "low", "tags": ["synthetic", "local-validation"], "ownerId": None,
        }, headers)
        require(status == 201, f"Asset creation returned {status}.")
        asset = reply["asset"]
    report = {
        "version": "2.1.0",
        "runs": [{
            "tool": {"driver": {"name": "ASPM synthetic validation", "rules": [{
                "id": "LOCAL-001", "shortDescription": {"text": "Synthetic validation finding - not a real vulnerability"},
                "fullDescription": {"text": "Deliberately generated local end-to-end fixture."},
                "help": {"text": "No production remediation is required for this synthetic record."},
            }]}},
            "results": [{
                "ruleId": "LOCAL-001", "guid": "local-validation-guid", "level": "note",
                "message": {"text": "Synthetic evidence for the authenticated import pipeline."},
                "locations": [{"physicalLocation": {"artifactLocation": {"uri": "synthetic/fixture.txt"}, "region": {"startLine": 1}}}],
            }],
        }],
    }
    status, imported = call("POST", "/api/v1/imports", {
        "apiVersion": "aspm/v1alpha1", "assetId": asset["id"], "format": "sarif",
        "report": json.dumps(report), "sourceId": "local-validation", "scanId": str(uuid.uuid4()),
        "scope": {"id": "local-validation", "revision": "v1", "branch": "validation"}, "sourceScanAt": None,
        "collectedAt": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
        "sourceStatus": "succeeded", "scanKind": "full", "completeness": "complete",
    }, headers)
    require(status == 202, f"Import submission returned {status}.")
    job = imported["import"]
    require(job["state"] == "queued", "An accepted import was not represented as queued.")
    for _ in range(100):
        status, progress = call("GET", "/api/v1/imports/" + job["id"], headers=headers)
        require(status == 200, f"Import progress returned {status}.")
        if progress["import"]["state"] in ("succeeded", "failed"):
            break
        time.sleep(0.2)
    require(progress["import"]["state"] == "succeeded", "Independent import worker did not complete the fixture.")
    status, findings = call("GET", "/api/v1/work?q=Synthetic", headers=headers)
    require(status == 200 and len(findings["items"]) >= 1, "The imported fixture was not returned by the work API.")
    status, detail = call("GET", "/api/v1/findings/" + findings["items"][0]["id"], headers=headers)
    require(status == 200 and detail["finding"]["evidence"]["verificationState"] == "not-run", "Fixture evidence was incorrectly represented as verified.")
    if args.reports:
        status, overview = call("GET", "/api/v1/reports/overview?freshnessDays=7", headers=headers)
        require(status == 200 and overview["report"]["totals"]["findings"] >= 1, "The authorized report did not reflect stored findings.")
        status, pending = call("POST", "/api/v1/reports/snapshots", {"name": "Local synthetic snapshot", "freshnessDays": 7}, headers)
        require(status == 202 and pending["snapshot"]["state"] == "queued", "Snapshot was not queued.")
        for _ in range(100):
            status, saved = call("GET", "/api/v1/reports/snapshots/" + pending["snapshot"]["id"], headers=headers)
            require(status == 200, "Snapshot progress lookup failed.")
            if saved["snapshot"]["state"] in ("succeeded", "failed"):
                break
            time.sleep(0.2)
        require(saved["snapshot"]["state"] == "succeeded" and saved["snapshot"]["report"]["totals"]["assets"] >= 1, "Independent report worker did not complete the snapshot.")
    status, _ = call("POST", "/api/v1/logout", headers=headers)
    require(status == 204, f"Logout returned {status}.")
    status, _ = call("GET", "/api/v1/work", headers=headers)
    require(status == 401, "Logout did not revoke access.")
    print("PASS: trusted HTTPS, anonymous denial, bootstrap/login, assets, queued import, independent processing, source evidence, logout.")
    if args.reports:
        print("PASS: authorized actual-data overview and independent durable snapshot worker.")
    print("Records are explicitly synthetic. Account credentials remain only in the private runtime directory.")


if __name__ == "__main__":
    main()
