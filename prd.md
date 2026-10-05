# PRD — Notion API 명세 기반 VDI 백엔드 구축

작성일: 2026-10-05
문서 상태: 구현 전 요구사항 및 구현 기준
기준 저장소: [hsoo3844/vdi-mock](https://github.com/hsoo3844/vdi-mock)
확인한 원본 main 커밋: 2345fceb9b46c2eecba341c8d1b49003df662337
계약 기준: [Notion API 명세서](https://app.notion.com/p/dc4b52ad1e3e83609bd881ce641fedd9)
추가 계약: [로그아웃 API](https://app.notion.com/p/3f0b52ad1e3e815386f1d3eab6716a58)

## Problem Statement

기존 저장소는 FastAPI·PostgreSQL·Kubernetes 기반 VDI 목업이다. 이름만으로 로그인하고 만료 없는 토큰을 사용하며, 데스크톱을 Kubernetes Pod로 생성한다. 이는 프로젝트에서 정한 Go·MongoDB·OpenStack·Guacamole 스택과 Notion의 이메일·비밀번호 인증 및 데스크톱 API 계약을 충족하지 않는다.

프로젝트에는 프론트 구현이 없으므로 기존 목업 화면에 대한 호환은 필요하지 않다. 사용자는 Notion 명세를 기준으로 백엔드만 빠르게 구축하고, 별도 서버의 OpenStack API를 통해 생성된 VM에 Guacamole으로 접속할 수 있게 하려 한다.

## Solution

Go·MongoDB 백엔드를 Kubernetes Deployment로 배포한다. 백엔드가 OpenStack 인증 후 Nova API를 호출해 Glance 이미지와 m1.micro Flavor로 VM을 생성·조회·삭제하고, 선택한 VM만 허용하는 Guacamole 접속 URL을 발급한다.

Notion의 기존 17개 API 전체를 구현하고, 서버 세션을 폐기하는 로그아웃 API 하나를 추가해 총 18개를 제공한다. 기존 API의 요청·응답·상태 코드는 유지한다. 생성과 삭제는 비동기로 접수하고 상태 조회로 진행 상황을 제공한다.

완료 기준은 실제 OpenStack·Guacamole 연동 코드 구현 및 자동 테스트 통과다. 실제 인프라에서의 VM 생성·RDP 접속·삭제 검증은 별도 단계다.

## User Stories

1. As a 사용자, I want 이메일과 비밀번호로 로그인하고 싶다, so that 사전 등록된 계정으로 서비스를 이용할 수 있다.
2. As a 사용자, I want 잘못된 인증 정보에 일관된 오류를 받고 싶다, so that 계정 존재 여부를 노출하지 않고 재입력할 수 있다.
3. As a 사용자, I want 로그인 세션의 만료 시각을 알고 싶다, so that 재로그인이 필요한 시점을 확인할 수 있다.
4. As a 사용자, I want 내 사용자 정보를 조회하고 싶다, so that 이름·이메일·역할을 확인할 수 있다.
5. As a 사용자, I want 현재 로그인 세션을 로그아웃하고 싶다, so that 현재 토큰이 서버에서도 더 이상 사용되지 않게 할 수 있다.
6. As a 사용자, I want 로그아웃해도 다른 기기의 세션은 유지하고 싶다, so that 현재 접속만 종료할 수 있다.
7. As a 사용자, I want 생성 가능한 OS 목록을 조회하고 싶다, so that 준비된 이미지 중에서 선택할 수 있다.
8. As a 사용자, I want Ubuntu·Windows·Rocky·Debian을 선택할 수 있고 싶다, so that 필요한 OS 환경을 사용할 수 있다.
9. As a 사용자, I want 이름과 OS 및 허용 사양으로 데스크톱을 생성하고 싶다, so that OpenStack VM을 할당받을 수 있다.
10. As a 사용자, I want 생성 요청 접수를 바로 확인하고 싶다, so that VM 준비가 끝날 때까지 HTTP 요청을 대기하지 않아도 된다.
11. As a 사용자, I want 동일한 생성 요청을 재전송해도 VM이 중복 생성되지 않게 하고 싶다, so that 네트워크 재시도에 안전하게 대응할 수 있다.
12. As a 사용자, I want 최대 2대의 생성 한도를 안내받고 싶다, so that 자원 사용 규칙을 이해할 수 있다.
13. As a 사용자, I want 내가 소유한 데스크톱만 조회하고 싶다, so that 내 자원과 상태를 확인할 수 있다.
14. As a 사용자, I want 생성 중과 RDP 준비 중을 구분하고 싶다, so that 실제 접속 가능 여부를 알 수 있다.
15. As a 사용자, I want 데스크톱 단건 상태를 조회하고 싶다, so that 생성·준비·실패·삭제 진행을 확인할 수 있다.
16. As a 사용자, I want 실패 이유와 재시도 가능 여부를 확인하고 싶다, so that 다음 행동을 결정할 수 있다.
17. As a 사용자, I want 생성 실패 VM이 자동 정리되길 원한다, so that 실패한 자원 때문에 생성 한도를 계속 차지하지 않게 할 수 있다.
18. As a 사용자, I want 준비된 데스크톱의 Guacamole URL을 받고 싶다, so that 프론트에서 해당 VM을 열 수 있다.
19. As a 사용자, I want 내 VM 하나에만 접속 권한을 받고 싶다, so that 타인의 VM에는 접근할 수 없게 할 수 있다.
20. As a 사용자, I want 접속이 끊겼을 때 URL을 다시 요청하고 싶다, so that 준비된 VM에 재연결할 수 있다.
21. As a 사용자, I want 데스크톱을 삭제하고 싶다, so that 사용하지 않는 VM과 루트 디스크를 회수할 수 있다.
22. As a 사용자, I want 삭제 접수와 삭제 완료를 구분하고 싶다, so that 자원 회수 진행을 확인할 수 있다.
23. As a 관리자, I want 사용자와 데스크톱 집계를 조회하고 싶다, so that 서비스 운영 현황을 파악할 수 있다.
24. As a 관리자, I want 사용자 목록과 소유 데스크톱 수를 조회하고 싶다, so that 자원 할당을 관리할 수 있다.
25. As a 관리자, I want 사용자를 추가하고 싶다, so that 별도 공개 회원가입 없이 계정을 제공할 수 있다.
26. As a 관리자, I want 사용자 이름·이메일·역할을 변경하고 싶다, so that 사용자 정보를 관리할 수 있다.
27. As a 관리자, I want 사용자 삭제 시 세션을 폐기하고 소유 VM을 회수하고 싶다, so that 삭제된 계정이 서비스를 이용하지 못하게 할 수 있다.
28. As a 관리자, I want 전체 또는 특정 사용자의 데스크톱을 조회하고 싶다, so that 사용자별 상태를 확인할 수 있다.
29. As a 관리자, I want 특정 사용자에게 데스크톱을 생성·할당하고 싶다, so that 사용자를 대신해 환경을 제공할 수 있다.
30. As a 관리자, I want 데스크톱을 강제로 회수하고 싶다, so that 운영상 불필요한 자원을 정리할 수 있다.
31. As a 관리자, I want 감사 이벤트를 필터링하고 싶다, so that 로그인·로그아웃·자원 변경과 실패 이력을 확인할 수 있다.
32. As a 운영자, I want Deployment 재시작 후 접수된 작업이 복구되길 원한다, so that 생성·삭제 요청이 유실되지 않게 할 수 있다.
33. As a 운영자, I want 설정과 Secret으로 외부 시스템을 연결하고 싶다, so that 인증정보를 API 응답이나 로그에 노출하지 않고 배포할 수 있다.
34. As a 구현자, I want 실제 외부 시스템 없이 API 계약을 자동 검증하고 싶다, so that 빠르게 구현하고 반복적으로 회귀를 확인할 수 있다.

## Implementation Decisions

### 범위와 기준

- 백엔드만 재구축하며 기존 프론트 호환 계층은 만들지 않는다.
- 노션 계약의 경로·필드·타입·상태 코드·필터를 기준으로 구현한다. 실제 명세의 예시 값은 환경 설정이나 상수로 오인하지 않는다.
- 이전에 제안했던 OS별 CPU·메모리·디스크 표는 폐기한다. 사양 정책은 모든 OS에 Nova m1.micro를 사용하는 것으로 확정했다.
- Cinder 볼륨 보존에 관한 중간 논의는 취소됐다. Glance 이미지 부팅과 Nova Flavor 루트 디스크를 사용한다.
- 준비 여부를 확인하는 내부 작업은 백엔드의 비동기 처리다. OpenStack 서버에 별도 작업자를 설치하는 요구사항이 아니다.

### API 목록

| 번호 | Method | 경로 | 기능 | 성공 |
|---|---|---|---|---|
| 1 | POST | /api/auth/login | 로그인 | 200 |
| 2 | POST | /api/auth/logout | 현재 세션 로그아웃 | 204 |
| 3 | GET | /api/me | 내 사용자 정보 | 200 |
| 4 | GET | /api/images | 선택 가능한 OS 목록 | 200 |
| 5 | GET | /api/desktops | 내 데스크톱 목록 | 200 |
| 6 | POST | /api/desktops | 데스크톱 생성 접수 | 202 |
| 7 | GET | /api/desktops/{desktopId} | 단건·상태 조회 | 200 |
| 8 | DELETE | /api/desktops/{desktopId} | 삭제·반납 접수 | 202 |
| 9 | POST | /api/desktops/{desktopId}/connect | Guacamole 접속 URL 발급 | 200 |
| 10 | GET | /api/admin/summary | 관리자 요약 | 200 |
| 11 | GET | /api/admin/users | 사용자 목록 | 200 |
| 12 | POST | /api/admin/users | 사용자 추가 | 201 |
| 13 | PATCH | /api/admin/users/{userId} | 사용자 정보 변경 | 200 |
| 14 | DELETE | /api/admin/users/{userId} | 사용자 삭제·자원 회수 접수 | 200 |
| 15 | GET | /api/admin/desktops | 전체·사용자별 데스크톱 목록 | 200 |
| 16 | POST | /api/admin/desktops | 특정 사용자에게 신규 VM 할당 | 202 |
| 17 | DELETE | /api/admin/desktops/{desktopId} | 강제 회수 접수 | 202 |
| 18 | GET | /api/admin/events | 감사 이벤트 조회 | 200 |

- 모든 경로는 /api를 유지한다. JSON은 camelCase이고, 성공 객체·배열을 직접 반환하며 data 래퍼와 새 페이지네이션 계약을 추가하지 않는다.
- 로그인 이외의 API는 Bearer 인증이 필요하다. 관리자 경로는 ADMIN 권한을 요구한다.
- 날짜는 RFC 3339 UTC 문자열, 공개 ID는 JavaScript 안전 정수 범위의 양의 정수, nullable 필드는 null, 빈 목록은 빈 배열로 반환한다.
- 오류는 code·message 문자열과 details 배열로 통일한다. 검증 오류의 각 details 항목은 field·message 문자열을 갖는다.
- 일반 사용자의 타인 데스크톱 상세·접속·삭제는 404로 거부한다. 관리자 목록 조회가 타인 VM 접속 권한을 부여하지 않는다.
- VM IP·RDP 비밀번호·OpenStack 인증정보·비밀번호 해시·내부 imageId·vmId는 일반 DTO에 넣지 않는다.

### 입력과 응답의 핵심 계약

- 로그인 입력은 email·password이며 응답은 token·expiresAt·user다. username 또는 admin 로그인 별칭은 추가하지 않는다.
- 일반 생성 입력은 name·osId·cpuCores·memoryGb·storageGb이고 모든 필드가 필수다. userId는 인증 세션에서 결정하며 요청에 받지 않는다.
- 관리자 신규 할당은 일반 생성 입력에 userId를 추가한다. 기존 VM의 소유자를 이전하는 API가 아니다.
- DesktopResponse는 id·userId·name·osId·cpuCores·memoryGb·storageGb·os·status·nodeName·connectionState·canConnect·failure·createdAt·updatedAt를 갖는다. failure는 null 또는 code·message·retryable 객체다.
- 데스크톱 삭제 접수 응답은 id·status이며 status는 DELETING이다.
- 접속 응답은 url·expiresAt다. 이 응답은 생성 응답과 분리한다.
- 사용자 삭제는 userId·reclaimedDesktopIds를 반환한다. reclaimedDesktopIds는 외부 삭제 요청을 접수한 자원 목록이며 완료 목록이 아니다.
- 사용자 정보 변경은 Notion 상세 설명대로 name·email·role의 전달된 필드만 수정한다. 최소 한 필드가 필요하고 null을 허용하지 않는다. quota·disabled·password 변경은 포함하지 않는다. 현재 상세 페이지의 필드 표에 email이 빠져 있으므로 본문 설명을 기준으로 적용한다.
- 관리자 데스크톱 목록은 선택적 userId 필터를, 이벤트 조회는 limit·actorId·desktopId·action·targetType 필터를 지원한다. 이벤트 limit 기본 100, 허용 범위 1~500과 createdAt 내림차순을 적용한다.

### 인증과 로그아웃

- MongoDB 기반 서버 세션에 연결된 암호학적 무작위 불투명 토큰을 발급한다. 서버에는 토큰 원문 대신 해시를 저장한다.
- 세션은 발급 후 8시간 고정 만료한다. 자동 갱신은 제공하지 않는다. MongoDB TTL 정리 지연과 무관하게 요청마다 만료 시각을 검사한다.
- 비밀번호는 해시로 검증·저장한다. 존재하지 않는 계정 자동 가입은 하지 않는다.
- 초기 관리자 이름은 admin, 로그인 이메일은 admin@example.com, 초기 비밀번호는 secret이다. 공개 사용자 id는 문자열 admin이 아닌 정수다.
- 초기 관리자 정보는 Kubernetes Secret으로 주입해 최초 한 번 생성한다. 재시작 시 기존 계정의 비밀번호를 초기값으로 덮어쓰지 않는다. 이후 사용자 추가는 관리자 API로 처리한다.
- 역할 변경과 사용자 삭제 시 해당 사용자의 기존 서버 세션을 폐기한다.

로그아웃의 상세 계약:

- POST /api/auth/logout은 Authorization 헤더만 필요하며 경로·쿼리·요청 본문은 없다.
- 현재 토큰에 해당하는 세션만 폐기한다. 다른 기기·브라우저 세션은 유지한다.
- 성공은 204 No Content이며 빈 객체나 null을 반환하지 않는다.
- 토큰 누락·무효·만료·이미 폐기된 세션은 401 UNAUTHORIZED, 내부 오류는 500 INTERNAL_ERROR로 응답한다. 공통 오류 DTO를 사용한다.
- 세션 폐기와 LOGOUT 이벤트 기록은 원자적으로 처리한다. actorId는 인증 사용자 ID, desktopId는 null, targetType은 USER다.
- 폐기된 토큰으로 보호 API 또는 로그아웃을 다시 호출하면 401이다.
- 로그아웃은 VM 중지·삭제, 이미 발급한 Guacamole 인증정보 폐기, 이미 열린 Guacamole 연결 종료를 의미하지 않는다.

### OpenStack·Glance·Nova 연동

- Go 백엔드는 Kubernetes Deployment에서 실행되고 별도 OpenStack 서버의 API를 호출한다.
- OpenStack의 인증 토큰을 사용해 Nova API를 호출한다. 인증 갱신·타임아웃·오류 변환은 연동 모듈에서 처리한다.
- OS 타입은 UBUNTU·WINDOWS·ROCKY·DEBIAN을 지원한다. 각 OS의 실제 버전·Glance 이미지 ID는 배포 설정으로 등록하며 준비되지 않은 이미지를 가용 목록으로 표시하지 않는다.
- Flavor는 m1.micro 하나로 고정한다. 이름으로 CPU·RAM·디스크 숫자를 추정하지 않고 실제 Flavor ID와 속성을 조회한다.
- 요청의 cpuCores·memoryGb·storageGb를 무시하지 않는다. 선택 이미지와 m1.micro의 실제 허용 조합과 일치하는지 검증한다.
- Flavor의 RAM을 명세의 양의 정수 GB로 정확히 표현할 수 없거나 이미지 요구사항을 충족하지 못하면 SPEC_UNAVAILABLE 또는 OS_UNAVAILABLE로 거부한다. 허위 사양을 응답하거나 클라이언트 값을 조용히 대체하지 않는다.
- Flavor 루트 디스크가 0인 경우 원본 이미지 크기를 사용하는 Nova 동작을 고려한다. 이미지의 실제 가상 크기를 조회해 명세의 GB 값으로 표현할 수 있는지 확인하며, 결정할 정보가 없으면 해당 조합을 가용하지 않다고 처리한다. min_disk는 최소 요구 조건이며 실제 디스크 크기로 대신 쓰지 않는다.
- 지정된 이미지·Flavor·네트워크·보안 그룹으로 Nova 생성 요청을 보낸다. 내부 VM metadata에 서비스 데스크톱 식별자를 넣어 재시작·타임아웃 후 기존 자원을 추적한다.
- Cinder 및 추가 볼륨을 만들지 않는다. VM 삭제는 해당 VM 루트 디스크의 삭제를 수반하고 원본 Glance 이미지는 유지한다.
- RDP가 준비된 이미지, VM까지의 네트워크 경로, RDP 허용 보안 그룹은 외부 인프라 전제조건이다. 백엔드가 이미지 제작이나 RDP 설치를 담당하지 않는다.

### 생성·상태·실패·삭제

- 요청 접수와 작업 상태를 MongoDB에 저장한 뒤 명세의 202 응답을 반환한다. 내부 작업자가 Nova 생성 요청을 수행한다.
- Nova 생성 응답에서 받은 vmId를 저장한다. 생성 응답은 VM 실행·RDP 준비 완료와 구분한다.
- 상태는 CREATING/PENDING → RUNNING/PENDING → RUNNING/READY로 진행한다. DesktopStatus에 READY를 추가하지 않는다.
- canConnect는 VM 실행 상태와 RDP 준비 상태를 반영한 스냅샷이다. connect 요청에서는 권한·VM 상태·RDP 준비를 다시 검증한다.
- 실패는 ERROR/UNAVAILABLE과 failure에 기록하고 해당 EventType을 남긴다.
- 사용자가 보유할 수 있는 VM은 최대 2대이며 관리자 신규 할당도 같은 한도를 적용한다. 동시 생성 요청에서도 한도를 넘지 않게 원자적으로 검사·예약한다.
- 생성 중, 삭제 미완료, 외부 정리 실패 VM은 한도에 포함한다. VM이 정리된 실패 기록 자체는 한도를 계속 차지하지 않는다.
- 생성 실패 VM은 자동 삭제한다. 정리 완료가 확인된 후 한도를 반환하고 실패 기록을 유지한다.
- 사용자 재시도는 같은 POST 경로에 새 Idempotency-Key를 보내는 신규 생성 요청이다. 실패한 기존 VM의 정리 여부를 먼저 확인한다.
- 삭제 접수는 DELETING/UNAVAILABLE로 표시한다. Nova에서 VM 삭제 완료를 확인한 후 목록에서 제외하며 단건 조회는 404로 응답한다.
- 삭제 실패는 ERROR/failure와 DESKTOP_DELETE_FAILED로 기록한다. 작업자가 정리를 재처리하며 조회 GET은 자원을 삭제하거나 롤백하지 않는다.

### Guacamole

- 기존 목업의 암호화 JSON 인증 방식을 Go로 구현한다. 선택한 VM 연결 하나만 포함한다.
- Guacamole JSON 인증 확장과 공유키 설정은 외부 Guacamole의 준비 조건이다. 별도 Guacamole 사용자·연결 DB 관리 기능은 구현하지 않는다.
- OS별 RDP 인증정보와 Guacamole 공유키는 Secret으로 주입한다. 인증정보가 포함된 JSON은 Guacamole 공식 형식으로 서명·암호화한다.
- 만료된 접속 URL은 새 접속에 사용할 수 없으며 재연결은 connect API를 다시 호출한다. URL 사용기한은 이미 열린 접속의 수명과 구분한다.
- 로그아웃 또는 사용자 세션 폐기만으로 이미 열린 Guacamole 접속이 즉시 끊어진다고 보장하지 않는다. 사용자·VM 회수 완료 시 해당 VM에 대한 연결이 종료된다.
- 브라우저 창 닫기를 VM 중지 요청으로 해석하지 않는다.

### 영속성·감사·배포의 구현 기본값

아래는 합의한 요구사항을 구현하기 위해 채택한 기본값이다. 새 공개 API를 추가하지 않고 설정 가능한 시간 값은 배포 설정으로 조정한다.

- 구성은 Go 표준 HTTP 서버, MongoDB 공식 드라이버, OpenStack Go SDK로 한다. HTTP·업무 규칙·저장·외부 연동 경계를 나누되 과도한 계층을 만들지 않는다.
- API와 비동기 작업은 같은 Go 프로세스에서 실행한다. Deployment 최초 replica는 1이며 Redis나 별도 메시지 브로커는 추가하지 않는다.
- MongoDB에 사용자·OS·데스크톱·세션·이벤트·멱등 요청·작업 상태를 저장한다. ObjectId는 내부용이며 공개 정수 ID는 원자적 카운터로 발급한다.
- 소유권 userId, 내부 vmId, connectionState, failure 및 내부 삭제·정리 상태를 보완한다. 내부 필드는 계약 DTO에 임의 노출하지 않는다.
- 사용자·멱등 키 unique index, 이메일 unique index, 소유권·상태·이벤트 조회 index, 세션·멱등 기록 TTL index를 구성한다.
- 한도 예약·요청 접수·감사 기록의 원자성은 MongoDB 트랜잭션으로 확보한다. 실제 및 테스트 MongoDB는 replica set을 전제로 한다.
- 작업을 원자적으로 선점하고 진행 상태를 기록한다. 재시작·배포 중 중복 실행에도 외부 자원 정리와 상태 갱신이 안전하도록 처리한다.
- Nova 생성 응답을 받지 못해 결과가 불확실한 경우 VM metadata로 기존 생성 결과를 확인한다. 존재 여부를 판단할 수 없으면 생성 재호출을 중단하고 복구 상태를 유지해 중복 VM 생성을 막는다.
- Idempotency-Key는 사용자·키 기준으로 24시간 보존한다. 최초 접수 응답을 보관해 같은 키·본문 재전송에는 동일 응답을 반환한다. 다른 본문은 409 IDEMPOTENCY_CONFLICT다.
- VM·RDP 준비 제한시간은 15분, 상태 확인 간격은 5초다. 개별 외부 호출에도 유한 타임아웃을 적용한다.
- Guacamole 접속 URL 사용기한은 2분이다.
- 관리자 자기 삭제 및 마지막 관리자 삭제·강등을 403으로 거부한다. 동시 변경에도 마지막 관리자 보호가 유지되도록 처리한다.
- 삭제된 사용자와 데스크톱은 공개 조회·집계에서 제외한다. 내부 삭제 기록·감사 이벤트는 보존하고 공개 이벤트 참조 ID를 변경하지 않는다.
- 시스템 이벤트에는 로그인 불가능한 내부 사용자 주체의 양의 정수 ID를 사용한다. 이 주체는 사용자 목록·사용자 집계·관리자 수정 대상에서 제외하고 새 역할 enum을 추가하지 않는다.
- EventAction/Target 참조 이름은 기존 스키마에 정의된 EventType/TargetType으로 통일한다. 원문에 없는 사용자 CRUD 이벤트 enum을 추가하지 않는다.
- 배포 산출물은 컨테이너 이미지 빌드 설정, Kubernetes Deployment·Service·ConfigMap·Secret 예시 및 설정 안내다. 인증정보·RDP 비밀번호·접속 URL payload는 로그에 기록하지 않는다.

## Testing Decisions

### 검증 경계와 기준

HTTP API를 가장 높은 주 검증 경계로 삼는다. 요청을 보내고 응답 및 이후 조회 결과를 확인해 외부 동작을 검증한다. 내부 함수 호출 횟수나 구현 구조를 그대로 따라 쓰는 테스트는 만들지 않는다.

실제 MongoDB replica set과 OpenStack HTTP 대체 서버를 사용한다. 외부 의존성을 대체하되 저장·트랜잭션·unique index 동작은 실제 DB로 검증한다. Guacamole payload는 복호화·서명 검증을 통해 실제 형식과 선택 VM 범위를 검사한다. 타임아웃 테스트는 제어 가능한 시간 설정으로 긴 대기 없이 수행한다.

기존 저장소에는 배포 환경에서 로그인·VM 생성·준비·Guacamole·한도·관리자 흐름을 검증하는 E2E 스크립트가 있다. 이 사용자 흐름을 참고하되 Python 목업의 DTO·Kubernetes 전제를 복사하지 않는다. 기존 OpenStack용 테스트 경계는 없으므로 새 HTTP 대체 서버를 사용한다.

### 필수 테스트 시나리오

1. 18개 API의 Method·경로·입력·응답 필드·타입·상태 코드가 Notion 계약과 일치한다.
2. camelCase, 정수 ID, UTC 시간, nullable, 빈 배열, 공통 오류 DTO를 검증한다.
3. 초기 관리자 생성 및 재시작 시 기존 비밀번호를 덮어쓰지 않는 동작을 검증한다.
4. 올바른 이메일·비밀번호 로그인, 입력 검증, 계정 미존재·비밀번호 오류의 동일 응답을 검증한다.
5. 토큰 만료 시각 경계와 DB TTL 정리 지연에도 8시간 만료가 적용되는지 검증한다.
6. 로그아웃 204의 본문이 비어 있고 현재 세션만 폐기되는지 확인한다.
7. 로그아웃 후 보호 API 및 재로그아웃이 401이고 다른 로그인 세션은 유지되는지 확인한다.
8. LOGOUT 이벤트와 세션 폐기의 원자성, 토큰 비노출, VM·Guacamole에 대한 불필요한 작업이 없는지 확인한다.
9. 사용자·관리자 권한, 타인 자원 404, 관리자 목록 조회와 접속 권한 분리를 검증한다.
10. 등록 이미지의 가용성, 내부 imageId 비노출, m1.micro 사양 매핑 및 호환되지 않는 요청 거부를 검증한다.
11. 동시에 생성하더라도 사용자당 2대를 넘지 않으며 관리자 할당에도 한도가 적용되는지 확인한다.
12. 같은 사용자·키·본문 중복 접수는 동일 응답·하나의 VM 생성이고 다른 본문은 409인지 확인한다.
13. 같은 키를 서로 다른 사용자가 사용하는 경우 요청이 서로 섞이지 않는지 확인한다.
14. 202 이후 상태가 생성 중·RDP 준비 중·사용 가능으로 진행되는지 확인한다.
15. Nova 실행 상태만으로 canConnect가 true가 되지 않으며 connect가 준비 상태를 재검증하는지 확인한다.
16. Nova 생성 실패·응답 유실·RDP 타임아웃의 failure와 실패 이벤트·자원 정리를 검증한다.
17. 실패 자원 정리 완료 후 한도를 반환하고 정리 실패 시에는 반환하지 않는지 확인한다.
18. Deployment 재시작과 작업 중복 선점 상황에서 접수 작업 복구 및 중복 VM 방지를 검증한다.
19. Guacamole 서명·암호화·2분 만료·선택 VM 하나만 포함·민감정보 비노출을 확인한다.
20. VM 삭제 접수 202, 완료 전 DELETING, 완료 후 목록 제외·단건 404를 검증한다.
21. 삭제 실패 기록·재처리, 사용자 삭제 시 세션 폐기와 소유 VM 회수를 검증한다.
22. 관리자 자기 삭제 및 마지막 관리자 삭제·강등 보호를 검증한다.
23. 관리자 요약·사용자 desktopCount·이벤트 필터·limit·정렬을 검증한다.
24. GET 요청이 외부 자원을 삭제하거나 상태 롤백을 수행하지 않는지 확인한다.

### 완료 조건

- 모든 API 계약 및 필수 시나리오 자동 테스트가 통과한다.
- 연동 구현에 성공을 고정 반환하는 목업 경로가 남아 있지 않다.
- 실제 인프라 연결에 필요한 설정과 Deployment 예시가 제공된다.
- 실제 OpenStack·Guacamole 서버를 호출한 E2E 검증은 완료 조건과 별도이며, 수행 전에는 실제 환경 검증 완료로 표시하지 않는다.

## Out of Scope

- 프론트 화면 구현, 기존 HTML/JS 프론트 계약 호환.
- OpenStack·Kubernetes·MongoDB·Guacamole 인프라 설치 및 실제 배포 실행.
- Glance 이미지 제작, 이미지 내부 RDP 설치·초기화.
- Cinder 볼륨 생성·보존·재사용, 추가 디스크 관리.
- 공개 회원가입, 비밀번호 변경·초기화 API, JWT·refresh token, 전체 세션 로그아웃 API.
- VM 시작·중지 API, 유휴 상태 판정·자동 반납, 브라우저 종료 시 VM 중지.
- 로그아웃 시 이미 열린 Guacamole 연결의 강제 종료와 발급된 Guacamole URL의 즉시 폐기.
- Prometheus API, DesktopMetric 수집·조회, 별도 메트릭 대시보드.
- 과금, 스냅샷·백업, 사용자별 가변 한도, 여러 Flavor 선택, API 페이지네이션.
- 기존 PostgreSQL·Pod 데이터의 운영 마이그레이션과 자동 CI/CD 파이프라인 구축.
- GitHub 이슈·PR 발행 및 백엔드 코드 구현은 이번 문서 작성 작업에 포함하지 않는다.

## Further Notes

- 이 PRD는 대화의 최종 결정을 반영한다. Deployment가 배포 단위이며 VM을 Kubernetes Pod로 생성하지 않는다.
- 기존 API 상세 계약은 Notion이 기준이며, 이 PRD에서 확정한 미정 정책과 추가 로그아웃 계약을 함께 적용한다.
- 일반 생성 응답과 Guacamole URL 발급은 별도 API다. Nova 생성 응답이 RDP 준비 완료를 의미하지 않는다.
- OpenStack URL·인증정보·프로젝트·네트워크·보안 그룹·4개 이미지의 실제 ID와 버전·m1.micro 속성·Guacamole URL·공유키·RDP 인증정보는 배포 시 제공해야 하는 환경 값이다. 이 문서는 실제 값이 확보됐다고 가정하지 않는다.
- 실제 m1.micro가 명세의 정수 GB 사양 및 4개 이미지와 호환되는지는 외부 환경 확인이 필요하다. 불일치를 임의 반올림이나 다른 Flavor 선택으로 숨기지 않는다.
- 현재 작업 공간에는 원본 소스가 체크아웃되어 있지 않다. 원본 이해는 위 main 커밋을 읽은 결과이며 이후 구현 전에 대상 저장소를 확보한다.
- 요청 범위에 맞춰 로컬 PRD를 작성한다. 이슈 트래커·triage 라벨은 설정되지 않았으므로 이슈 발행이 필요하면 /setup-matt-pocock-skills로 먼저 구성한다.
- 본 작업은 문서 작성·Notion 명세 추가이며, 백엔드 구현이나 실제 서버 테스트를 수행한 결과 보고서가 아니다.

