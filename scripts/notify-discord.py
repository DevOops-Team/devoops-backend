#!/usr/bin/env python3
"""One final CI notification. No Jenkins API credentials or script approvals required."""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request


def redact(text, env):
    for name in ("DISCORD_WEBHOOK", "GHCR_TOKEN", "GHCR_PASSWORD", "COLLABOPS_TOKEN"):
        value = env.get(name, "")
        if value:
            text = text.replace(value, "****")
    text = re.sub(r"https?://(?:canary\.|ptb\.)?discord(?:app)?\.com/api/webhooks/[^\s<>\"']+", "[webhook masked]", text)
    text = re.sub(r"(://)[^\s/@:]+:[^\s/@]+@", r"\1****@", text)
    text = re.sub(r"(?i)((?:password|token|secret|authorization)\s*[:=]\s*)(?:bearer\s+)?[^\s,;]+", r"\1****", text)
    text = re.sub(r"(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+)", "****", text)
    return text.replace("```", "'''")


def current_digest(env):
    path = Path(env.get("WORKSPACE", ".")) / ".deploy-digest"
    try:
        # A checkout/tool failure must not report a previous build's image.
        if path.stat().st_mtime * 1000 < int(env.get("N_START_MILLIS", "0")):
            return "미확정"
        value = path.read_text().strip()
        if re.fullmatch(r"sha256:[0-9a-f]{64}", value):
            return "sha256:" + value[7:19]
    except (OSError, ValueError):
        pass
    return "미확정"


def build_log(env):
    """The current deployment runs on the controller; remote agents may lack this file."""
    home, job, number = env.get("JENKINS_HOME"), env.get("JOB_NAME"), env.get("N_BUILD", "")
    if not home or not job or not number.isdigit():
        return ""
    parts = job.split("/")
    if any(part in ("", ".", "..") for part in parts):
        return ""
    path = Path(home)
    for part in parts:
        path = path / "jobs" / part
    try:
        with (path / "builds" / number / "log").open("rb") as stream:
            stream.seek(0, 2)
            stream.seek(max(0, stream.tell() - 2 * 1024 * 1024))
            text = stream.read().decode("utf-8", errors="replace")
    except OSError:
        return ""
    text = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", text)
    # Exclude our own shell step and pipeline-post housekeeping from the failure excerpt.
    for marker in ("[Pipeline] { (Declarative: Post Actions)", "[Pipeline] sh (Discord 알림)"):
        text = text.split(marker, 1)[0]
    return redact(text, env)


def commits():
    try:
        merge = subprocess.run(["git", "rev-parse", "--verify", "HEAD^2"], capture_output=True).returncode == 0
        args = ["git", "log", "--no-merges", "--format=%h %s (%an)", "-8"]
        result = subprocess.run(args + (["HEAD^1..HEAD"] if merge else ["-1"]), capture_output=True, text=True, check=True)
        return result.stdout.strip() or "커밋 정보 없음"
    except (OSError, subprocess.SubprocessError):
        return "커밋 정보 없음"


def payload(env, log="", commit="", now=None, digest="미확정"):
    status = env.get("N_RESULT", "UNKNOWN")
    label = {"SUCCESS": "✅ 성공", "FAILURE": "❌ 실패", "UNSTABLE": "⚠️ 불안정", "ABORTED": "⏹️ 중단"}.get(status, "ℹ️ 상태 미확인")
    now = now or dt.datetime.now(dt.timezone.utc)
    kst = now.astimezone(dt.timezone(dt.timedelta(hours=9)))
    fields = []

    def field(name, value, inline=False):
        fields.append({"name": name, "value": redact(str(value or "-"), env)[:1000], "inline": inline})

    field("브랜치 → 환경", f"{env.get('N_BRANCH', '-')} → {env.get('N_ENV', '-')}", True)
    field("소요 시간", env.get("N_DURATION"), True)
    field("완료 알림 시각 (KST)", kst.strftime("%Y-%m-%d %H:%M:%S KST"))
    field("이미지 digest / 내용 해시", f"{digest}\nsource-tree: {env.get('N_TREE', '-')}")
    url = env.get("N_BUILD_URL", "")
    if url:
        field("Jenkins 빌드", url)
    if commit:
        field("커밋", commit[:800])
    log = redact(log, env)
    description = ""
    if status == "SUCCESS":
        lines = [line for line in log.splitlines() if re.search(r"(?:staging|prod) 반영:", line)]
        field("결과", lines[-1] if lines else "파이프라인 성공. 인프라 반영 상세는 Jenkins 로그를 확인하세요.")
    else:
        field("실패·중단 단계", env.get("N_LAST_STAGE", "파이프라인 시작"))
        lines = [line for line in log.splitlines() if line.strip() and "[Pipeline]" not in line]
        tail = lines[-25:]
        # Retain earlier ERROR lines even if skipped stages filled the end of the log.
        errors = [line for line in lines if re.search(r"\bERROR\b|Finished:", line)]
        excerpt = "\n".join([line for line in errors[-3:] if line not in tail] + tail)
        if excerpt:
            description = "**오류·마지막 로그 (최대 25줄 + ERROR)**\n```text\n" + excerpt[-2600:] + "\n```"
        else:
            field("오류 로그", "로컬 로그 파일을 읽을 수 없습니다. Jenkins 빌드 링크에서 확인하세요.")
    embed = {
        "title": f"[CI/CD] {env.get('N_REPO', 'backend')} #{env.get('N_BUILD', '-')} {label}"[:240],
        "color": {"SUCCESS": 0x2ECC71, "FAILURE": 0xE74C3C, "UNSTABLE": 0xF1C40F}.get(status, 0x95A5A6),
        "fields": fields,
    }
    if description:
        embed["description"] = description
    return {"username": "Jenkins", "allowed_mentions": {"parse": []}, "embeds": [embed]}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--dry-run", action="store_true", help="Render sanitized JSON without sending")
    args = parser.parse_args()
    env = os.environ
    message = payload(env, build_log(env), commits(), digest=current_digest(env))
    if args.dry_run:
        print(json.dumps(message, ensure_ascii=False))
        return
    webhook = env.get("DISCORD_WEBHOOK", "")
    try:
        target = urllib.parse.urlsplit(webhook)
    except ValueError:
        print("Discord 알림 생략: 웹훅 Credential 형식을 확인하세요.")
        return
    if target.scheme != "https" or target.hostname not in ("discord.com", "discordapp.com", "canary.discord.com", "ptb.discord.com") or not target.path.startswith("/api/webhooks/"):
        print("Discord 알림 생략: 유효한 웹훅 Credential이 필요합니다.")
        return
    request = urllib.request.Request(webhook, data=json.dumps(message).encode(), headers={"Content-Type": "application/json", "User-Agent": "Jenkins-CI-notifier"}, method="POST")
    try:
        # One request, no retries: avoid duplicate messages. Never log the URL or response body.
        with urllib.request.build_opener(NoRedirect).open(request, timeout=10) as response:
            print(f"Discord 알림 HTTP {response.status}")
    except urllib.error.HTTPError as error:
        print(f"Discord 알림 HTTP {error.code} (전송 실패, 원래 빌드 결과 유지)")
    except Exception:
        print("Discord 알림 전송 실패: 네트워크·Credential을 확인하세요. 원래 빌드 결과는 유지합니다.")


if __name__ == "__main__":
    main()
