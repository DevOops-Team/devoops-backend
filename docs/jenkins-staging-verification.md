# staging 확인과 digest 직접 승격

## Jenkins 흐름

- develop: 정적·단위·Mongo 통합 테스트 → 이미지 빌드 → Trivy → GHCR 업로드 → 격리 기동 검사 → staging digest 갱신 → release.json 보관.
- main: PROMOTE_DIGEST에 지정한 이미지를 digest로 직접 pull → 이미지 소스와 main 비교 → prod 매니페스트 갱신 → 배포 완료 대기 → 운영 HTTP 확인.

승격용 tree 태그 발행·조회 단계는 없다. 빌드 태그는 이력 추적용으로 유지한다.
이미지에는 `org.opencontainers.image.revision`(빌드 소스 커밋), `io.devoops.source-tree`(전체 파일 내용 해시)를 기록한다.
main에서는 source-tree 라벨이 현재 main의 전체 tree 해시와 같아야 한다. 이 값은 이미지 메타데이터이며 태그로 발행하지 않는다.

## 담당자의 승격 절차

1. 이 Jenkinsfile로 develop을 빌드한다. 성공한 빌드의 Artifacts에서 `release.json`을 확인한다.
2. 기록된 image·digest·source_commit·source_tree·staging_manifest_revision을 확인하고, 해당 digest의 staging 배포·로그인·핵심 기능을 검증한다.
3. 동일한 소스 내용의 develop → main 릴리스 CR을 승인·머지한다.
4. main 잡의 **Build with Parameters**에서 `PROMOTE_DIGEST`에 검증한 `release.json`의 digest 전체 값(`sha256:` 포함)을 입력한다.
5. main의 이미지 조회·소스 일치·prod 배포 완료·HTTP 결과를 확인한다.

`release.json`은 staging 매니페스트 갱신과 원격 digest 일치 확인이 성공한 뒤에만 생성·보관한다.
기록 파일에는 image, digest, source_commit, source_tree, staging_manifest_revision, build_number가 들어간다.
릴리스 기록 생성 자체가 실제 staging 배포·기능 검증 성공을 뜻하지 않는다. 실제 확인은 담당자가 수행한다.
다음 develop 빌드가 staging을 바꿔도 승인한 release.json의 digest를 명시적으로 선택하므로 최신 staging 값으로 자동 교체되지 않는다.
기존 manifests `ci/update-image.sh`의 staging 이력 방어는 유지한다.

## Jenkins 사용 변경과 전환

- main 자동 webhook 실행은 PROMOTE_DIGEST가 비어 있으면 소스 단계에서 실패하고 prod를 변경하지 않는다. 운영 승격은 digest를 입력한 파라미터 빌드로 실행한다.
- develop 빌드에는 PROMOTE_DIGEST가 필요 없다.
- 운영 조회 Credential 파라미터 PROD_VERIFY_CREDENTIALS는 기존대로 유지한다.
- 과거 라벨 없는 이미지는 새 소스 일치 검증에 통과하지 못한다. 변경 후 develop을 새로 빌드·검증한 릴리스를 사용한다.
- main의 소스가 선택한 이미지와 다르면 실패한다. merge commit SHA가 달라도 파일 내용이 같으면 전체 tree 해시는 같으므로 정상 승격할 수 있다.
- 실패한 main 빌드를 무조건 재실행하지 않는다. 승인한 digest와 현재 prod 상태를 먼저 확인한다. 자동 롤백은 하지 않는다.
- 이번 변경은 백엔드 develop 코드 변경이며 main 병합·실제 Jenkins 파라미터 빌드·클러스터 검증은 별도다.

## 검사 범위

Jenkins develop은 staging Application·Deployment·Pod 상태나 staging API를 검사하지 않는다.
Mongo 통합 테스트는 빌드별 임시 MongoDB 8 replica set을 사용하고 종료 시 컨테이너·볼륨을 삭제한다. `k8s-staging-mongo` Credential과 staging DB 접근 권한은 더 이상 필요하지 않다. 실행 방법은 README의 Jenkins 통합 테스트 설명을 따른다.
main은 운영 Deployment/Rollout·Pod를 읽기 전용으로 조회한다. Argo CD 설정·동기화 정책은 변경하지 않는다.
실제 배포 참조는 staging·prod 모두 불변 digest다.

## 이미지 기동 검사와 운영 헬스체크

develop: 이미지 업로드 → 이미지 기동 검사 → staging 매니페스트 갱신 → release.json 보관.
main: 이미지 조회 → prod 매니페스트 갱신 → 목표 digest 배포 완료 → 운영 HTTP 확인.

