#!/usr/bin/env python3
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name+'.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


startup = load('check-image-startup')


class ImageStartupTests(unittest.TestCase):
    def test_startup_uses_digest_isolation_and_always_cleans(self):
        for broken in (False, True):
            with self.subTest(broken=broken), tempfile.TemporaryDirectory() as root:
                digest = 'sha256:'+'a'*64
                Path(root,'.deploy-digest').write_text(digest)
                env = {'WORKSPACE':root,'SOURCE_BRANCH':'develop','DEPLOY_ENV':'staging',
                       'IMAGE_NAME':'ghcr.io/devoops-team/devoops-backend'}
                calls = []
                def docker(*args):
                    calls.append(args)
                    if args[0] == 'inspect': return 'false' if broken else 'true'
                    return ''
                with patch.object(startup,'docker',side_effect=docker), patch.object(startup.time,'sleep'):
                    if broken:
                        with self.assertRaises(RuntimeError): startup.check_image(env)
                    else: startup.check_image(env)
                api = next(c for c in calls if c[0]=='run' and c[-1].endswith(digest))
                self.assertIn('OS_DOMAIN_NAME=Default',api)
                self.assertNotIn('-p',api)
                self.assertNotIn('-v',api)
                self.assertTrue(any(c[:3]==('network','create','--internal') for c in calls))
                self.assertEqual(sum(c[:3]==('rm','-f','-v') for c in calls),3)
                self.assertEqual(calls[-1][:2],('network','rm'))
                checks = [c for c in calls if c[0]=='exec' and c[-1]==startup.HTTP_CHECK]
                self.assertEqual(len(checks),0 if broken else 3)

    def test_startup_rejects_main_before_docker(self):
        with patch.object(startup,'docker') as docker:
            with self.assertRaises(RuntimeError): startup.check_image({'SOURCE_BRANCH':'main','DEPLOY_ENV':'prod'})
            docker.assert_not_called()

    def test_mock_exposes_compute_and_image_version_discovery(self):
        with patch('http.server.HTTPServer'):
            scope = {}
            exec(startup.MOCK, scope)
        for path in ('/compute/', '/image/'):
            handler = object.__new__(scope['Handler'])
            handler.path = path
            handler.wfile = io.BytesIO()
            handler.send_response = lambda status: self.assertEqual(status, 200)
            handler.send_header = lambda *args: None
            handler.end_headers = lambda: None
            handler.do_GET()
            version = json.loads(handler.wfile.getvalue())['versions'][0]
            self.assertEqual(version['id'], 'v2.0')
            self.assertEqual(version['links'][0]['href'], 'http://cloud:8000'+path)

    def test_startup_diagnostic_redacts_arbitrary_log_text(self):
        logs = '\n'.join([json.dumps({'msg':'startup failed','reason':'Nova endpoint unavailable'}),
                            'private-password'])
        with patch.object(startup,'docker',side_effect=[json.dumps({'ExitCode':1,'OOMKilled':False}),logs]):
            message = startup.startup_failure('ci-container')
        self.assertIn('Compute',message)
        self.assertNotIn('private-password',message)
        with patch.object(startup,'docker',side_effect=[json.dumps({'ExitCode':1}),
                         json.dumps({'msg':'startup failed','reason':'secret-token'})]):
            self.assertNotIn('secret-token',startup.startup_failure('ci-container'))

    def test_embedded_mock_and_http_probe_compile(self):
        compile(startup.MOCK, '<mock-cloud>', 'exec')
        compile(startup.HTTP_CHECK, '<image-http-probe>', 'exec')

    def test_startup_wait_is_bounded(self):
        with self.assertRaises(RuntimeError):
            startup.wait(lambda: (_ for _ in ()).throw(subprocess.CalledProcessError(1,'docker')),
                         attempts=2,sleep=lambda _:None)

    def test_pipeline_stops_at_manifest_update_without_cluster_access(self):
        text=Path(__file__).parent.parent.joinpath('Jenkinsfile').read_text()
        stages=['이미지 업로드 [develop]','이미지 기동 검사 [develop]','이미지 조회 [main]','매니페스트 갱신 [develop·main]']
        positions=[text.index("stage('"+name+"')") for name in stages]
        self.assertEqual(positions,sorted(positions))
        startup_stage=text.split("stage('이미지 기동 검사 [develop]')",1)[1].split("stage('",1)[0]
        self.assertIn("env.DEPLOY_ENV == 'staging'",startup_stage)
        for removed in ('kubectl', 'KUBECONFIG', 'PROD_VERIFY_CREDENTIALS', 'check-prod-health', '운영 HTTP 확인 [main]'):
            self.assertNotIn(removed,text)
        self.assertIn('실제 배포 상태는 Argo CD에서 확인',text)


if __name__=='__main__': unittest.main()
