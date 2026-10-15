# Jenkins 최종 Discord 알림 (#128)

백엔드 `Jenkinsfile`의 최종 `post { always { ... } }` 한 곳에서만 알림을 전송한다.
`scripts/notify-discord.py`는 Python 표준 라이브러리만 사용한다. 별도 Discord 플러그인은 필요 없다.

## Jenkins 설정

Manage Jenkins → Credentials → System → Global credentials에서 다음 값을 사용한다.

| 항목 | 값 |
| --- | --- |
| Kind | Secret text |
| ID | `discord-ci-webhook` |
| Secret | CI/CD 채널 Discord 웹훅 URL |

기존 Credential이 있으면 재등록하지 않는다. 원본 웹훅은 기존 운영 절차의 Vault
`devoops/ops/discord-webhook` 또는 Operation 서버의 관리 파일에서 확보하며, Git·이슈·로그에 붙여넣지 않는다.
모니터링과 채널을 공유하더라도 메시지는 `[CI/CD]` 말머리로 구분한다.
실행 노드에는 Python 3와 Discord HTTPS 통신 경로가 필요하다.
Manage Jenkins → System → Jenkins URL은 수신자가 접근할 수 있는 주소로 설정해야 빌드 링크를 열 수 있다.

## 메시지 내용

- 성공·실패·불안정·중단, 저장소, 빌드 번호, 브랜치 → staging/prod
- Jenkins 빌드 링크, 소요 시간, KST 알림 시각
- 이미지 digest 앞 12자리, tree 내용 해시, 커밋 요약
- 성공: `staging 반영:` 또는 `prod 반영:` 결과 줄. Argo CD Healthy를 확인했다는 뜻은 아니다.
- 실패: 마지막 실행 Stage와 알림 이전 로그 마지막 25줄 및 오류 줄(길이 제한 시 일부 생략)

실행하지 않은 skipped Stage는 실패 단계로 기록하지 않는다. 이미지 발행 이전 실패는 digest를
미확정으로 표시하고 이전 빌드의 `.deploy-digest`를 사용하지 않는다.
Credential은 환경 변수로만 전달하고 shell tracing을 끈다. 웹훅·토큰 형태·URL 비밀번호를 추가 마스킹하며
로그는 Jenkins Credential 마스킹이 이미 적용된 빌드 로그를 사용한다. @everyone 등 멘션은 차단한다.

현재 built-in 노드에서는 `JENKINS_HOME/jobs/.../builds/<번호>/log`에서 로그를 읽는다.
별도 원격 agent, 다른 로그 저장 플러그인 등으로 해당 파일이 없으면 실패 단계·빌드 링크는 전송하되
로그를 읽을 수 없다는 안내를 넣는다. 민감정보를 빌드 명령 자체에서 출력하면 안 된다.
최초 checkout 실패로 helper 파일을 받지 못하면 알림 대신 Console Output에 생략 안내가 남을 수 있다.

전송은 한 번만 시도하고 웹훅 주소·응답 본문은 출력하지 않는다. HTTP 204/200은 전달 성공이며,
Credential 누락·HTTP 오류·네트워크 실패는 Console Output에 남기되 원래 빌드 결과를 바꾸지 않는다.
실패 코드가 알림 성공으로 바뀌거나 중복 전송되지 않도록 자동 재시도는 하지 않는다.

## 검증 범위와 확인 방법

이번 변경에서는 실제 Jenkins 빌드, 실제 Discord 전송, 운영 배포를 실행하지 않는다.
로컬 알림 형식·마스킹 테스트만 다음 명령으로 실행한다.

```bash
python3 scripts/test-notify-discord.py
```

이후 담당자가 backend-multibranch의 develop 성공·의도된 실패·main prod 반영 실행에서
Console Output `Discord 알림 HTTP 204`(또는 200)와 실제 채널 메시지의 결과·단계·링크·시간을 확인한다.
main 알림 검증을 위해 승인 없이 prod 배포를 실행하지 않는다. 확인 결과는 이슈 #128에 기록한다.

프론트는 별도 저장소의 Jenkinsfile/notifyDiscord 구현이므로 이 백엔드 변경으로 자동 수정되지 않는다.
프론트 CR-100의 로그 보완과 공통 필드(링크·KST·digest) 반영은 별도 작업이다.