배포 전 `scripts/check-image-startup.py`는 `.deploy-digest`의 정확한 불변 이미지를 사용한다.
Docker internal 네트워크에 임시 MongoDB 8.0 replica set과 Python 3.13 OpenStack 인증 대체 서버를
준비한다. 실제 운영 Mongo·OpenStack·Guacamole 자격증명을 사용하지 않으며 외부 포트를 열거나
호스트 경로를 마운트하지 않는다. 관리자·키는 임시 값이며 DB와 함께 폐기한다.
새 서버의 `/healthz` JSON 200·status ok(Mongo ping 포함), 미인증 `/api/me` JSON 401을
3회 연속 확인한다. 실패하면 staging 매니페스트 갱신·릴리스 기록 단계에 도달하지 않는다.
종료·실패 시 API·Mongo·대체 서버 컨테이너와 Mongo 익명 볼륨·네트워크를 정리한다.
Jenkins Docker daemon에서 mongo:8.0, python:3.13-alpine을 내려받을 수 있어야 한다.
운영 OpenStack·실제 이미지 목록·VM 생성·RDP 연결 검증은 이 기동 검사의 범위가 아니다.

배포 지시 후 `scripts/check-prod-health.py`는 PROD_HEALTHCHECK_URL(기본 https://devoops.site)의
`/backend/api/me`, `/backend/api/desktops`가 미인증 JSON 401과 예상 오류 코드를 반환하는지
3회 연속 확인한다. 리다이렉트·HTML 응답·다른 상태 코드는 실패다. 사용자 비밀번호 없이
운영 프록시와 백엔드 인증 경계를 확인하는 검사이며 인증 성공·DB 상태를 증명하지 않는다.
HTTP 요청당 10초 제한, 최대 30회 재시도한다.

운영 HTTP 검사 전에 `.deploy-digest`의 목표 이미지를 사용하는 backend Deployment의
관찰된 generation과 updated/ready/available/전체 replicas를 확인한다. 전체 backend Pod도
목표 이미지·Running·Ready여야 하며 이전 이미지·종료 중 Pod가 남아 있으면 기다린다.
backend Rollout이 존재하면 workloadRef가 backend Deployment인지, 두 리소스의 관찰된
세대와 Rollout Healthy·replicas까지 검사한다. 롤링 전환으로 Rollout/CRD가 없으면
Deployment 기준으로 검사한다. 권한·접속 오류는 Rollout 부재로 처리하지 않는다.
HTTP 3회 확인 때마다 배포 상태도 다시 확인한다. 외부 API는 인증 경로 확인이며
로그인·VM 생성 등 전체 기능 성공을 보장하지 않는다.

### 운영 조회 Credential 설정

Jenkins Secret file에 운영 읽기 전용 kubeconfig를 등록한다. 기본 ID는 `k8s-prod-readonly`이며
잡 파라미터 `PROD_VERIFY_CREDENTIALS`로 기존 ID를 선택할 수 있다. staging Mongo용
Credential은 재사용하지 않는다. main의 도구 확인 단계에서 Credential과 Deployment·Pod
조회가 가능한지 먼저 검사하므로 누락 시 prod 매니페스트 변경 전에 실패한다.

필요 권한은 운영 클러스터 `vdi-prod` 네임스페이스에 한정한다.

- apps/deployments: get (backend)
- core/pods: list
- argoproj.io/rollouts: get (backend, CRD가 있는 클러스터)

생성·수정·삭제·exec·Secret 조회 권한은 필요 없다. kubeconfig와 토큰은 Git에 넣지 않는다.
Jenkins agent에 kubectl 설치와 운영 API 서버 접근 경로가 필요하다.
이 변경은 Credential/RBAC를 실제 서버에 생성하지 않으므로 운영 적용 전에 등록해야 한다.

배포 상태는 최대 90회(회당 10초 간격, 요청 시간 별도) 조회하고 HTTP는 최대 30회 재시도한다.
전체 운영 확인 단계는 45분으로 제한한다. 실패 시 prod 매니페스트는 이미 바뀌었을 수 있으며
자동 롤백하지 않는다. Git의 prod digest 복구 등 기존 장애 대응 절차를 따른다.
이 Jenkinsfile이 main에 병합된 뒤 운영 검사에 적용된다. 실제 클러스터 검증은 별도다.

로컬 검사: `make test`, `python3 scripts/test-main-health.py`,
`python3 scripts/test-prod-deployment.py`, `python3 scripts/test-image-release.py`(운영 접근 없이 모의 검증).
