#!/usr/bin/env python3
"""Offline rollout/API verification tests. No cluster or real account is contacted."""
import contextlib
import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("verify", Path(__file__).with_name("verify-staging.py"))
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)
REV = "a" * 40
IMAGE = verify.IMAGE_NAME + "@sha256:" + "b" * 64


def fixture():
    app = {"spec": {"source": {"targetRevision": "main"}}, "status": {
        "sync": {"status": "Synced", "revision": REV}, "health": {"status": "Healthy"},
        "operationState": {"phase": "Succeeded", "syncResult": {"revision": REV}}}}
    deployment = {"metadata": {"generation": 3}, "spec": {"replicas": 1, "template": {
        "spec": {"containers": [{"name": "backend", "image": IMAGE}]}}}, "status": {
            "observedGeneration": 3, "replicas": 1, "updatedReplicas": 1,
            "readyReplicas": 1, "availableReplicas": 1}}
    pods = {"items": [{"metadata": {"name": "backend-pod"}, "spec": {
        "containers": [{"name": "backend", "image": IMAGE}]}, "status": {
            "conditions": [{"type": "Ready", "status": "True"}],
            "containerStatuses": [{"name": "backend", "ready": True, "state": {"running": {}}}]}}]}
    service = {"spec": {"selector": {"app": "backend"}, "ports": [{"port": 8080}]}}
    return app, deployment, pods, service


