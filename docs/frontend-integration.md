# 프론트엔드 연동

`../frontend`의 VDI 화면은 Go API를 사용한다. 브라우저 요청 흐름은 다음과 같다.

```text
브라우저 → Next.js /backend/api/* → Go :8080/api/*
```

Next.js의 기존 `/api/*` 구현과 경로 충돌을 피하고 동일 출처 프록시를 사용한다. Bearer 토큰, JSON 본문, Idempotency-Key와 쿼리는 백엔드로 전달된다. 기존 백엔드 DTO와 인증 계약은 변경하지 않았다.

생성 폼은 추가한 `GET /api/images/{osId}/spec`에서 `{osId,cpuCores,memoryGb,storageGb}`를 조회한다. 실제 API 모드에서는 조회된 사양을 사용하고 조회가 끝나기 전에는 생성할 수 없다. OS 변경 시 이전 사양을 비우며, 조회 실패 시 재시도할 수 있다. 생성 API에서도 현재 사양을 다시 검증한다. 이미지·Flavor 내부 ID는 노출하지 않는다.

## 실행

1. 백엔드 README의 MongoDB replica set, OpenStack, Guacamole 및 관리자 환경 변수를 설정하고 `make build` 후 `./bin/vdi-api`를 실행한다.
2. `../frontend/.env.local`에 아래 값을 설정한다. 현재 로컬 파일에는 적용되어 있다.

   ```dotenv
   VDI_BACKEND_URL=http://127.0.0.1:8080
   NEXT_PUBLIC_API_BASE_URL=/backend
   NEXT_PUBLIC_VDI_DEMO=false
   ```

3. 프론트엔드 폴더에서 `npm run dev`를 실행하고 `http://localhost:3000/login`에 접속한다. 초기 관리자 계정은 백엔드에 설정한 `ADMIN_EMAIL` / `ADMIN_PASSWORD`를 사용한다.
4. 관리자 사용자 생성, 사용자 로그인, 이미지 목록, 데스크톱 생성·조회·접속·삭제를 확인한다. 생성 폼의 CPU/RAM/디스크는 실제 `m1.micro`와 선택 이미지의 허용 사양을 자동 조회한다.

다른 호스트나 컨테이너에서는 `VDI_BACKEND_URL`을 **Next.js 서버에서 접근 가능한** Go 서버 주소로 설정한다. 주소 변경 후 개발 서버를 재시작한다. 프로덕션은 환경 값을 설정한 뒤 다시 빌드하고 시작한다. 백엔드가 꺼져 있어도 데모로 자동 전환하지 않는다. 데모가 필요하면 `NEXT_PUBLIC_VDI_DEMO=true`로 명시하고 재시작/재빌드한다. 데모와 API 세션 저장 키를 분리했다.

로그아웃은 Go의 `POST /api/auth/logout`을 호출한 후 브라우저 세션을 정리한다. 백엔드 호출이 실패하면 오류를 알리고 로컬 세션을 정리한다.

## 재현 가능한 연동 검사

프론트 의존성을 설치하고 `VDI_BACKEND_URL=http://127.0.0.1:8080` 설정으로 프로덕션 빌드한 뒤 백엔드 폴더에서 실행한다. 전용 테스트 MongoDB replica set URI와 프론트 폴더의 **절대 경로**를 지정한다. 8080·3107 포트가 비어 있어야 한다.

```bash
TEST_MONGO_URI='mongodb://127.0.0.1:27028/?replicaSet=vdi-integration&directConnection=true' \
FRONTEND_INTEGRATION_DIR="$PWD/../frontend" \
make test-integration
```

`TestFrontendIntegration`은 전용 Go HTTP 서버·작업자와 Next.js 프로덕션 서버를 시작하고, `scripts/test-frontend.cjs`에서 실제 `frontend/lib/vdi/api.ts`를 실행한다. sessionStorage와 리다이렉트는 Node 검사 환경에서 대체하고 실제 MongoDB 및 HTTP 요청을 사용한다. 테스트마다 고유 DB를 만들고 종료 시 DB와 서버를 정리한다. OpenStack·RDP 준비 상태는 기존 `testinfra`로 대체한다. 해당 테스트는 `FRONTEND_INTEGRATION_DIR`가 없으면 skip한다.

## 검증 결과 (2026-10-05~06)

- `make vet`, `make build`: 통과.
- 로컬 MongoDB 8.0 replica set으로 기존 백엔드 통합 테스트 전체 통과. race detector 사용.
- 신규 사양 조회 검사 통과: 허용 사양으로 생성 성공, 미인증·잘못된 ID·없는 OS·불필요한 쿼리·비가용 이미지 및 사양 거부.
- 실제 프론트 API 모듈 → Next.js 프록시 → Go API/작업자 → MongoDB 검사 통과: 정상/실패 로그인, 사용자 생성, `/me`, 권한 검사, 이미지/사양 조회, 생성/멱등 재전송, 준비 상태, 암호화된 접속 URL 발급, 삭제 완료, 로그아웃 후 토큰 폐기, 관리자 할당/회수, 사용자/이벤트 쿼리, 집계, 401 세션 정리 및 로그인 리다이렉트.
- `npm run build -- --webpack`: 타입 검사 및 프로덕션 빌드 통과. 기본 Turbopack 빌드는 실행 환경의 포트 권한 제한으로 실패했다.
- 빌드된 Next.js 서버와 Go의 실제 `App.Handler()`를 사용하는 임시 서버를 연결해 7개 요청을 검증했다. 로그인 입력 검증, 이미지·생성·이벤트·접속·로그아웃의 미인증 응답, 없는 경로의 404가 직접 호출과 동일했다. 이 검사는 DB 없는 인증/입력 검증 경계만 확인하며 정상 로그인과 업무 처리 검증은 포함하지 않는다. 임시 서버는 종료했다.
- Chrome에서 정상 로그인, OS 목록, 생성 폼의 1코어/1GB/10GB 자동 반영, 생성 제출, 목록의 ‘사용 가능’ 전환 및 로그아웃을 확인했다. 실제 Go API와 MongoDB를 사용했다.
- 실제 OpenStack VM 생성 및 Guacamole 브라우저 RDP 접속은 운영 연결 설정이 없어 수행하지 않았다. 테스트에서 발급한 URL은 테스트용 Guacamole 주소이므로 실제 원격 화면을 제공하지 않는다.
