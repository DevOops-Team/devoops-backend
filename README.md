# devoops-backend

[prd.md](prd.md) 및 [Notion API 명세](https://app.notion.com/p/dc4b52ad1e3e83609bd881ce641fedd9)를 구현한 Go 백엔드입니다. 최초 구현 시 확인한 18개 상세 계약은 [docs/reference/notion-contracts.json](docs/reference/notion-contracts.json)에 보관했습니다. 기존 Python/Kubernetes Pod 목업을 런타임에 사용하지 않습니다.

Go 표준 HTTP 서버, MongoDB 공식 드라이버, Gophercloud OpenStack SDK를 사용합니다. API와 영속 작업자가 한 프로세스에서 실행됩니다. 공개 API는 기존 18개와 프론트 연동용 사양 조회 1개로 구성되며, 공개 DTO에 MongoDB 내부 필드·VM ID·IP·비밀번호를 노출하지 않습니다.

## 실행

전체 스택은 상위 디렉토리의 [docker-compose.yml](../docker-compose.yml)과 [.env.example](../.env.example)을 사용합니다. 실행 순서와 OpenStack 연결값은 [상위 README](../README.md)를 참고하세요.

```bash
cd ..
docker compose build
docker compose up -d --wait
```

백엔드는 Go API/작업자만 실행합니다. MongoDB 서버는 `../mongodb`, Guacamole 웹/guacd는 `../guacamole`에서 빌드합니다. `internal/vdi/store.go`의 DB 접근·트랜잭션과 `internal/guacamole`의 접속 URL 서명은 백엔드의 업무 코드이므로 유지합니다. 백엔드는 시작할 때 실제 Keystone에 인증하고 Nova/Glance 클라이언트를 구성하므로 OpenStack 연결값과 접근 가능한 서비스 카탈로그가 필요합니다.

`GET /healthz`는 HTTP 서버와 MongoDB 연결을 확인하며, VM 생성이나 원격 데스크톱 접속까지 확인하지 않습니다.

## 설정

| 환경 변수 | 의미 |
| --- | --- |
| `MONGO_URI`, `MONGO_DATABASE` | replica set 연결 URI, DB 이름(기본 `vdi`) |
| `HTTP_ADDR` | HTTP 바인딩 주소(기본 `:8080`) |
| `ADMIN_NAME`, `ADMIN_EMAIL`, `ADMIN_PASSWORD` | 최초 관리자 정보, Secret 필수 |
| `OS_AUTH_URL`, `OS_USERNAME`, `OS_PASSWORD` | Keystone v3 인증 |
| `OS_PROJECT_ID` 또는 `OS_PROJECT_NAME` | 인증 프로젝트 |
| `OS_USER_DOMAIN_NAME`, `OS_PROJECT_DOMAIN_NAME` | 로컬 Compose에서는 사용자 도메인을 SDK의 `OS_DOMAIN_NAME`에 전달합니다. 현재 두 도메인은 `Default`이며, 다른 프로젝트 도메인에서는 프로젝트 ID 기반 인증을 사용하세요. 직접 바이너리를 실행하면 `OS_DOMAIN_NAME`을 설정하세요. |
| `OS_REGION_NAME` | 서비스 카탈로그 리전 |
| `OS_NETWORK_ID`, `OS_SECURITY_GROUPS` | VM 네트워크 UUID, 쉼표로 구분한 보안 그룹 이름 |
| `VDI_SERVICE_ID` | metadata 자원 추적 범위, 기본 `devoops-vdi`; 운영 DB마다 고유하고 재시작 시 유지 |
| `RDP_CREDENTIALS_JSON` | OS 타입별 `username`, `password`, 선택 `domain` 객체 |
| `GUAC_URL` | 외부 Guacamole 절대 URL |
| `GUAC_JSON_KEY` | JSON 인증 확장과 동일한 AES-128 공유키, 32자리 hex |
| `EXTERNAL_TIMEOUT` | 개별 외부 호출 제한, 기본 `10s` |
| `POLL_INTERVAL` | 작업 재조회 간격, 기본 `5s` |
| `READY_TIMEOUT` | 생성·RDP 준비 제한, 기본 `15m` |
| `GUAC_URL_TTL` | 새 Guacamole URL 사용기한, 기본 `2m` |

OS 컬렉션이 비어 있으면 부트스트랩에서 Glance의 active 이미지 목록을 최초 한 번 가져와 저장합니다. `os_distro` 메타데이터가 `ubuntu`, `windows`, `rocky`, `debian` 중 하나이고 `os_version`이 있는 이미지만 등록합니다. 이름은 Glance 이미지 이름을 사용합니다. 기존 DB 모델은 OS 유형당 이미지 하나를 지원하므로 동일 유형에서는 생성 시각이 가장 최근인 유효 이미지를 선택하고, 생성 시각이 같으면 이미지 ID 오름차순으로 선택합니다. CirrOS와 메타데이터가 없는 이미지는 제외합니다.

성공한 초기 조회는 지원 이미지가 0개여도 `settings`의 `os-bootstrap` 문서로 완료를 기록합니다. 이후 재시작은 Glance 목록을 다시 가져오거나 기존 OS를 덮어쓰거나 삭제하지 않습니다. 기존 OS 컬렉션에 데이터가 있으면 그대로 보존하고 초기화 완료만 기록합니다. Glance 조회 실패 시 완료를 기록하지 않고 기동에 실패하여 다음 시작에서 재시도합니다. 여러 Pod가 동시에 조회할 수 있지만, 등록과 완료 기록은 MongoDB 트랜잭션에서 직렬화하므로 중복 저장되지 않습니다. 자동 주기 동기화는 하지 않습니다. 나중에 다시 가져오려면 백엔드를 중지한 뒤 OS 컬렉션과 `os-bootstrap` 완료 기록을 함께 초기화해야 합니다. 기존 데스크톱이 있는 DB에서는 OS ID 참조를 고려해야 합니다.

OS 목록 및 생성 시 실제 Glance 이미지와 `m1.micro`를 검사합니다. 등록된 이미지라도 호환 Flavor가 없으면 공개 목록에서 제외됩니다. OS 내부 `imageId`는 API JSON에서 제외합니다. OS별 RDP 계정은 기존 `RDP_CREDENTIALS_JSON` 방식을 유지하며 해당 정보가 없는 OS는 원격 접속할 수 없습니다.

Flavor RAM(MiB)은 1024의 배수여야 합니다. 루트 디스크가 0이면 Glance `virtual_size`(byte)가 양의 정수 GiB로 정확히 표현되어야 합니다. `min_disk`를 실제 크기로 대신 사용하지 않습니다. RAM/디스크를 반올림하지 않고 호환되지 않는 조합은 가용 목록에서 제외합니다. 생성 입력 사양이 실제 허용 사양과 다르면 400 검증 오류, 표현 불가능한 Flavor는 409 `SPEC_UNAVAILABLE`, 비가용 이미지는 409 `OS_UNAVAILABLE`입니다. 프론트 연동용 `GET /api/images/{osId}/spec`은 선택 OS의 실제 허용 사양을 반환합니다. 프론트 생성 폼이 이 값을 조회하며, 생성 시 서버에서 다시 검증합니다.

## API

로그인 외에는 `Authorization: Bearer <token>`이 필요합니다. 관리자 경로는 `ADMIN`만 사용할 수 있습니다. JSON은 camelCase, ID는 양의 JavaScript 안전 정수, 시간은 RFC3339 UTC, 빈 배열은 `[]`, nullable 값은 `null`입니다. 오류는 `{code,message,details:[{field,message}]}`입니다.

| Method | 경로 | 성공 |
| --- | --- | --- |
| POST | `/api/auth/login` | 200 |
| POST | `/api/auth/logout` | 204, 본문 없음 |
| GET | `/api/me` | 200 |
| GET | `/api/images` | 200 |
| GET | `/api/images/{osId}/spec` | 200 |
| GET / POST | `/api/desktops` | 200 / 202 |
| GET / DELETE | `/api/desktops/{desktopId}` | 200 / 202 |
| POST | `/api/desktops/{desktopId}/connect` | 200 |
| GET | `/api/admin/summary` | 200 |
| GET / POST | `/api/admin/users` | 200 / 201 |
| PATCH / DELETE | `/api/admin/users/{userId}` | 200 |
| GET / POST | `/api/admin/desktops` | 200 / 202 |
| DELETE | `/api/admin/desktops/{desktopId}` | 202 |
| GET | `/api/admin/events` | 200 |

로그인은 `email,password`, 사용자 추가는 `name,email,password,role`을 받습니다. 사용자 PATCH는 `name,email,role` 중 전달된 필드만 변경하며 null 및 빈 객체를 거부합니다. 생성 입력은 `name,osId,cpuCores,memoryGb,storageGb` 모두 필수이고 관리자 할당은 `userId`가 추가됩니다. 두 생성 API에는 UUID `Idempotency-Key`가 필수입니다.

관리자 데스크톱 목록만 `userId` 필터를 받습니다. 감사 이벤트는 `limit`(기본 100, 1~500), `actorId`, `desktopId`, `action`, `targetType`을 지원하며 생성 시각 내림차순입니다. 상세 enum과 DTO는 저장된 Notion 계약을 참고하세요.

`GET /api/images/{osId}/spec`은 `{osId,cpuCores,memoryGb,storageGb}`를 반환하며 이미지·Flavor 내부 ID는 노출하지 않습니다. 다른 API와 동일하게 Bearer 인증이 필요합니다.

## 저장·복구 동작

- 세션은 32byte 무작위 토큰의 SHA-256 해시만 저장합니다. 비밀번호는 bcrypt로 저장하며 입력은 1~72byte입니다. 요청마다 8시간 고정 만료를 검사합니다. 로그아웃은 현재 세션 삭제와 `LOGOUT` 이벤트를 같은 트랜잭션으로 저장합니다.
- 사용자당 최대 2개의 예약을 원자적으로 확보합니다. 관리자 할당도 같은 한도입니다. 생성·요청 원본 응답·24시간 멱등 기록·감사 이벤트를 하나의 MongoDB 트랜잭션으로 저장합니다. 재전송은 한도나 현재 인프라 상태가 바뀌어도 최초 응답을 재사용합니다.
- 작업 상태와 lease는 desktop 문서에 함께 저장합니다. 원자적 선점, lease 만료 복구, revision 조건 갱신으로 중복 작업과 API 삭제 경쟁을 처리합니다. 재시작 후 접수된 요청을 다시 조회합니다.
- Nova POST를 호출하기 **전에** 제출 상태를 저장합니다. 응답 유실이나 제출 직후 프로세스 종료는 서비스·desktop metadata로 복구합니다. 빈 조회만으로 미생성을 단정하지 않고 다시 POST하지 않습니다. 결과가 끝내 불확실하면 ERROR 기록과 예약을 유지하며 복구를 계속합니다. 운영자는 Nova 상태와 metadata를 확인해야 하며, 불확실 작업의 수동 해결용 공개 API는 제공하지 않습니다.
- 명확한 Nova 요청 거부는 실패를 기록하고 한도를 반환합니다. VM/RDP 준비 실패는 VM을 삭제한 뒤 실제 부재를 확인해야 한도를 반환합니다. 정리 실패 중 새 생성 재시도는 거부합니다. 중복 metadata 자원도 모두 정리합니다.
- VM 실행과 RDP 준비를 구분하고, RDP 프로토콜 협상으로 준비를 검사합니다. connect는 소유권·세션·Nova ACTIVE·RDP를 다시 검사합니다. 관리자 목록 조회로 타인 VM 접속 권한이 생기지 않습니다.
- Guacamole payload는 HMAC-SHA256 서명 후 zero-IV AES-128-CBC/PKCS7으로 암호화합니다. 선택한 VM 연결 하나만 포함하고 밀리초 epoch 만료를 넣습니다. RDP 인증정보는 공유키로 암호화된 URL 안에 있으므로 URL 자체도 인증정보처럼 취급해야 합니다.
- 삭제는 DELETING으로 접수하고 Nova 삭제 완료 확인 후 공개 조회에서 제외합니다. 실패는 기록하고 다시 처리합니다. 사용자 삭제는 세션 폐기와 VM 회수 접수를 원자적으로 저장합니다. 역할 변경은 기존 세션을 모두 폐기합니다. 관리자 자기 삭제·마지막 관리자 삭제/강등은 금지됩니다.
- GET은 DB 읽기와 가용성 조회만 수행하고 VM 생성·삭제·롤백을 수행하지 않습니다. 내부 시스템 사용자는 양의 정수 ID를 가지며 로그인·공개 목록·집계에서 제외됩니다. 삭제 기록과 감사 이벤트는 보존합니다.

## 빌드 확인

GitHub의 `.github/workflows/publish-image.yml`은 CollabOps에서 미러된 모든 브랜치 push, `v*` 태그 push, 수동 실행에서 Docker 이미지를 빌드해 GHCR에 업로드합니다. PR에서는 빌드만 검증합니다. 브랜치 이름을 정규화한 태그와 `sha-<전체 커밋 SHA>`를 발행하고, 기본 브랜치 `main`에서만 `latest`를 갱신합니다. 버전 태그 `v1.2.3`은 이미지 태그 `1.2.3`으로 발행합니다.

GitHub Actions repository variables는 `GHCR_IMAGE=ghcr.io/devoops-team/devoops-backend`, `IMAGE_PLATFORMS=linux/amd64`입니다. 인증에는 자동 제공되는 `GITHUB_TOKEN`과 작업의 `packages: write` 권한을 사용하므로 별도 GHCR secret은 필요 없습니다. 기존 CollabOps → GitHub 미러의 `GH_DEPLOY_KEY`는 CollabOps에 설정합니다.

```bash
make vet
make build
```

이미지 메타데이터 선택 검사는 `go test ./internal/cloud`로 실행합니다. OS 최초 등록·재시작 보존·빈 결과·실패 재시도·동시 초기화 검사는 실제 MongoDB replica set을 사용하는 통합 검사입니다. `VDI_TEST_MONGO_URI`를 설정해 `go test ./...`로 실행하며, 임시 테스트 DB만 생성하고 종료 시 삭제합니다. URI가 없으면 DB 통합 검사는 건너뜁니다. OpenStack HTTP 목업은 사용하지 않습니다. 로컬 Compose가 기본 실행 구성이며 `deploy/`는 Kubernetes 배포 참고 자료입니다.
