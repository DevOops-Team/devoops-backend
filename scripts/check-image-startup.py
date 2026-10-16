#!/usr/bin/env python3
"""Boot the exact promotion digest with disposable dependencies, never production secrets."""
import json
import os
from pathlib import Path
import re
import subprocess
import time
import uuid


class StartupError(RuntimeError):
    pass


MOCK = r'''
import datetime, json
from http.server import BaseHTTPRequestHandler, HTTPServer
class Handler(BaseHTTPRequestHandler):
 def log_message(self, *args): pass
 def do_POST(self):
  if self.path != '/v3/auth/tokens':
   self.send_error(404); return
  self.rfile.read(int(self.headers.get('Content-Length', 0)))
  catalog = [{'type': kind, 'name': name, 'endpoints': [{'interface':'public','region':'RegionOne','url':'http://cloud:8000/'+path+'/'}]} for kind,name,path in [('compute','nova','compute'),('image','glance','image')]]
  payload = json.dumps({'token':{'expires_at':(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M:%SZ'),'catalog':catalog}}).encode()
  self.send_response(201); self.send_header('Content-Type','application/json'); self.send_header('X-Subject-Token','disposable-ci-token'); self.end_headers(); self.wfile.write(payload)
HTTPServer(('0.0.0.0',8000), Handler).serve_forever()
'''
HTTP_CHECK = r'''
import json, urllib.request, urllib.error
for path, expected in [('/healthz',200),('/api/me',401)]:
 try: response = urllib.request.urlopen('http://backend:8080'+path,timeout=5)
 except urllib.error.HTTPError as error: response = error
 with response:
  assert response.status == expected
  assert 'application/json' in response.headers.get('Content-Type','').lower()
  body = json.load(response)
  assert body.get('status') == 'ok' if expected == 200 else body.get('code') in ('UNAUTHORIZED','UNAUTHENTICATED')
'''


def docker(*args):
    return subprocess.run(['docker', *args], check=True, capture_output=True, text=True, timeout=120).stdout.strip()


def wait(check, attempts=40, sleep=time.sleep):
    for attempt in range(attempts):
        try:
            check()
            return
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            if attempt + 1 < attempts:
                sleep(2)
    raise StartupError('임시 환경 준비 또는 새 이미지 HTTP 검사 시간 초과')


