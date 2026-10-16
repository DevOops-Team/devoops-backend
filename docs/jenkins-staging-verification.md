# staging 확인과 prod 승격

## 현재 Jenkins 흐름

- develop: 정적·단위·Mongo 통합 테스트 → 이미지 빌드 → Trivy → GHCR 업로드 → 임시 컨테이너 기동 검사 →
  infra main의 staging digest 갱신 → 동일 digest의 `tree-<내용해시>` 태그 발행.
- main: 동일 내용의 tree 태그 조회 → 동일 digest로 prod 매니페스트 갱신 → 운영 HTTP 확인.
  새 이미지를 빌드하지 않는다. 대응 태그가 없으면 실패한다.

Jenkins는 Argo CD Application·Deployment·Rollout·Pod 상태나 staging API를 검사하지 않는다.
develop의 이미지 기동·main의 운영 HTTP 검사는 아래 별도 절차로 수행한다.
기존 `scripts/verify-staging.py`는 선택적 진단용으로 남겨두지만 Jenkinsfile에서는 호출하지 않는다.
검증용 STAGING_VERIFY_CREDENTIALS·STAGING_SMOKE_CREDENTIALS 파라미터도 사용하지 않는다.
기존 `k8s-staging-mongo`는 Mongo 통합 테스트에서 계속 필요하다. 해당 테스트의 Kubernetes 권한은 유지한다.

## 담당자의 승인 전 확인

1. infra staging digest가 이번 이미지인지 확인한다.
2. staging Argo CD에서 해당 변경의 동기화·배포 결과와 Rollout·Pod 상태를 확인한다.
3. staging 로그인·핵심 기능을 직접 검사한다. 알림이 없다는 것만으로 성공으로 판단하지 않는다.
4. 성공을 확인한 뒤 같은 소스 내용의 develop → main 릴리스 CR을 승인·머지한다.

Jenkins develop 성공은 매니페스트 갱신·승격용 태그 발행까지다. 실제 staging 배포 성공을 뜻하지 않는다.
`tree-*` 태그도 자동 배포·API 검증 통과의 증거가 아니다.
main 성공은 prod 매니페스트 갱신·운영 HTTP 확인까지다. 실제 prod 새 digest의 카나리 완료는 Argo CD에서 확인한다.
새 태그 조회 방식은 이 Jenkinsfile을 포함한 develop이 main에 병합된 뒤 적용된다.

## 변경 범위

Argo CD·Rollouts의 배포·알림 설정은 변경하지 않는다. 배포 여부는 사람이 확인한다.
이 변경으로 실제 Jenkins 빌드나 staging/prod 배포 성공을 검증한 것은 아니다.

## Stage View 이름

Stage 이름의 대괄호는 실행 브랜치만 표시한다.
`[develop]` 단계는 develop에서만, `[main]`는 main에서만 실행한다.
공통 단계와 매니페스트 갱신은 `[develop·main]`으로 표시한다.

- 이미지 업로드: `<빌드번호>-<커밋 앞 12자>` 이미지 업로드
- 태그 발행: staging 매니페스트 갱신 뒤 `tree-<내용해시 앞 12자>` 발행
- 이미지 조회: main에서 같은 소스 내용의 기존 이미지 조회
- 매니페스트 갱신: infra main의 해당 환경 kustomization에 불변 digest 반영

현재 빌드의 브랜치·대상 환경·내용 해시는 Jenkins 빌드 설명에서 확인한다.
실제 이미지 태그와 digest는 각 GHCR 단계의 로그에서 확인한다.

## 이미지 기동 검사와 운영 헬스체크

develop: 이미지 업로드 → 이미지 기동 검사 → staging 매니페스트 갱신 → 태그 발행.
main: 이미지 조회 → prod 매니페스트 갱신 → 운영 HTTP 확인.

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
요청당 10초 제한, 최대 30회 재시도, Jenkins 단계 제한 12분이다.

이 운영 HTTP 응답은 아직 이전 버전이 반환할 수 있다. Argo CD의 새 digest 배포 완료를
기다리거나 판정하는 검사가 아니다. 실패 시 prod 매니페스트는 이미 바뀌었을 수 있으며
자동 롤백하지 않는다. 담당자가 Argo CD·카나리·실제 배포 이미지를 확인해야 한다.
이 Jenkinsfile이 main에 승인·병합된 뒤 main 검사에 적용된다.

로컬 검사: `python3 scripts/test-main-health.py`(Docker·운영 HTTP 모의 테스트).
