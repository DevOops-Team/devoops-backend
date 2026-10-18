# staging 확인과 prod 승격

## 현재 Jenkins 흐름

- develop: 정적·단위·Mongo 통합 테스트 → 이미지 빌드 → Trivy → GHCR 업로드 → 임시 컨테이너 기동 검사 →
  devoops-manifests main의 staging digest 갱신 → 동일 digest의 `tree-<내용해시>` 태그 발행.
- main: 동일 내용의 tree 태그 조회 → 동일 digest로 prod 매니페스트 갱신 → 목표 digest 배포 완료 → 운영 HTTP 확인.
  새 이미지를 빌드하지 않는다. 대응 태그가 없으면 실패한다.

Jenkins develop은 staging Application·Deployment·Rollout·Pod 상태나 staging API를 검사하지 않는다.
main은 운영 Deployment/Rollout·Pod를 읽기 전용으로 조회한다. Argo CD Application 상태는 직접 조회하지 않는다.
develop의 이미지 기동·main의 운영 HTTP 검사는 아래 별도 절차로 수행한다.
기존 `scripts/verify-staging.py`는 선택적 진단용으로 남겨두지만 Jenkinsfile에서는 호출하지 않는다.
검증용 STAGING_VERIFY_CREDENTIALS·STAGING_SMOKE_CREDENTIALS 파라미터도 사용하지 않는다.
기존 `k8s-staging-mongo`는 Mongo 통합 테스트에서 계속 필요하다. 해당 테스트의 Kubernetes 권한은 유지한다.

## 담당자의 승인 전 확인

1. devoops-manifests staging digest가 이번 이미지인지 확인한다.
2. staging Argo CD에서 해당 변경의 동기화·배포 결과와 Rollout·Pod 상태를 확인한다.
3. staging 로그인·핵심 기능을 직접 검사한다. 알림이 없다는 것만으로 성공으로 판단하지 않는다.
4. 성공을 확인한 뒤 같은 소스 내용의 develop → main 릴리스 CR을 승인·머지한다.

Jenkins develop 성공은 매니페스트 갱신·승격용 태그 발행까지다. 실제 staging 배포 성공을 뜻하지 않는다.
`tree-*` 태그도 자동 배포·API 검증 통과의 증거가 아니다.
main 성공은 목표 digest의 운영 배포 완료·전체 backend Pod Ready·운영 HTTP 확인까지다.
새 태그 조회 방식은 이 Jenkinsfile을 포함한 develop이 main에 병합된 뒤 적용된다.

## 변경 범위

Argo CD·Rollouts의 배포·알림 설정은 변경하지 않는다. staging 배포와 실제 기능은 사람이 확인한다.
이 변경으로 실제 Jenkins 빌드나 staging/prod 배포 성공을 검증한 것은 아니다.

## Stage View 이름

Stage 이름의 대괄호는 실행 브랜치만 표시한다.
`[develop]` 단계는 develop에서만, `[main]`는 main에서만 실행한다.
공통 단계와 매니페스트 갱신은 `[develop·main]`으로 표시한다.

- 이미지 업로드: `<빌드번호>-<커밋 앞 12자>` 이미지 업로드
- 태그 발행: staging 매니페스트 갱신 뒤 `tree-<내용해시 앞 12자>` 발행
- 이미지 조회: main에서 같은 소스 내용의 기존 이미지 조회
- 매니페스트 갱신: devoops-manifests main의 해당 환경 kustomization에 불변 digest 반영

현재 빌드의 브랜치·대상 환경·내용 해시는 Jenkins 빌드 설명에서 확인한다.
실제 이미지 태그와 digest는 각 GHCR 단계의 로그에서 확인한다.

## 이미지 기동 검사와 운영 헬스체크

develop: 이미지 업로드 → 이미지 기동 검사 → staging 매니페스트 갱신 → 태그 발행.
main: 이미지 조회 → prod 매니페스트 갱신 → 목표 digest 배포 완료 → 운영 HTTP 확인.

배포 전 `scripts/check-image-startup.py`는 `.deploy-digest`의 정확한 불변 이미지를 사용한다.
Docker internal 네트워크에 임시 MongoDB 8.0 replica set과 Python 3.13 OpenStack 인증 대체 서버를
준비한다. 실제 운영 Mongo·OpenStack·Guacamole 자격증명을 사용하지 않으며 외부 포트를 열거나
호스트 경로를 마운트하지 않는다. 관리자·키는 임시 값이며 DB와 함께 폐기한다.
새 서버의 `/healthz` JSON 200·status ok(Mongo ping 포함), 미인증 `/api/me` JSON 401을
3회 연속 확인한다. 실패하면 staging 매니페스트 갱신·승격용 태그 발행 단계에 도달하지 않는다.
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
`python3 scripts/test-prod-deployment.py`(운영 접근 없이 모의 검증).