class StagingTests(unittest.TestCase):
    def test_healthy_exact_deployment(self):
        self.assertTrue(verify.deployment_ready(*fixture(), REV, IMAGE))

    def test_wrong_digest_revision_generation_and_old_pods_block(self):
        mutations = [
            lambda a, d, p, s: a["status"]["sync"].update(revision="c" * 40),
            lambda a, d, p, s: a["spec"]["source"].update(targetRevision="feature"),
            lambda a, d, p, s: a["status"]["operationState"].update(phase="Running"),
            lambda a, d, p, s: d["status"].update(observedGeneration=2),
            lambda a, d, p, s: d["status"].update(availableReplicas=0),
            lambda a, d, p, s: d["spec"]["template"]["spec"]["containers"][0].update(image="wrong"),
            lambda a, d, p, s: p["items"][0]["spec"]["containers"][0].update(image="wrong"),
            lambda a, d, p, s: p["items"].append(copy.deepcopy(p["items"][0])),
            lambda a, d, p, s: p["items"][0]["status"]["containerStatuses"][0].update(ready=False),
            lambda a, d, p, s: s["spec"]["selector"].update(app="wrong"),
        ]
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                data = fixture()
                mutate(*data)
                self.assertFalse(verify.deployment_ready(*data, REV, IMAGE))

    def test_rollout_replicas_and_workload_generation(self):
        data = fixture()
        data[1]["spec"]["replicas"] = 0
        data[1]["status"]["replicas"] = 0
        data[2]["items"].append(copy.deepcopy(data[2]["items"][0]))
        ro = {"metadata": {"generation": 4}, "spec": {"replicas": 2,
              "workloadRef": {"kind": "Deployment", "name": "backend"}}, "status": {
              "phase": "Healthy", "observedGeneration": "4", "workloadObservedGeneration": "3",
              "replicas": 2, "updatedReplicas": 2, "readyReplicas": 2, "availableReplicas": 2}}
        self.assertTrue(verify.deployment_ready(*data, REV, IMAGE, rollout=ro))
        for key, value in [("phase", "Degraded"), ("updatedReplicas", 1), ("workloadObservedGeneration", "2")]:
            bad = copy.deepcopy(ro)
            bad["status"][key] = value
            self.assertFalse(verify.deployment_ready(*data, REV, IMAGE, rollout=bad))

    def test_only_pruning_out_of_sync_is_accepted(self):
        data = fixture()
        data[0]["status"]["sync"]["status"] = "OutOfSync"
        data[0]["status"]["resources"] = [{"status": "OutOfSync", "requiresPruning": True}]
        self.assertTrue(verify.deployment_ready(*data, REV, IMAGE))
        data[0]["status"]["resources"].append({"status": "OutOfSync"})
        self.assertFalse(verify.deployment_ready(*data, REV, IMAGE))

    def api(self, fail_path=None):
        calls = []
        def call(base, method, path, body=None, token=None):
            calls.append((method, path, token))
            if path == fail_path:
                return 500, {"code": "ERROR"}
            if path == "/healthz":
                return 200, {"status": "ok"}
            if path == "/api/auth/login":
                return 200, {"token": "test-session"}
            if path == "/api/auth/logout":
                return 204, None
            if path == "/api/me":
                if not token or any(p == "/api/auth/logout" for _, p, _ in calls):
                    return 401, {"code": "UNAUTHORIZED"}
                return 200, {"id": 5, "email": "ci@example.test", "role": "USER"}
            if path == "/api/desktops":
                return 200, []
            raise AssertionError("Unexpected API request")
        return call, calls

    def test_authenticated_api_smoke(self):
        call, calls = self.api()
        verify.smoke("http://127.0.0.1:1", "ci@example.test", "not-real", call)
        self.assertEqual(calls[-1][1], "/api/me")
        self.assertEqual(len(calls), 7)
        self.assertFalse(any(method in ("DELETE", "PATCH") for method, _, _ in calls))

    def test_bad_health_and_api_block_and_logout_cleans_session(self):
        for path in ("/healthz", "/api/auth/login", "/api/desktops"):
            with self.subTest(path=path):
                call, calls = self.api(path)
                with self.assertRaises(RuntimeError):
                    verify.smoke("http://127.0.0.1:1", "ci@example.test", "not-real", call)
                if path == "/api/desktops":
                    self.assertIn(("POST", "/api/auth/logout", "test-session"), calls)

    def test_marker_only_after_smoke_and_second_snapshot(self):
        @contextlib.contextmanager
        def tunnel(pod):
            self.assertEqual(pod, "backend-pod")
            yield "http://127.0.0.1:1"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            marker = root / ".staging-verified"
            smoke = mock.Mock()
            verify.verify(root, REV, IMAGE, "email", "password", fixture, tunnel, smoke)
            self.assertEqual(marker.read_text(), IMAGE)
            smoke.assert_called_once()
            # A failed retry removes any marker from a previous successful run.
            with self.assertRaises(RuntimeError):
                verify.verify(root, REV, IMAGE, "email", "password", fixture, tunnel,
                              mock.Mock(side_effect=RuntimeError("smoke failed")))
            self.assertFalse(marker.exists())
            changed = fixture()
            changed[0]["status"]["sync"]["revision"] = "c" * 40
            with self.assertRaises(RuntimeError):
                verify.verify(root, REV, IMAGE, "email", "password",
                              mock.Mock(side_effect=[fixture(), changed]), tunnel, smoke)
            self.assertFalse(marker.exists())

    def test_timeout_and_rbac_error_never_mark(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(RuntimeError):
                verify.verify(root, REV, IMAGE, "email", "password", limit=0)
            with self.assertRaises(PermissionError):
                verify.verify(root, REV, IMAGE, "email", "password",
                              get_snapshot=mock.Mock(side_effect=PermissionError("RBAC")))
            self.assertFalse((root / ".staging-verified").exists())

    def test_kubectl_failure_cleans_tunnel(self):
        with mock.patch.object(verify.subprocess, "Popen") as popen:
            process = popen.return_value
            process.poll.side_effect = [None, None]
            with mock.patch.object(verify.time, "monotonic", side_effect=[0, 31]):
                with self.assertRaises(RuntimeError):
                    with verify.tunnel("backend-pod"):
                        self.fail("Should not open a tunnel")
            process.terminate.assert_called_once()
            process.wait.assert_called_once()

    def test_preflight_cluster_and_portforward_guard(self):
        with tempfile.TemporaryDirectory() as directory:
            env = {"WORKSPACE": directory, "SOURCE_BRANCH": "develop", "DEPLOY_ENV": "staging",
                   "KUBECONFIG": "not-a-real-file", "SMOKE_EMAIL": "ci@example.test", "SMOKE_PASSWORD": "not-real"}
            def kube(*args, as_json=True):
                if args[:3] == ("config", "view", "--minify"):
                    return {"clusters": [{"cluster": {"server": verify.API_SERVER}}]}
                return "yes"
            with mock.patch.dict(verify.os.environ, env, clear=True), \
                    mock.patch("sys.argv", ["verify-staging.py", "--preflight"]), \
                    mock.patch.object(verify, "kubectl", side_effect=kube), \
                    mock.patch.object(verify, "snapshot", return_value=fixture()):
                verify.main()
                with mock.patch.object(verify, "kubectl", return_value={"clusters": [{"cluster": {"server": "https://prod.invalid"}}]}):
                    with self.assertRaises(RuntimeError):
                        verify.main()
                with mock.patch.dict(verify.os.environ, {"SOURCE_BRANCH": "main"}):
                    with self.assertRaises(RuntimeError):
                        verify.main()
            self.assertFalse((Path(directory) / ".staging-verified").exists())


if __name__ == "__main__":
    unittest.main()
