# VDI 백엔드

[prd.md](prd.md) 및 [Notion API 명세](https://app.notion.com/p/dc4b52ad1e3e83609bd881ce641fedd9)를 구현한 Go 백엔드입니다. 최초 구현 시 확인한 18개 상세 계약은 [docs/reference/notion-contracts.json](docs/reference/notion-contracts.json)에 보관했습니다. 기존 Python/Kubernetes Pod 목업을 런타임에 사용하지 않습니다.

Go 표준 HTTP 서버, MongoDB 공식 드라이버, Gophercloud OpenStack SDK를 사용합니다. API와 영속 작업자가 한 프로세스에서 실행됩니다. 공개 API는 `/api` 아래 18개이며, 공개 DTO에 MongoDB 내부 필드·VM ID·IP·비밀번호를 노출하지 않습니다.

## 실행

Go 1.26 및 MongoDB replica set이 필요합니다. 단독 MongoDB는 트랜잭션을 지원하지 않으므로 사용할 수 없습니다.

1. `deploy/configmap.yaml`과 `deploy/secret.example.yaml`의 환경 값을 준비합니다. 로컬 실행 시 해당 값을 환경 변수로 내보냅니다. 실제 Secret 파일은 `deploy/secret.yaml`로 만들면 Git에서 제외됩니다.
2. `make build`로 바이너리를 빌드합니다.
3. 환경 변수를 주입한 후 `./bin/vdi-api`를 실행합니다. 기본 주소는 `:8080`입니다.

```bash
curl -i http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  --data '{"email":"admin@example.com","password":"secret"}'
```

초기 관리자 기본 계약은 `admin` / `admin@example.com` / `secret`이며 예제 Secret에서 주입합니다. 최초 부트스트랩 표시를 DB에 기록하므로 재시작 시 기존 관리자 이름·역할·비밀번호를 덮어쓰거나 삭제된 계정을 재생성하지 않습니다. 일반 사용자는 관리자 API로 추가합니다.

## 설정

| 환경 변수 | 의미 |
| --- | --- |
| `MONGO_URI`, `MONGO_DATABASE` | replica set 연결 URI, DB 이름(기본 `vdi`) |
| `HTTP_ADDR` | HTTP 바인딩 주소(기본 `:8080`) |
| `ADMIN_NAME`, `ADMIN_EMAIL`, `ADMIN_PASSWORD` | 최초 관리자 정보, Secret 필수 |
| `OS_AUTH_URL`, `OS_USERNAME`, `OS_PASSWORD` | Keystone v3 인증 |
| `OS_PROJECT_ID` 또는 `OS_PROJECT_NAME` | 인증 프로젝트 |
| `OS_USER_DOMAIN_NAME`, `OS_PROJECT_DOMAIN_NAME` | 인증 도메인, Gophercloud의 표준 OS 환경 변수 지원 |
| `OS_REGION_NAME` | 서비스 카탈로그 리전 |
| `OS_NETWORK_ID`, `OS_SECURITY_GROUPS` | VM 네트워크 UUID, 쉼표로 구분한 보안 그룹 이름 |
| `VDI_SERVICE_ID` | metadata 자원 추적 범위, 기본 `devoops-vdi`; 운영 DB마다 고유하고 재시작 시 유지 |
| `OS_IMAGES_JSON` | `name`, `type`, `version`, `imageId`를 갖는 OS 등록 배열 |
| `RDP_CREDENTIALS_JSON` | OS 타입별 `username`, `password`, 선택 `domain` 객체 |
| `GUAC_URL` | 외부 Guacamole 절대 URL |
| `GUAC_JSON_KEY` | JSON 인증 확장과 동일한 AES-128 공유키, 32자리 hex |
| `EXTERNAL_TIMEOUT` | 개별 외부 호출 제한, 기본 `10s` |
| `POLL_INTERVAL` | 작업 재조회 간격, 기본 `5s` |
| `READY_TIMEOUT` | 생성·RDP 준비 제한, 기본 `15m` |
| `GUAC_URL_TTL` | 새 Guacamole URL 사용기한, 기본 `2m` |

등록 가능한 OS 타입은 `UBUNTU`, `WINDOWS`, `ROCKY`, `DEBIAN`입니다. 등록 배열이 비어 있으면 이미지 목록도 비어 있습니다. DB에 OS 등록을 저장하고, 목록 및 생성 시 실제 Glance 이미지와 `m1.micro`를 검사합니다. OS 내부 `imageId`는 API JSON에서 제외합니다.

Flavor RAM(MiB)은 1024의 배수여야 합니다. 루트 디스크가 0이면 Glance `virtual_size`(byte)가 양의 정수 GiB로 정확히 표현되어야 합니다. `min_disk`를 실제 크기로 대신 사용하지 않습니다. RAM/디스크를 반올림하지 않고 호환되지 않는 조합은 가용 목록에서 제외합니다. 생성 입력 사양이 실제 허용 사양과 다르면 400 검증 오류, 표현 불가능한 Flavor는 409 `SPEC_UNAVAILABLE`, 비가용 이미지는 409 `OS_UNAVAILABLE`입니다. 현재 계약에는 사양 안내 API가 없어 운영자가 실제 허용 CPU/RAM/디스크를 프론트에 전달해야 합니다.

## API

로그인 외에는 `Authorization: Bearer <token>`이 필요합니다. 관리자 경로는 `ADMIN`만 사용할 수 있습니다. JSON은 camelCase, ID는 양의 JavaScript 안전 정수, 시간은 RFC3339 UTC, 빈 배열은 `[]`, nullable 값은 `null`입니다. 오류는 `{code,message,details:[{field,message}]}`입니다.

| Method | 경로 | 성공 |
| --- | --- | --- |
| POST | `/api/auth/login` | 200 |
| POST | `/api/auth/logout` | 204, 본문 없음 |
| GET | `/api/me` | 200 |
| GET | `/api/images` | 200 |
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

## 검증

```bash
make vet
make test
make test-integration
```

`make test-integration`은 Docker의 전용 MongoDB 8 replica set을 사용하며 실제 트랜잭션·unique/TTL index·HTTP API를 검증합니다. 테스트마다 고유 DB를 만들고 종료 후 그 DB를 제거합니다. Keystone/Nova/Glance만 HTTP 대체 서버로 바꾸고 실제 Gophercloud를 사용합니다. API 테스트의 RDP probe는 준비/실패를 제어하는 대체 경계이고, 별도 테스트는 실제 TCP RDP 협상 응답을 검증합니다. Guacamole 서버가 필요 없이 payload를 복호화해 서명을 검증합니다.

기존 테스트 DB 연결을 사용하려면:

```bash
TEST_MONGO_URI='mongodb://127.0.0.1:27018/?replicaSet=rs0&directConnection=true' make test-integration
```

`TEST_MONGO_URI` 없는 일반 `make test`에서는 DB 통합 테스트를 명시적으로 skip합니다. 완료 검증에는 반드시 `make test-integration`을 사용하세요. 테스트 컨테이너 정리는 `docker compose -f compose.test.yaml down`입니다.

## 배포

```bash
docker build -t vdi-api:local .
# image 태그를 실제 레지스트리 주소로 변경 후 배포 환경에서 실행
kubectl apply -f deploy/configmap.yaml
kubectl apply -f deploy/secret.yaml
kubectl apply -f deploy/deployment.yaml
```

Deployment 기본 replica는 1입니다. `Recreate` 전략과 종료 유예로 교체 시 진행 중 작업을 정리하고, 중단된 작업은 lease 만료 후 복구합니다. 컨테이너는 non-root, 읽기 전용 파일시스템으로 실행됩니다. TLS 종료·Ingress는 운영 환경에서 구성해야 합니다.

외부 준비 조건은 MongoDB replica set, 접근 가능한 Keystone/Nova/Glance, `m1.micro`, 등록한 Glance 이미지, VM 네트워크·RDP 보안 그룹, RDP가 준비된 이미지와 OS별 계정, Guacamole JSON 인증 확장 및 동일 공유키입니다. 서비스 계정은 프로젝트 VM 목록과 지정 이미지·Flavor를 조회할 수 있어야 합니다. 백엔드와 Guacamole에서 VM IP로 RDP 접근이 가능해야 합니다.

실제 OpenStack VM 생성·Guacamole 브라우저 접속·삭제 E2E 및 운영 배포는 별도 검증 단계이며 이 저장소의 HTTP 대체 테스트 통과와 구분합니다. 로그아웃은 VM 또는 이미 열린 Guacamole 연결을 종료하지 않습니다. Cinder, 공개 회원가입, VM 시작/중지, 프론트, 메트릭 API는 구현 범위에 포함하지 않습니다.
