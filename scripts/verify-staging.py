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


class VerificationError(RuntimeError):
    """Only developer-controlled messages safe to show in CI logs."""


def failure_message(error):
    if isinstance(error, VerificationError):
        return str(error)
    # Do not stringify unknown exceptions: they may contain credentials or HTTP bodies.
    trace = error.__traceback__
    while trace and trace.tb_next:
        trace = trace.tb_next
    location = f"verify-staging.py:{trace.tb_lineno}" if trace else "위치 없음"
    return f"예상 밖 오류 {type(error).__name__} ({location})"


def kubectl(*args, as_json=True):
    command = ["kubectl", "--request-timeout=15s", *args]
    if as_json:
        command += ["-o", "json"]
    target = " ".join(args)  # Callers pass fixed resource names, never secrets.
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=20, check=True)
    except subprocess.TimeoutExpired:
        raise VerificationError(f"Kubernetes 요청 시간 초과: {target}") from None
    except subprocess.CalledProcessError as error:
        stderr = (error.stderr or "").lower()
        reason = "명령 실패"
        for pattern, label in (("forbidden", "권한 거부(RBAC)"), ("unauthorized", "인증 실패"),
                               ("notfound", "리소스 없음"), ("not found", "리소스 없음"),
                               ("x509", "TLS 인증서 오류"), ("timeout", "연결 시간 초과"),
                               ("connection refused", "연결 거부")):
            if pattern in stderr:
                reason = label
                break
        raise VerificationError(f"Kubernetes {reason}: {target} (exit {error.returncode})") from None
    try:
        return json.loads(result.stdout) if as_json else result.stdout.strip()
    except json.JSONDecodeError:
        raise VerificationError(f"Kubernetes JSON 응답 형식 오류: {target}") from None


