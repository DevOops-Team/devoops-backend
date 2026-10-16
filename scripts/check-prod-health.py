#!/usr/bin/env python3
"""Check the public backend proxy, without assuming which digest answered."""
import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(url):
    try:
        response = urllib.request.build_opener(NoRedirect).open(
            urllib.request.Request(url, headers={'Accept':'application/json', 'Cache-Control':'no-cache'}), timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = response.read(65537)
        if len(data) > 65536:
            raise ValueError('응답 크기 제한')
        return response.status, response.headers.get('Content-Type',''), json.loads(data)


def check_once(base, get=request):
    for path in ('/backend/api/me', '/backend/api/desktops'):
        status, content_type, body = get(base + path)
        if (status != 401 or 'application/json' not in content_type.lower() or
                not isinstance(body, dict) or body.get('code') not in ('UNAUTHENTICATED','UNAUTHORIZED')):
            raise ValueError('운영 백엔드 인증 경계 HTTP 검사 실패')


def wait_healthy(base, check=check_once, sleep=time.sleep, attempts=30):
    consecutive = 0
    for attempt in range(attempts):
        try:
            check(base)
            consecutive += 1
            print(f'운영 백엔드 HTTP 정상 ({consecutive}/3 연속)', flush=True)
            if consecutive == 3:
                return
        except Exception:
            consecutive = 0
            print('운영 백엔드 HTTP 재확인 대기', flush=True)
        if attempt + 1 < attempts:
            sleep(10)
    raise RuntimeError('운영 HTTP 검사에서 3회 연속 정상 응답을 확인하지 못했습니다')


def main(env=os.environ):
    if env.get('SOURCE_BRANCH',env.get('BRANCH_NAME')) != 'main' or env.get('DEPLOY_ENV') != 'prod':
        raise ValueError('main → prod HTTP 검사만 허용합니다')
    base = env.get('PROD_HEALTHCHECK_URL','')
    parsed = urllib.parse.urlsplit(base)
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password or
            parsed.path not in ('','/') or parsed.query or parsed.fragment):
        raise ValueError('운영 HTTPS origin이 필요합니다')
    print('운영 프록시·미인증 API 차단 응답을 검사합니다. 새 digest 배포 완료 판정은 별도입니다.', flush=True)
    wait_healthy(base.rstrip('/'))


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('운영 백엔드 HTTP 검사 실패. Argo CD·운영 접근 경로를 확인하세요. 자동 롤백하지 않습니다.', flush=True)
        raise SystemExit(1)
