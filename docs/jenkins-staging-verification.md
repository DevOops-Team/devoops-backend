# staging 확인과 digest 직접 승격

## Jenkins 흐름

- develop: 정적·단위·Mongo 통합 테스트 → 이미지 빌드 → Trivy → GHCR 업로드 → 격리 기동 검사 → staging digest 갱신 → release.json 보관.
- main: CollabOps manifests main의 staging digest 자동 조회 → digest로 직접 pull → 이미지 소스와 main 비교 → prod 매니페스트 갱신.

승격용 tree 태그 발행·조회 단계는 없다. 빌드 태그는 이력 추적용으로 유지한다.
이미지에는 `org.opencontainers.image.revision`(빌드 소스 커밋), `io.devoops.source-tree`(전체 파일 내용 해시)를 기록한다.
main에서는 source-tree 라벨이 현재 main의 전체 tree 해시와 같아야 한다. 이 값은 이미지 메타데이터이며 태그로 발행하지 않는다.

## 담당자의 승격 절차

1. 이 Jenkinsfile로 develop을 빌드한다. 성공한 빌드의 Artifacts에서 `release.json`을 확인한다.
2. 기록된 image·digest·source_commit·source_tree·staging_manifest_revision을 확인하고, 해당 digest의 staging 배포·로그인·핵심 기능을 검증한다.
3. 동일한 소스 내용의 develop → main 릴리스 CR을 승인·머지한다.
4. 머지 웹훅으로 main 잡이 실행되면 staging 매니페스트에서 백엔드 digest를 자동 조회한다. 수동 입력은 없다.
5. Jenkins의 prod 매니페스트 갱신 성공을 확인하고, 실제 배포 상태는 Argo CD에서 확인한다. 배포된 앱의 기능은 별도로 검증한다.

`release.json`은 staging 매니페스트 갱신과 원격 digest 일치 확인이 성공한 뒤에만 생성·보관한다.
기록 파일에는 image, digest, source_commit, source_tree, staging_manifest_revision, build_number가 들어간다.
릴리스 기록 생성 자체가 실제 staging 배포·기능 검증 성공을 뜻하지 않는다. 실제 확인은 담당자가 수행한다.
main 이미지 조회 단계에서 staging 매니페스트를 한 번 읽어 digest를 고정한다. 이후 staging이 바뀌어도 해당 실행의 digest는 바뀌지 않는다. 조회 시점에 이미 staging이 다음 develop 소스로 바뀌었다면 main과 이미지 소스 비교에서 중단한다.
기존 manifests `ci/update-image.sh`의 staging 이력 방어는 유지한다.

## Jenkins 사용 변경과 전환

- main 웹훅 실행은 staging 매니페스트 digest를 자동 사용한다. PROMOTE_DIGEST 파라미터는 제거했다.
- 조회 실패·잘못된 digest·소스 불일치는 prod 변경 전에 중단한다.
- Jenkins는 kubeconfig·kubectl·운영 조회 RBAC를 사용하지 않는다. 기존 `k8s-staging-mongo` 자격 증명도 참조하지 않는다.
- 과거 라벨 없는 이미지는 새 소스 일치 검증에 통과하지 못한다. 변경 후 develop을 새로 빌드·검증한 릴리스를 사용한다.
- main의 소스가 선택한 이미지와 다르면 실패한다. merge commit SHA가 달라도 파일 내용이 같으면 전체 tree 해시는 같으므로 정상 승격할 수 있다.
- 실패한 main 빌드를 무조건 재실행하지 않는다. 승인한 digest와 현재 prod 상태를 먼저 확인한다. 자동 롤백은 하지 않는다.
- 이번 변경은 백엔드 develop 코드 변경이며 main 병합·실제 Jenkins 자동 빌드·클러스터 검증은 별도다.

## 검사 범위

Jenkins develop은 staging Application·Deployment·Pod 상태나 staging API를 검사하지 않는다.
Mongo 통합 테스트는 빌드별 임시 MongoDB 8 replica set을 사용하고 종료 시 컨테이너·볼륨을 삭제한다. `k8s-staging-mongo` Credential과 staging DB 접근 권한은 더 이상 필요하지 않다. 실행 방법은 README의 Jenkins 통합 테스트 설명을 따른다.
main은 prod 매니페스트 갱신까지만 수행한다. Argo CD 설정·동기화 정책은 변경하지 않는다.
실제 배포 참조는 staging·prod 모두 불변 digest다.

## 배포 전 이미지 기동 검사

develop: 이미지 업로드 → 이미지 기동 검사 → staging 매니페스트 갱신 → release.json 보관.
main: 이미지 조회 → prod 매니페스트 갱신 → 종료.

배포 전 `scripts/check-image-startup.py`는 `.deploy-digest`의 정확한 불변 이미지를 사용한다.
Docker internal 네트워크에 임시 MongoDB 8.0 replica set과 Python 3.13 OpenStack 인증 대체 서버를
준비한다. 실제 운영 Mongo·OpenStack·Guacamole 자격증명을 사용하지 않으며 외부 포트를 열거나
호스트 경로를 마운트하지 않는다. 관리자·키는 임시 값이며 DB와 함께 폐기한다.
새 서버의 `/healthz` JSON 200·status ok(Mongo ping 포함), 미인증 `/api/me` JSON 401을
3회 연속 확인한다. 실패하면 staging 매니페스트 갱신·릴리스 기록 단계에 도달하지 않는다.
종료·실패 시 API·Mongo·대체 서버 컨테이너와 Mongo 익명 볼륨·네트워크를 정리한다.
Jenkins Docker daemon에서 mongo:8.0, python:3.13-alpine을 내려받을 수 있어야 한다.
운영 OpenStack·실제 이미지 목록·VM 생성·RDP 연결 검증은 이 기동 검사의 범위가 아니다.

## 배포 완료와 기능 확인

Jenkins 성공은 매니페스트 갱신 성공을 의미한다. 실제 롤링 업데이트·배포 상태는 Argo CD와
Kubernetes에서 확인한다. Jenkins는 배포 완료를 기다리거나 운영 API를 검사하지 않는다.
배포된 앱의 로그인·DB 읽기·쓰기 등 기능 검사는 현재 자동화돼 있지 않다. 담당자가 별도로
확인해야 하며 Argo CD Healthy만으로 전체 기능 정상을 판단하지 않는다.
자동 롤백은 하지 않으며 필요하면 Git의 prod digest를 이전 정상 값으로 복구한다.

운영 kubeconfig 발급·권한 추가는 이 Jenkins 파이프라인에 필요하지 않다. 기존 자격 증명이나
클러스터 RBAC를 이번 코드 변경으로 삭제하거나 수정하지 않는다.
이 흐름은 변경을 main에 병합한 뒤 적용된다.

로컬 검사: `python3 scripts/test-main-health.py`, `python3 scripts/test-image-release.py`,
`python3 scripts/test-notify-discord.py`(외부 접근 없이 모의 검증).
