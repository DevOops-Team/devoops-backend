# staging 실제 배포·백엔드 API 검증

develop 흐름은 테스트 → 이미지 빌드·보안 검사·GHCR push → staging 매니페스트 반영 →
실제 배포·Pod·API 검증 → `verified-tree-<내용해시>` 태그 발행이다.
main은 이 검증 태그만 조회한다. 이전 `tree-<내용해시>` 태그로 대체하거나 재빌드하지 않는다.
검증 태그가 없으면 prod 매니페스트를 바꾸기 전에 실패한다.

## Jenkins Credential

| ID (기본값) | 타입 | 용도 |
| --- | --- | --- |
| `k8s-staging-mongo` | Secret file | staging 조회·백엔드 Pod port-forward 전용 kubeconfig |
| `staging-backend-smoke` | Username with password | staging 전용 USER 계정의 이메일 / 비밀번호 |

각 ID는 Jenkins 작업 파라미터 `STAGING_VERIFY_CREDENTIALS`, `STAGING_SMOKE_CREDENTIALS`로 지정한다.
기본값은 기존 통합 테스트 DB 터널용 `k8s-staging-mongo`를 재사용한다.
단, Mongo 접속 권한만으로는 부족하다. 아래 조회·port-forward 권한이 있는 경우에만
백엔드 배포 검증에도 사용할 수 있다. 스테이징 검증 권한 확인 단계가 각 권한의 yes/no를
출력하고 하나라도 부족하면 빌드 전에 중단한다. 실제 리소스 조회도 확인한다.
권한 부족 시 staging 관리자가 해당 계정에 RBAC를 추가하거나 별도 계정을 발급한다.
별도 파일을 Secret file `k8s-staging-backend-verify`로 등록했다면
Build with Parameters에서 `STAGING_VERIFY_CREDENTIALS=k8s-staging-backend-verify`로 지정한다.
기존 작업에 저장된 파라미터 값이 있다면 재사용하려는 ID로 직접 변경한다.
이 코드 변경이 클러스터 RBAC를 자동으로 추가하지는 않는다.
운영 admin kubeconfig를 사용하지 않는다. 검증 스크립트는 API 주소
`https://172.30.0.61:6443`와 namespace `vdi-staging`만 허용한다.

필요한 권한:

- `argocd`: `argoproj.io/applications`의 `portal-staging` get
- `vdi-staging`: `apps/deployments`의 `backend` get, `argoproj.io/rollouts`의 `backend` get
- `vdi-staging`: core Services의 `backend` get, Pods get/list/watch
- `vdi-staging`: core Pods/portforward create (kubectl 버전에 따라 get도 허용)
- `kubectl auth can-i` 확인을 위한 SelfSubjectAccessReview 생성 (일반 인증 사용자의 기본 권한)

`staging-backend-smoke` 계정은 관리자가 staging에 미리 만든 전용 USER여야 한다.
이 파이프라인은 계정이나 VM을 만들지 않는다. 로그인/로그아웃에 따른 세션·감사 이벤트만 생성하며
실제 사용자 비밀번호나 관리자 계정을 사용하지 않는다. 테스트 계정의 VM 목록은 조회만 한다.

## 검증 내용

1. 이미지 반영 후 infra main을 다시 읽어 staging digest가 이번 이미지와 같은지 확인하고 revision을 저장한다.
2. Argo CD `portal-staging`이 main을 추적하며 해당 revision에 대해 동기화·Healthy·작업 Succeeded인지 확인한다.
   `prune:false`의 정리 대기 리소스만 OutOfSync이면 인프라 기존 검증과 동일하게 허용한다.
3. backend Deployment의 이미지가 이번 digest인지, backend Rollout이 최신 자체 generation과 Deployment generation을 관찰했는지 확인한다. Rollout Healthy 및 updated/ready/available/전체 replica가 Rollout 목표 수와 같아야 한다. Deployment는 workloadRef 이관 후 0개일 수 있으므로 Deployment replica를 목표 Pod 수로 사용하지 않는다.
4. 삭제 중인 Pod를 제외한 모든 backend Pod가 같은 digest·Ready·Running인지 확인한다.
   containerd imageID는 멀티플랫폼 manifest와 다를 수 있으므로 이미지 일치 기준은 Pod 스펙의 불변 digest다.
5. 각 Pod에 임시 localhost 포트로 터널을 열어 다음 API를 검사하고 종료한다.
   - `/healthz`: 200, JSON `status: ok` (MongoDB ping 포함)
   - 미인증 `/api/me`: JSON 401
   - `POST /api/auth/login`: 전용 계정 로그인, 토큰은 메모리에만 보관
   - 인증된 `/api/me`: 계정 이메일·양의 ID·USER 역할 확인
   - 인증된 `/api/desktops`: JSON 배열 200
   - `POST /api/auth/logout`: 204, 기존 토큰으로 `/api/me` 재요청 시 401
6. API 검사 후 배포 상태를 다시 조회한다. 같을 때만 이번 이미지의 `.staging-verified`를 기록한다.
7. 태그 발행 단계가 마커와 digest를 다시 비교한 후 `verified-tree-...`를 push한다.

API 검증은 외부 프론트 프록시가 아니라 실제 새 backend Pod를 대상으로 한다. 외부 라우팅,
OpenStack VM 생성·삭제·RDP·Guacamole E2E 검증을 대신하지 않는다.
DB/인증/핵심 조회가 실패하거나 RBAC·클러스터 주소가 틀리면 승격 태그를 발행하지 않는다.
동시 인프라 변경으로 Argo CD가 저장한 revision을 건너뛰면 실패할 수 있으므로 최신 develop 빌드를 다시 실행한다.
main 승격은 이 검증 코드를 포함한 develop이 승인된 CR로 머지된 뒤 사용한다.

## 범위

이번 변경은 Jenkinsfile·검증 스크립트·문서다. Credential 발급/등록, RBAC 변경, 실제 Jenkins 빌드나
staging/prod 배포는 실행하지 않는다. Jenkins가 수정 커밋을 자동 감지하면 실제 빌드가 시작될 수 있다.
로컬에서는 `python3 scripts/test-verify-staging.py`로 모의 배포·API 검증을 실행한다.
