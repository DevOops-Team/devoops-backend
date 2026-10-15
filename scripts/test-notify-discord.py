#!/usr/bin/env python3
"""Offline notification tests; no Jenkins build or Discord request is made."""
import datetime as dt
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("notify", Path(__file__).with_name("notify-discord.py"))
notify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(notify)


class NotificationTests(unittest.TestCase):
    def env(self, result="SUCCESS"):
        return {"N_REPO": "devoops-backend", "N_RESULT": result, "N_BRANCH": "develop",
                "N_ENV": "staging", "N_BUILD": "4", "N_BUILD_URL": "https://jenkins.example/job/backend/4/",
                "N_DURATION": "2 min", "N_TREE": "abcdef123456", "N_LAST_STAGE": "Trivy"}

    def test_success_fields_and_kst(self):
        value = notify.payload(self.env(), "staging 반영: abc123 → 미러", "abc fix",
                               dt.datetime(2026, 10, 15, 0, 0, tzinfo=dt.timezone.utc), "sha256:123456789abc")
        fields = {f["name"]: f["value"] for f in value["embeds"][0]["fields"]}
        self.assertEqual(fields["완료 알림 시각 (KST)"], "2026-10-15 09:00:00 KST")
        self.assertIn("staging 반영:", fields["결과"])
        self.assertIn("sha256:123456789abc", fields["이미지 digest / 내용 해시"])
        self.assertEqual(value["allowed_mentions"], {"parse": []})

    def test_failed_stage_not_skipped_stage(self):
        value = notify.payload(self.env("FAILURE"), 'ERROR: vulnerability\nStage "GHCR push" skipped due to earlier failure')
        fields = {f["name"]: f["value"] for f in value["embeds"][0]["fields"]}
        self.assertEqual(fields["실패·중단 단계"], "Trivy")
        self.assertIn("vulnerability", value["embeds"][0]["description"])

    def test_redaction_and_size_limits(self):
        env = self.env("FAILURE")
        env["DISCORD_WEBHOOK"] = "https://discord.com/api/webhooks/123/fake-secret"
        log = ("ERROR: " + "a" * 200 + "\n") * 100
        log += env["DISCORD_WEBHOOK"] + " token=private-value mongodb://user:password@mongo/"
        value = notify.payload(env, log, "commit " * 400)
        encoded = json.dumps(value)
        for secret in (env["DISCORD_WEBHOOK"], "private-value", "user:password"):
            self.assertNotIn(secret, encoded)
        embed = value["embeds"][0]
        self.assertLessEqual(len(embed["description"]), 4096)
        self.assertTrue(all(len(f["value"]) <= 1024 for f in embed["fields"]))
        self.assertLess(sum(len(f['name']) + len(f['value']) for f in embed['fields']) + len(embed['description']) + len(embed['title']), 6000)

    def test_stale_digest_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".deploy-digest"
            path.write_text("sha256:" + "a" * 64)
            os.utime(path, (1, 1))
            env = {"WORKSPACE": directory, "N_START_MILLIS": "2000"}
            self.assertEqual(notify.current_digest(env), "미확정")
            os.utime(path, (3, 3))
            self.assertEqual(notify.current_digest(env), "sha256:" + "a" * 12)

    def test_local_log_excludes_post_and_handles_missing_log(self):
        with tempfile.TemporaryDirectory() as directory:
            env = dict(self.env("FAILURE"), JENKINS_HOME=directory, JOB_NAME="backend/develop")
            self.assertEqual(notify.build_log(env), "")
            path = Path(directory) / "jobs/backend/jobs/develop/builds/4/log"
            path.parent.mkdir(parents=True)
            path.write_text("ERROR: original\n[Pipeline] { (Declarative: Post Actions)\nnotification noise")
            self.assertIn("ERROR: original", notify.build_log(env))
            self.assertNotIn("notification noise", notify.build_log(env))

    def test_prod_success_and_no_log_fallback(self):
        env = dict(self.env(), N_BRANCH="main", N_ENV="prod")
        value = notify.payload(env, "prod 반영: 987 → 미러")
        self.assertIn("main → prod", json.dumps(value, ensure_ascii=False))
        failed = notify.payload(self.env("ABORTED"))
        self.assertIn("로그 파일", json.dumps(failed, ensure_ascii=False))

    def test_transport_failure_preserves_result_and_hides_url(self):
        env = dict(self.env("FAILURE"), DISCORD_WEBHOOK="https://discord.com/api/webhooks/123/not-a-real-token")
        output = io.StringIO()
        with mock.patch.dict(os.environ, env, clear=True), mock.patch("sys.argv", ["notify-discord.py"]), \
                mock.patch.object(notify, "commits", return_value="commit"), \
                mock.patch.object(notify.urllib.request, "build_opener") as opener, \
                mock.patch("sys.stdout", output):
            opener.return_value.open.side_effect = OSError(env["DISCORD_WEBHOOK"])
            notify.main()
        self.assertIn("원래 빌드 결과", output.getvalue())
        self.assertNotIn(env["DISCORD_WEBHOOK"], output.getvalue())
        opener.return_value.open.assert_called_once()


if __name__ == "__main__":
    unittest.main()
