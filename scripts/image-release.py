#!/usr/bin/env python3
"""Read the staging manifest digest and verify production promotion."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

IMAGE = 'ghcr.io/devoops-team/devoops-backend'
TREE_LABEL = 'io.devoops.source-tree'


def digest(value):
    value = value.strip()
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', value):
        raise ValueError('staging 매니페스트에 유효한 sha256 digest가 필요합니다')
    return value


def context(env, branch, target):
    if env.get('SOURCE_BRANCH') != branch or env.get('DEPLOY_ENV') != target:
        raise ValueError('브랜치와 릴리스 환경이 일치하지 않습니다')
    if env.get('IMAGE_NAME') != IMAGE:
        raise ValueError('백엔드 이미지 저장소만 허용합니다')
    tree = env.get('SOURCE_TREE', '')
    if not re.fullmatch(r'[a-f0-9]{40}|[a-f0-9]{64}', tree):
        raise ValueError('소스 tree 전체 해시가 필요합니다')
    return Path(env['WORKSPACE']), tree


def prepare_prod(env=os.environ):
    root, _ = context(env, 'main', 'prod')
    # Read once from the cloned CollabOps main snapshot. Subsequent staging
    # updates cannot change this run's selected digest.
    manifests = root / 'promotion-manifests'
    result = subprocess.run(
        ['awk', '-v', 'mode=get', '-v', 'img=' + IMAGE,
         '-f', str(manifests / 'ci/images.awk'),
         str(manifests / 'overlays/staging/kustomization.yaml')],
        check=True, capture_output=True, text=True, timeout=30)
    selected = digest(result.stdout)
    (root / '.deploy-digest').write_text(selected + '\n')
    print('staging 매니페스트에서 자동 조회한 digest: ' + selected, flush=True)


def verify_prod(env=os.environ, inspect=None):
    root, tree = context(env, 'main', 'prod')
    selected = digest((root / '.deploy-digest').read_text())
    image = IMAGE + '@' + selected
    if inspect is None:
        def inspect(reference):
            result = subprocess.run(['docker', 'inspect', '--format', '{{json .Config.Labels}}', reference],
                                    check=True, capture_output=True, text=True, timeout=30)
            return json.loads(result.stdout)
    labels = inspect(image)
    if not isinstance(labels, dict) or labels.get(TREE_LABEL) != tree:
        raise ValueError('승격 이미지의 소스가 main과 다릅니다. main과 같은 소스의 develop 빌드가 staging에 반영된 후 다시 실행하세요')
    print('staging digest 이미지와 main 소스 일치 확인 완료', flush=True)


def record_staging(env=os.environ):
    root, tree = context(env, 'develop', 'staging')
    selected = digest((root / '.deploy-digest').read_text())
    commit = env.get('SOURCE_COMMIT', '')
    revision = (root / '.staging-infra-revision').read_text().strip()
    if not all(re.fullmatch(r'[a-f0-9]{40}|[a-f0-9]{64}', value) for value in (commit, revision)):
        raise ValueError('소스 커밋과 staging 매니페스트 revision 전체 값이 필요합니다')
    release = dict(image=IMAGE, digest=selected, source_commit=commit, source_tree=tree,
                   staging_manifest_revision=revision, build_number=env.get('BUILD_NUMBER', ''))
    (root / 'release.json').write_text(json.dumps(release, indent=2) + '\n')
    print('릴리스 기록 생성 완료. main은 staging 매니페스트의 digest를 자동 조회합니다.', flush=True)


if __name__ == '__main__':
    try:
        {'prepare-prod': prepare_prod, 'verify-prod': verify_prod, 'record-staging': record_staging}[sys.argv[1]]()
    except Exception as error:
        print(str(error) if isinstance(error, ValueError) else '릴리스 기록·이미지 확인 실패', file=sys.stderr)
        raise SystemExit(1)
