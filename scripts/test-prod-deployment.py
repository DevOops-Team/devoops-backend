#!/usr/bin/env python3
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('prod', Path(__file__).with_name('check-prod-health.py'))
prod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prod)
IMAGE = 'ghcr.io/devoops-team/devoops-backend@sha256:' + 'a' * 64


def fixture():
    status = dict(observedGeneration=3, replicas=1, updatedReplicas=1, readyReplicas=1, availableReplicas=1)
    deployment = dict(metadata=dict(generation=3), spec=dict(replicas=1, template=dict(spec=dict(
        containers=[dict(name='backend', image=IMAGE)]))), status=status)
    pod = dict(metadata=dict(labels=dict(app='backend')), spec=dict(containers=[dict(name='backend', image=IMAGE)]),
               status=dict(phase='Running', conditions=[dict(type='Ready', status='True')],
                           containerStatuses=[dict(name='backend', ready=True, state=dict(running={}))]))
    return deployment, dict(items=[pod])


class DeploymentTests(unittest.TestCase):
    def test_rolling_target_and_complete_status_required(self):
        deployment, pods = fixture()
        self.assertTrue(prod.deployment_ready(deployment, None, pods, IMAGE))
        for key, value in [('observedGeneration', 2), ('replicas', 2), ('updatedReplicas', 0),
                           ('readyReplicas', 0), ('availableReplicas', 0), ('unavailableReplicas', 1)]:
            bad = copy.deepcopy(deployment)
            bad['status'][key] = value
            with self.subTest(key=key):
                self.assertFalse(prod.deployment_ready(bad, None, pods, IMAGE))
        old = copy.deepcopy(deployment)
        old['spec']['template']['spec']['containers'][0]['image'] = IMAGE.replace('a'*64, 'b'*64)
        self.assertFalse(prod.deployment_ready(old, None, pods, IMAGE))
        self.assertFalse(prod.deployment_ready(None, None, pods, IMAGE))
        invalid = copy.deepcopy(deployment)
        invalid.pop('metadata')
        self.assertFalse(prod.deployment_ready(invalid, None, pods, IMAGE))
        deployment['spec']['replicas'] = 0
        self.assertFalse(prod.deployment_ready(deployment, None, dict(items=[]), IMAGE))

    def test_old_unready_terminating_or_missing_pods_do_not_pass(self):
        deployment, pods = fixture()
        variants = [dict(items=[]), dict(items=pods['items']*2)]
        for change in ('old', 'notready', 'terminating', 'waiting', 'phase'):
            bad = copy.deepcopy(pods)
            p = bad['items'][0]
            if change == 'old': p['spec']['containers'][0]['image'] = 'old:tag'
            if change == 'notready': p['status']['conditions'][0]['status'] = 'False'
            if change == 'terminating': p['metadata']['deletionTimestamp'] = 'now'
            if change == 'waiting': p['status']['containerStatuses'][0]['state'] = dict(waiting={})
            if change == 'phase': p['status']['phase'] = 'Pending'
            variants.append(bad)
        for pods in variants:
            self.assertFalse(prod.deployment_ready(deployment, None, pods, IMAGE))

    def test_workloadref_rollout_must_finish_current_generation(self):
        deployment, pods = fixture()
        rollout = dict(metadata=dict(generation=4), spec=dict(replicas=1, workloadRef=dict(kind='Deployment', name='backend')),
                       status=dict(deployment['status'], phase='Healthy', workloadObservedGeneration='3'))
        rollout['status']['observedGeneration'] = '4'
        self.assertTrue(prod.deployment_ready(deployment, rollout, pods, IMAGE))
        for key, value in [('phase', 'Paused'), ('phase', 'Degraded'), ('observedGeneration', '3'),
                           ('workloadObservedGeneration', '2'), ('abort', True), ('pauseConditions', [{}]),
                           ('updatedReplicas', 0)]:
            bad = copy.deepcopy(rollout)
            bad['status'][key] = value
            self.assertFalse(prod.deployment_ready(deployment, bad, pods, IMAGE))

    def test_cluster_query_filters_other_apps_and_uses_read_only_commands(self):
        deployment, pods = fixture()
        other = copy.deepcopy(pods['items'][0])
        other['metadata']['labels']['app'] = 'frontend'
        pods['items'].append(other)
        with patch.object(prod, 'kubectl', side_effect=[deployment, None, pods]) as query:
            self.assertTrue(prod.check_deployment(IMAGE))
            self.assertEqual(query.call_args_list[0].args, ('deployment/backend',))
            self.assertEqual(query.call_args_list[1].args, ('rollouts.argoproj.io/backend',))
        result = subprocess.CompletedProcess([], 0, json.dumps(deployment), '')
        with patch.object(prod.subprocess, 'run', return_value=result) as run:
            prod.kubectl('deployment/backend')
            command = run.call_args.args[0]
            self.assertIn('get', command)
            self.assertIn('vdi-prod', command)
            self.assertEqual(run.call_args.kwargs['timeout'], 15)

    def test_wait_is_bounded_and_never_accepts_old_healthy_version(self):
        with self.assertRaises(prod.DeploymentError):
            prod.wait_deployed(IMAGE, check=lambda _:False, sleep=lambda _:None, attempts=2)
        states = iter([False, True])
        prod.wait_deployed(IMAGE, check=lambda _:next(states), sleep=lambda _:None, attempts=2)

    def test_kubernetes_failures_are_redacted_and_only_absent_crd_is_optional(self):
        for stderr in ['Forbidden secret-token', 'Unauthorized secret-token', 'connection refused private-data']:
            result = subprocess.CompletedProcess([], 1, '', stderr)
            with patch.object(prod.subprocess, 'run', return_value=result):
                with self.assertRaises(prod.DeploymentError) as error:
                    prod.kubectl('rollouts.argoproj.io/backend', optional=True)
                self.assertNotIn('secret-token', str(error.exception))
                self.assertNotIn('private-data', str(error.exception))
        result = subprocess.CompletedProcess([], 1, '', 'the server doesn\'t have a resource type "rollouts"')
        with patch.object(prod.subprocess, 'run', return_value=result):
            self.assertIsNone(prod.kubectl('rollouts.argoproj.io/backend', optional=True))
        with patch.object(prod.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '', '')):
            self.assertIsNone(prod.kubectl('rollouts.argoproj.io/backend', optional=True))

    def test_access_preflight_reads_all_resources_without_deploy_digest(self):
        env = dict(SOURCE_BRANCH='main', DEPLOY_ENV='prod', KUBECONFIG='/test/config')
        with patch.object(prod, 'kubectl', side_effect=[{'metadata': {'name': 'backend'}}, {}, None]) as get:
            prod.check_access(env)
            self.assertEqual(get.call_args_list, [
                unittest.mock.call('deployment/backend'), unittest.mock.call('pods'),
                unittest.mock.call('rollouts.argoproj.io/backend', optional=True)])
        for invalid in ({}, dict(env, SOURCE_BRANCH='develop'), dict(env, DEPLOY_ENV='staging')):
            with patch.object(prod, 'kubectl') as get:
                with self.assertRaises(prod.DeploymentError): prod.check_access(invalid)
                get.assert_not_called()
        with patch.object(prod, 'kubectl', return_value=None):
            with self.assertRaises(prod.DeploymentError): prod.check_access(env)
        for failing_index in range(3):
            responses = [{'metadata': {'name': 'backend'}}, {}, None]
            responses[failing_index] = prod.DeploymentError('denied')
            with patch.object(prod, 'kubectl', side_effect=responses):
                with self.assertRaises(prod.DeploymentError): prod.check_access(env)

    def test_pipeline_uses_existing_credential_and_preflights_before_manifest_update(self):
        text = Path(__file__).parent.parent.joinpath('Jenkinsfile').read_text()
        self.assertIn("PROD_VERIFY_CREDENTIALS = 'k8s-staging-mongo'", text)
        self.assertNotIn('params.PROD_VERIFY_CREDENTIALS', text)
        self.assertNotIn('k8s-prod-readonly', text)
        self.assertLess(text.index('--check-access'), text.index("stage('매니페스트 갱신"))
        self.assertEqual(text.count('credentialsId: env.PROD_VERIFY_CREDENTIALS'), 2)

    def test_main_gates_http_on_deployment_and_rechecks_each_sample(self):
        with tempfile.TemporaryDirectory() as root:
            Path(root, '.deploy-digest').write_text('sha256:'+'a'*64)
            env = dict(SOURCE_BRANCH='main', DEPLOY_ENV='prod', WORKSPACE=root, KUBECONFIG='/test/config',
                       PROD_HEALTHCHECK_URL='https://example.test', IMAGE_NAME=IMAGE.split('@')[0])
            with patch.object(prod, 'wait_deployed', side_effect=prod.DeploymentError('timeout')), patch.object(prod, 'wait_healthy') as http:
                with self.assertRaises(prod.DeploymentError): prod.main(env)
                http.assert_not_called()
            wait_http = prod.wait_healthy
            with patch.object(prod, 'wait_healthy', side_effect=lambda base, check: wait_http(base, check=check, sleep=lambda _:None)), patch.object(prod, 'wait_deployed') as deployed, patch.object(prod, 'check_deployment', return_value=True) as check, patch.object(prod, 'check_once') as http:
                prod.main(env)
                deployed.assert_called_once_with(IMAGE)
                self.assertEqual(check.call_count, 3)
                self.assertEqual(http.call_count, 3)
            def one_sample(base, check):
                check(base)
            with patch.object(prod, 'wait_deployed'), patch.object(prod, 'check_deployment', return_value=False), patch.object(prod, 'check_once') as http, patch.object(prod, 'wait_healthy', side_effect=one_sample):
                with self.assertRaises(prod.DeploymentError): prod.main(env)
                http.assert_not_called()
            missing = dict(env)
            missing.pop('KUBECONFIG')
            with patch.object(prod, 'wait_deployed') as deployed:
                with self.assertRaises(ValueError): prod.main(missing)
                deployed.assert_not_called()
            for digest in ['latest', 'sha256:bad']:
                Path(root, '.deploy-digest').write_text(digest)
                with patch.object(prod, 'wait_deployed') as deployed:
                    with self.assertRaises(ValueError): prod.main(env)
                    deployed.assert_not_called()


if __name__ == '__main__':
    unittest.main()