def deployment_ready(app, deployment, pods, service, revision, image, rollout=None):
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
    containers = deployment.get("spec", {}).get("template", {}).get("spec", {}).get("containers", [])
    if not any(c.get("name") == "backend" and c.get("image") == image for c in containers):
        return False
    if rollout is not None:
        count = rollout.get("spec", {}).get("replicas", 1)
        rs = rollout.get("status", {})
        ref = rollout.get("spec", {}).get("workloadRef", {})
        if (count < 1 or ref.get("kind") != "Deployment" or ref.get("name") != "backend" or
                rs.get("phase") != "Healthy" or
                str(rs.get("observedGeneration")) != str(rollout.get("metadata", {}).get("generation")) or
                str(rs.get("workloadObservedGeneration")) != str(deployment.get("metadata", {}).get("generation")) or
                any(rs.get(key, 0) != count for key in ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas"))):
            return False
    else:
        count = deployment.get("spec", {}).get("replicas", 1)
        ds = deployment.get("status", {})
        if (count < 1 or ds.get("observedGeneration") != deployment.get("metadata", {}).get("generation") or
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
    except urllib.error.URLError:
        raise VerificationError(f"백엔드 API 연결 실패: {method} {path}") from None
    with response:
        status, content_type = response.status, response.headers.get("Content-Type", "")
        raw = response.read(1024 * 1024 + 1)
    if len(raw) > 1024 * 1024:
        raise VerificationError("API 응답 크기 제한 초과")
    if status == 204:
        return status, None
    if "application/json" not in content_type.lower():
        raise VerificationError("API JSON 응답 형식 불일치")
    try:
        return status, json.loads(raw)
    except json.JSONDecodeError:
        raise VerificationError(f"백엔드 API JSON 해석 실패: {method} {path} (HTTP {status})") from None


def smoke(base, email, password, call=request):
    status, body = call(base, "GET", "/healthz")
    if status != 200 or body != {"status": "ok"}:
        raise VerificationError("백엔드 DB 헬스 검사 실패")
    status, body = call(base, "GET", "/api/me")
    if status != 401 or not isinstance(body, dict) or body.get("code") not in ("UNAUTHENTICATED", "UNAUTHORIZED"):
        raise VerificationError("미인증 API 차단 검사 실패")
    status, login = call(base, "POST", "/api/auth/login", {"email": email, "password": password})
    if status != 200 or not isinstance(login, dict) or not isinstance(login.get("token"), str) or not login["token"]:
        raise VerificationError("staging 테스트 계정 로그인 실패")
    token = login["token"]
    try:
        status, me = call(base, "GET", "/api/me", token=token)
        if (status != 200 or not isinstance(me, dict) or me.get("email") != email or
                type(me.get("id")) is not int or me["id"] < 1 or me.get("role") != "USER"):
            raise VerificationError("로그인 사용자 조회 실패: 전용 USER 계정 필요")
        status, desktops = call(base, "GET", "/api/desktops", token=token)
        if status != 200 or not isinstance(desktops, list):
            raise VerificationError("데스크톱 목록 API 검사 실패")
    finally:
        status, _ = call(base, "POST", "/api/auth/logout", token=token)
        if status != 204:
            raise VerificationError("검증 세션 로그아웃 실패")
    status, _ = call(base, "GET", "/api/me", token=token)
    if status != 401:
        raise VerificationError("로그아웃 후 세션 폐기 검사 실패")


@contextlib.contextmanager
def tunnel(pod):
    with tempfile.TemporaryFile(mode="w+") as log:
        process = subprocess.Popen(["kubectl", "-n", NAMESPACE, "port-forward", "--address", "127.0.0.1",
                                    "pod/" + pod, ":8080"], stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise VerificationError("백엔드 검증 터널 시작 실패")
                log.seek(0)
                match = re.search(r"Forwarding from 127\.0\.0\.1:(\d+) -> 8080", log.read())
                if match:
                    yield "http://127.0.0.1:" + match.group(1)
                    return
                time.sleep(0.2)
            raise VerificationError("백엔드 검증 터널 대기 시간 초과")
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
        kubectl("-n", NAMESPACE, "get", "rollout", "backend"),
    )


def verify(workspace, revision, image, email, password, get_snapshot=snapshot, open_tunnel=tunnel, check=smoke, limit=600):
    marker = workspace / ".staging-verified"
    marker.unlink(missing_ok=True)
    deadline = time.monotonic() + limit
    def ready(data):
        return deployment_ready(*data[:4], revision, image, rollout=data[4] if len(data) > 4 else None)
    while time.monotonic() < deadline:
        print("staging 배포 상태 조회: Application·Deployment·Pod·Service·Rollout", flush=True)
        current = get_snapshot()  # Kubernetes/RBAC errors fail immediately; do not hide them as a rollout wait.
        if ready(current):
            # Check every serving Pod of this exact immutable image, not a possibly stale external proxy.
            for pod in current[2]["items"]:
                if not pod.get("metadata", {}).get("deletionTimestamp"):
                    print("staging backend Pod API·인증 검사 시작", flush=True)
                    with open_tunnel(pod["metadata"]["name"]) as base:
                        check(base, email, password)
            if not ready(get_snapshot()):
                raise VerificationError("API 검사 중 staging revision·이미지·Pod 상태가 변경되었습니다")
            marker.write_text(image)
            print("staging 배포·revision·digest·Pod·헬스·핵심 API 검증 성공")
            return
        print("staging의 이번 revision·digest·Ready 상태 대기 중", flush=True)
        time.sleep(5)
    raise VerificationError("staging 배포 검증 시간 초과")



def check_permissions():
    required = (
        ("argocd", "get", "applications.argoproj.io/portal-staging"),
        (NAMESPACE, "get", "deployments.apps/backend"),
        (NAMESPACE, "get", "rollouts.argoproj.io/backend"),
        (NAMESPACE, "get", "services/backend"),
        (NAMESPACE, "get", "pods"),
        (NAMESPACE, "list", "pods"),
        (NAMESPACE, "watch", "pods"),
        (NAMESPACE, "create", "pods/portforward"),
    )
    missing = []
    for namespace, verb, resource in required:
        try:
            allowed = kubectl("-n", namespace, "auth", "can-i", verb, resource, as_json=False) == "yes"
        except (subprocess.CalledProcessError, VerificationError):
            allowed = False
        print(f"staging 권한: {namespace} {verb} {resource}: {'yes' if allowed else 'no'}", flush=True)
        if not allowed:
            missing.append((namespace, verb, resource))
    if missing:
        raise VerificationError("백엔드 staging 조회·port-forward 권한이 부족합니다")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--preflight", action="store_true")
    args = parser.parse_args()
    if os.environ.get("SOURCE_BRANCH", os.environ.get("BRANCH_NAME")) != "develop" or os.environ.get("DEPLOY_ENV") != "staging":
        raise VerificationError("develop → staging 검증만 허용합니다")
    if not all(os.environ.get(key) for key in ("KUBECONFIG", "SMOKE_EMAIL", "SMOKE_PASSWORD")):
        raise VerificationError("staging 검증 Credential이 필요합니다")
    workspace = Path(os.environ.get("WORKSPACE", "."))
    (workspace / ".staging-verified").unlink(missing_ok=True)
    print("staging kubeconfig API 주소 확인", flush=True)
    config = kubectl("config", "view", "--minify")
    if config.get("clusters", [{}])[0].get("cluster", {}).get("server") != API_SERVER:
        raise VerificationError("staging API(.61:6443) kubeconfig가 아닙니다")
    if args.preflight:
        check_permissions()
        snapshot()
        print("staging API·조회·port-forward 권한 사전 확인 성공")
        return
    print("staging 배포 digest·infra revision 파일 확인", flush=True)
    values = []
    for name in (".deploy-digest", ".staging-infra-revision"):
        try:
            values.append((workspace / name).read_text().strip())
        except OSError:
            raise VerificationError(f"검증 입력 파일을 읽을 수 없습니다: {name}") from None
    digest, revision = values
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest) or not re.fullmatch(r"[a-f0-9]{40}", revision):
        raise VerificationError("배포 digest 또는 infra revision 형식 오류")
    if os.environ.get("IMAGE_NAME") != IMAGE_NAME:
        raise VerificationError("검증 대상은 백엔드 이미지여야 합니다")
    verify(workspace, revision, IMAGE_NAME + "@" + digest, os.environ["SMOKE_EMAIL"], os.environ["SMOKE_PASSWORD"])


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    try:
        main()
    except Exception as error:
        # Only our controlled messages are printed; no raw stderr, response, or exception text.
        print(f"staging 검증 실패: {failure_message(error)}. 승격 태그는 발행하지 않습니다.", flush=True)
        raise SystemExit(1)
