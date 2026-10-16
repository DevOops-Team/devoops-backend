#!/usr/bin/env python3
"""Verify the actual staging backend before making its digest promotable."""
import argparse
import contextlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

API_SERVER = "https://172.30.0.61:6443"
NAMESPACE = "vdi-staging"
IMAGE_NAME = "ghcr.io/devoops-team/devoops-backend"


def kubectl(*args, as_json=True):
    command = ["kubectl", "--request-timeout=15s", *args]
    if as_json:
        command += ["-o", "json"]
    result = subprocess.run(command, capture_output=True, text=True, timeout=20, check=True)
    return json.loads(result.stdout) if as_json else result.stdout.strip()


def deployment_ready(app, deployment, pods, service, revision, image):
    status = app.get("status", {})
    sync = status.get("sync", {})
    operation = status.get("operationState", {})
    resources = status.get("resources", [])
    # Match infra verify-deploy.sh: prune:false leftovers alone do not block a good deployment.
    synced = sync.get("status") == "Synced" or (
        sync.get("status") == "OutOfSync" and resources and
        all(r.get("status") == "Synced" or r.get("requiresPruning") for r in resources)
    )
    if (app.get("spec", {}).get("source", {}).get("targetRevision") != "main" or
            not synced or sync.get("revision") != revision or
            status.get("health", {}).get("status") != "Healthy" or
            operation.get("phase") != "Succeeded" or
            operation.get("syncResult", {}).get("revision") != revision):
        return False
    count = deployment.get("spec", {}).get("replicas", 1)
    ds = deployment.get("status", {})
    containers = deployment.get("spec", {}).get("template", {}).get("spec", {}).get("containers", [])
    if (count < 1 or not any(c.get("name") == "backend" and c.get("image") == image for c in containers) or
            ds.get("observedGeneration") != deployment.get("metadata", {}).get("generation") or
            any(ds.get(key, 0) != count for key in ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas")) or
            ds.get("unavailableReplicas", 0) != 0):
        return False
    ss = service.get("spec", {})
    if ss.get("selector", {}).get("app") != "backend" or not any(p.get("port") == 8080 for p in ss.get("ports", [])):
        return False
    active = [p for p in pods.get("items", []) if not p.get("metadata", {}).get("deletionTimestamp")]
    if len(active) != count:
        return False
    for pod in active:
        ps = pod.get("status", {})
        if (not any(c.get("name") == "backend" and c.get("image") == image for c in pod.get("spec", {}).get("containers", [])) or
                not any(c.get("type") == "Ready" and c.get("status") == "True" for c in ps.get("conditions", [])) or
                not any(c.get("name") == "backend" and c.get("ready") and c.get("state", {}).get("running") is not None
                        for c in ps.get("containerStatuses", []))):
            return False
    return True


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(base, method, path, body=None, token=None):
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(base + path, method=method, headers=headers,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        response = urllib.request.build_opener(NoRedirect).open(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        status, content_type = response.status, response.headers.get("Content-Type", "")
        raw = response.read(1024 * 1024 + 1)
    if len(raw) > 1024 * 1024:
        raise RuntimeError("API 응답 크기 제한 초과")
    if status == 204:
        return status, None
    if "application/json" not in content_type.lower():
        raise RuntimeError("API JSON 응답 형식 불일치")
    return status, json.loads(raw)


def smoke(base, email, password, call=request):
    status, body = call(base, "GET", "/healthz")
    if status != 200 or body != {"status": "ok"}:
        raise RuntimeError("백엔드 DB 헬스 검사 실패")
    status, body = call(base, "GET", "/api/me")
    if status != 401 or not isinstance(body, dict) or body.get("code") not in ("UNAUTHENTICATED", "UNAUTHORIZED"):
        raise RuntimeError("미인증 API 차단 검사 실패")
    status, login = call(base, "POST", "/api/auth/login", {"email": email, "password": password})
    if status != 200 or not isinstance(login, dict) or not isinstance(login.get("token"), str) or not login["token"]:
        raise RuntimeError("staging 테스트 계정 로그인 실패")
    token = login["token"]
    try:
        status, me = call(base, "GET", "/api/me", token=token)
        if (status != 200 or not isinstance(me, dict) or me.get("email") != email or
                type(me.get("id")) is not int or me["id"] < 1 or me.get("role") != "USER"):
            raise RuntimeError("로그인 사용자 조회 실패: 전용 USER 계정 필요")
        status, desktops = call(base, "GET", "/api/desktops", token=token)
        if status != 200 or not isinstance(desktops, list):
            raise RuntimeError("데스크톱 목록 API 검사 실패")
    finally:
        status, _ = call(base, "POST", "/api/auth/logout", token=token)
        if status != 204:
            raise RuntimeError("검증 세션 로그아웃 실패")
    status, _ = call(base, "GET", "/api/me", token=token)
    if status != 401:
        raise RuntimeError("로그아웃 후 세션 폐기 검사 실패")


@contextlib.contextmanager
def tunnel(pod):
    with tempfile.TemporaryFile(mode="w+") as log:
        process = subprocess.Popen(["kubectl", "-n", NAMESPACE, "port-forward", "--address", "127.0.0.1",
                                    "pod/" + pod, ":8080"], stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError("백엔드 검증 터널 시작 실패")
                log.seek(0)
                match = re.search(r"Forwarding from 127\.0\.0\.1:(\d+) -> 8080", log.read())
                if match:
                    yield "http://127.0.0.1:" + match.group(1)
                    return
                time.sleep(0.2)
            raise RuntimeError("백엔드 검증 터널 대기 시간 초과")
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


def snapshot():
    return (
        kubectl("-n", "argocd", "get", "application", "portal-staging"),
        kubectl("-n", NAMESPACE, "get", "deployment", "backend"),
        kubectl("-n", NAMESPACE, "get", "pods", "-l", "app=backend"),
        kubectl("-n", NAMESPACE, "get", "service", "backend"),
    )


def verify(workspace, revision, image, email, password, get_snapshot=snapshot, open_tunnel=tunnel, check=smoke, limit=600):
    marker = workspace / ".staging-verified"
    marker.unlink(missing_ok=True)
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        current = get_snapshot()  # Kubernetes/RBAC errors fail immediately; do not hide them as a rollout wait.
        if deployment_ready(*current, revision, image):
            # Check every serving Pod of this exact immutable image, not a possibly stale external proxy.
            for pod in current[2]["items"]:
                if not pod.get("metadata", {}).get("deletionTimestamp"):
                    with open_tunnel(pod["metadata"]["name"]) as base:
                        check(base, email, password)
            if not deployment_ready(*get_snapshot(), revision, image):
                raise RuntimeError("API 검사 중 staging revision·이미지·Pod 상태가 변경되었습니다")
            marker.write_text(image)
            print("staging 배포·revision·digest·Pod·헬스·핵심 API 검증 성공")
            return
        print("staging의 이번 revision·digest·Ready 상태 대기 중", flush=True)
        time.sleep(5)
    raise RuntimeError("staging 배포 검증 시간 초과")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--preflight", action="store_true")
    args = parser.parse_args()
    if os.environ.get("SOURCE_BRANCH", os.environ.get("BRANCH_NAME")) != "develop" or os.environ.get("DEPLOY_ENV") != "staging":
        raise RuntimeError("develop → staging 검증만 허용합니다")
    if not all(os.environ.get(key) for key in ("KUBECONFIG", "SMOKE_EMAIL", "SMOKE_PASSWORD")):
        raise RuntimeError("staging 검증 Credential이 필요합니다")
    workspace = Path(os.environ.get("WORKSPACE", "."))
    (workspace / ".staging-verified").unlink(missing_ok=True)
    config = kubectl("config", "view", "--minify")
    if config.get("clusters", [{}])[0].get("cluster", {}).get("server") != API_SERVER:
        raise RuntimeError("staging API(.61:6443) kubeconfig가 아닙니다")
    if args.preflight:
        snapshot()
        if kubectl("-n", NAMESPACE, "auth", "can-i", "create", "pods/portforward", as_json=False) != "yes":
            raise RuntimeError("백엔드 검증 port-forward 권한이 없습니다")
        print("staging API·조회·port-forward 권한 사전 확인 성공")
        return
    digest = (workspace / ".deploy-digest").read_text().strip()
    revision = (workspace / ".staging-infra-revision").read_text().strip()
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest) or not re.fullmatch(r"[a-f0-9]{40}", revision):
        raise RuntimeError("배포 digest 또는 infra revision 형식 오류")
    if os.environ.get("IMAGE_NAME") != IMAGE_NAME:
        raise RuntimeError("검증 대상은 백엔드 이미지여야 합니다")
    verify(workspace, revision, IMAGE_NAME + "@" + digest, os.environ["SMOKE_EMAIL"], os.environ["SMOKE_PASSWORD"])


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    try:
        main()
    except Exception:
        # Never print kubectl stderr, HTTP response bodies, passwords, or login tokens.
        print("staging 검증 실패: Credential·권한·배포 상태·백엔드 API를 확인하세요. 승격 태그는 발행하지 않습니다.", flush=True)
        raise SystemExit(1)