def check_image(env=os.environ):
    if env.get('SOURCE_BRANCH', env.get('BRANCH_NAME')) != 'main' or env.get('DEPLOY_ENV') != 'prod':
        raise StartupError('main → prod 기동 검사만 허용합니다')
    image = env.get('IMAGE_NAME')
    digest = (Path(env['WORKSPACE']) / '.deploy-digest').read_text().strip()
    if image != 'ghcr.io/devoops-team/devoops-backend' or not re.fullmatch(r'sha256:[a-f0-9]{64}', digest):
        raise StartupError('백엔드 불변 이미지 digest가 필요합니다')
    prefix = 'backend-check-' + uuid.uuid4().hex[:16]
    names = [prefix+'-api', prefix+'-mongo', prefix+'-cloud']
    network_created = False
    try:
        # Pull before creating the internal-only network. No published ports or host mounts.
        print('임시 MongoDB·HTTP 검사 도구 준비', flush=True)
        docker('pull', 'mongo:8.0')
        docker('pull', 'python:3.13-alpine')
        docker('network', 'create', '--internal', prefix)
        network_created = True
        print('격리 MongoDB replica set 준비', flush=True)
        docker('run', '-d', '--name', names[1], '--network', prefix, '--network-alias', 'mongo',
               'mongo:8.0', 'mongod', '--replSet', 'rs0', '--bind_ip_all')
        wait(lambda: docker('exec', names[1], 'mongosh', '--quiet', '--eval', 'db.adminCommand({ping:1})'))
        docker('exec', names[1], 'mongosh', '--quiet', '--eval',
               'rs.initiate({_id:"rs0",members:[{_id:0,host:"mongo:27017"}]})')
        wait(lambda: docker('exec', names[1], 'mongosh', '--quiet', '--eval',
                            'if (!db.hello().isWritablePrimary) quit(1)'))
        print('격리 OpenStack 인증 대체 서버 준비', flush=True)
        docker('run', '-d', '--name', names[2], '--network', prefix, '--network-alias', 'cloud',
               'python:3.13-alpine', 'python', '-u', '-c', MOCK)
        wait(lambda: docker('exec', names[2], 'python', '-c',
                            'import socket; socket.create_connection(("127.0.0.1",8000),2).close()'))
        settings = {
            'MONGO_URI':'mongodb://mongo:27017/?replicaSet=rs0', 'MONGO_DATABASE':'startup_check',
            'OS_AUTH_URL':'http://cloud:8000/v3', 'OS_USERNAME':'ci', 'OS_PASSWORD':'disposable',
            'OS_PROJECT_NAME':'ci', 'OS_DOMAIN_NAME':'Default',
            'OS_REGION_NAME':'RegionOne', 'OS_NETWORK_ID':'isolated-ci', 'OS_SECURITY_GROUPS':'ci',
            'OS_IMAGES_JSON':'[]', 'RDP_CREDENTIALS_JSON':'{}', 'GUAC_URL':'http://cloud:8000/guacamole/',
            'GUAC_JSON_KEY':uuid.uuid4().hex, 'ADMIN_NAME':'CI', 'ADMIN_EMAIL':'ci@example.invalid',
            'ADMIN_PASSWORD':uuid.uuid4().hex, 'HTTP_ADDR':':8080', 'OTEL_SDK_DISABLED':'true',
        }
        args = ['run', '-d', '--name', names[0], '--network', prefix, '--network-alias', 'backend']
        for key,value in settings.items():
            args += ['--env', key+'='+value]
        print('승격 digest의 새 백엔드 기동·HTTP 검사', flush=True)
        docker(*args, image+'@'+digest)
        consecutive = 0
        for attempt in range(40):
            if docker('inspect', '--format', '{{.State.Running}}', names[0]) != 'true':
                raise StartupError('새 이미지가 기동 중 종료됐습니다. prod 매니페스트는 변경하지 않습니다')
            try:
                docker('exec', names[2], 'python', '-c', HTTP_CHECK)
                consecutive += 1
                print(f'새 이미지 헬스·미인증 API 정상 ({consecutive}/3 연속)', flush=True)
                if consecutive == 3:
                    return
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
                consecutive = 0
                print('새 이미지 HTTP 응답 대기 중', flush=True)
            time.sleep(2)
        raise StartupError('새 이미지 HTTP 검사 실패. prod 매니페스트는 변경하지 않습니다')
    finally:
        failed = False
        for name in names:
            try:
                docker('rm', '-f', '-v', name)
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
                # Missing containers are normal when setup failed before creation.
                result = subprocess.run(['docker','container','inspect',name], capture_output=True, timeout=20)
                failed |= result.returncode == 0
        if network_created:
            try:
                docker('network','rm',prefix)
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
                failed = True
        if failed:
            raise StartupError('임시 환경 정리 실패: backend-check 리소스를 확인하세요')


if __name__ == '__main__':
    import signal
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(SystemExit(143)))
    try:
        check_image()
        print('새 이미지 격리 기동·MongoDB 헬스·미인증 API 검사 성공')
    except StartupError as error:
        print(f'배포 전 검사 실패: {error}', flush=True)
        raise SystemExit(1)
    except Exception:
        # No container logs, generated credentials, raw HTTP bodies or Docker errors.
        print('배포 전 새 이미지 검사 실패. 임시 환경·이미지 기동을 확인하세요. prod 변경을 차단합니다.', flush=True)
        raise SystemExit(1)
