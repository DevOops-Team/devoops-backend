# staging 확인과 prod 승격

## 현재 Jenkins 흐름

- develop: 정적·단위·Mongo 통합 테스트 → 이미지 빌드 → Trivy → GHCR 업로드 →
  infra main의 staging digest 갱신 → 동일 digest의 `tree-<내용해시>` 태그 발행.
- main: 동일 내용의 tree 태그 조회 → 동일 digest로 infra main의 prod 매니페스트 갱신.
  새 이미지를 빌드하지 않는다. 대응 태그가 없으면 실패한다.

Jenkins는 Argo CD Application·Deployment·Rollout·Pod 상태나 staging API를 검사하지 않는다.
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
main 성공은 prod 매니페스트 갱신까지이며 실제 prod 카나리 완료는 Argo CD에서 확인한다.
새 태그 조회 방식은 이 Jenkinsfile을 포함한 develop이 main에 병합된 뒤 적용된다.

## 변경 범위

Argo CD·Rollouts의 배포·알림 설정은 변경하지 않는다. 배포 여부는 사람이 확인한다.
이 변경으로 실제 Jenkins 빌드나 staging/prod 배포 성공을 검증한 것은 아니다.

## Stage View 이름

Stage 이름의 대괄호는 실행 브랜치와 배포 대상을 표시한다.
`[develop]` 단계는 develop에서만, `[main → prod]`는 main에서만 실행한다.
공통 단계는 `[develop·main]`, 매니페스트 갱신은 두 브랜치의 환경 대응을 함께 표시한다.

- GHCR 빌드 이미지 업로드: `<빌드번호>-<커밋 앞 12자>` 이미지 업로드
- GHCR tree 태그 발행: staging 매니페스트 갱신 뒤 `tree-<내용해시 앞 12자>` 발행
- GHCR tree 태그 조회: main에서 같은 소스 내용의 기존 이미지 조회
- 매니페스트 digest 갱신: infra main의 해당 환경 kustomization에 불변 digest 반영

현재 빌드의 브랜치·대상 환경·내용 해시는 Jenkins 빌드 설명에서 확인한다.
실제 이미지 태그와 digest는 각 GHCR 단계의 로그에서 확인한다.
