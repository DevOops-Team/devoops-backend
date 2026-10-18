#!/usr/bin/env python3
"""Check the public backend proxy, after verifying the target image has finished deploying."""
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request


class DeploymentError(RuntimeError):
    pass


def kubectl(resource, optional=False):
    # Fixed resources only. Never print kubeconfig, server responses, or credentials.
    result = subprocess.run(
        ['kubectl', '--request-timeout=10s', '-n', 'vdi-prod', 'get', resource,
         '--ignore-not-found=true', '-o', 'json'],
        capture_output=True, text=True, timeout=15)
    if result.returncode:
        # A rolling-only cluster need not have the Argo Rollouts CRD installed.
        if optional and "doesn't have a resource type" in result.stderr:
            return None
        raise DeploymentError('운영 Kubernetes 조회 실패: 권한·연결을 확인하세요')
    return json.loads(result.stdout) if result.stdout.strip() else None


def deployment_ready(deployment, rollout, pods, image):
    if not deployment or not deployment.get('metadata', {}).get('generation'):
        return False
    spec, status = deployment.get('spec', {}), deployment.get('status', {})
    containers = spec.get('template', {}).get('spec', {}).get('containers', [])
    if not any(c.get('name') == 'backend' and c.get('image') == image for c in containers):
        return False
    if rollout:
        rspec, status = rollout.get('spec', {}), rollout.get('status', {})
        ref = rspec.get('workloadRef', {})
        count = rspec.get('replicas', 1)
        if (ref.get('kind') != 'Deployment' or ref.get('name') != 'backend' or
                status.get('phase') != 'Healthy' or status.get('abort') or status.get('pauseConditions') or
                str(status.get('observedGeneration')) != str(rollout.get('metadata', {}).get('generation')) or
                str(status.get('workloadObservedGeneration')) != str(deployment.get('metadata', {}).get('generation'))):
            return False
    else:
        count = spec.get('replicas', 1)
        if status.get('observedGeneration') != deployment.get('metadata', {}).get('generation'):
            return False
    if (not isinstance(count, int) or count < 1 or
            any(status.get(key, 0) != count for key in ('replicas', 'updatedReplicas', 'readyReplicas', 'availableReplicas')) or
            status.get('unavailableReplicas', 0) != 0):
        return False
    # Include terminating old Pods: do not pass while an old backend is still serving.
    items = pods.get('items', [])
    if len(items) != count:
        return False
    for pod in items:
        ps = pod.get('status', {})
        if (pod.get('metadata', {}).get('deletionTimestamp') or ps.get('phase') != 'Running' or
                not any(c.get('name') == 'backend' and c.get('image') == image
                        for c in pod.get('spec', {}).get('containers', [])) or
                not any(c.get('type') == 'Ready' and c.get('status') == 'True' for c in ps.get('conditions', [])) or
                not any(c.get('name') == 'backend' and c.get('ready') is True and 'running' in c.get('state', {})
                        for c in ps.get('containerStatuses', []))):
            return False
    return True


def check_deployment(image):
    deployment = kubectl('deployment/backend')
    rollout = kubectl('rollouts.argoproj.io/backend', optional=True)
    # Filtering here keeps kubectl arguments fixed and limits RBAC to this namespace.
    pods = kubectl('pods') or {}
    pods['items'] = [p for p in pods.get('items', []) if p.get('metadata', {}).get('labels', {}).get('app') == 'backend']
    return deployment_ready(deployment, rollout, pods, image)


def wait_deployed(image, check=check_deployment, sleep=time.sleep, attempts=90):
    for attempt in range(attempts):
        if check(image):
            print('목표 digest의 운영 배포·전체 Pod Ready 확인 완료', flush=True)
            return
        print('목표 digest의 운영 배포 완료 대기', flush=True)
        if attempt + 1 < attempts:
            sleep(10)
    raise DeploymentError('목표 digest의 운영 배포 완료 시간 초과')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(url):
    try:
        response = urllib.request.build_opener(NoRedirect).open(
            urllib.request.Request(url, headers={
                'Accept': 'application/json',
                'Cache-Control': 'no-cache',
                'User-Agent': 'Devoops-Jenkins-Healthcheck/1.0',
            }), timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = response.read(65537)
        if len(data) > 65536:
            raise ValueError('응답 크기 제한')
        return response.status, response.headers.get('Content-Type',''), json.loads(data)


def check_once(base, get=request):
    for path in ('/backend/api/me', '/backend/api/desktops'):
        status, content_type, body = get(base + path)
        if (status != 401 or 'application/json' not in content_type.lower() or
                not isinstance(body, dict) or body.get('code') not in ('UNAUTHENTICATED','UNAUTHORIZED')):
            raise ValueError('운영 백엔드 인증 경계 HTTP 검사 실패')


def wait_healthy(base, check=check_once, sleep=time.sleep, attempts=30):
    consecutive = 0
    for attempt in range(attempts):
        try:
            check(base)
            consecutive += 1
            print(f'운영 백엔드 HTTP 정상 ({consecutive}/3 연속)', flush=True)
            if consecutive == 3:
                return
        except Exception:
            consecutive = 0
            print('운영 백엔드 HTTP 재확인 대기', flush=True)
        if attempt + 1 < attempts:
            sleep(10)
    raise RuntimeError('운영 HTTP 검사에서 3회 연속 정상 응답을 확인하지 못했습니다')


def main(env=os.environ):
    if env.get('SOURCE_BRANCH',env.get('BRANCH_NAME')) != 'main' or env.get('DEPLOY_ENV') != 'prod':
        raise ValueError('main → prod HTTP 검사만 허용합니다')
    base = env.get('PROD_HEALTHCHECK_URL','')
    parsed = urllib.parse.urlsplit(base)
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password or
            parsed.path not in ('','/') or parsed.query or parsed.fragment):
        raise ValueError('운영 HTTPS origin이 필요합니다')
    if not env.get('KUBECONFIG'):
        raise ValueError('운영 읽기 전용 KUBECONFIG가 필요합니다')
    digest = (Path(env['WORKSPACE']) / '.deploy-digest').read_text().strip()
    image_name = env.get('IMAGE_NAME')
    if image_name != 'ghcr.io/devoops-team/devoops-backend' or not re.fullmatch(r'sha256:[a-f0-9]{64}', digest):
        raise ValueError('백엔드 불변 이미지 digest가 필요합니다')
    image = image_name + '@' + digest
    wait_deployed(image)
    # Recheck the target on each HTTP sample: a superseding deployment must not pass.
    def verified_http(origin):
        if not check_deployment(image):
            raise DeploymentError('운영 배포 상태가 변경되었습니다')
        check_once(origin)
    wait_healthy(base.rstrip('/'), check=verified_http)


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('운영 백엔드 HTTP 검사 실패. Argo CD·운영 접근 경로를 확인하세요. 자동 롤백하지 않습니다.', flush=True)
        raise SystemExit(1)
