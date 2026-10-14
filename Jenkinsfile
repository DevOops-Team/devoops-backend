pipeline {
    agent any

    options {
        skipDefaultCheckout(true)
        disableConcurrentBuilds()
        timestamps()
        timeout(time: 30, unit: 'MINUTES')
        buildDiscarder(logRotator(numToKeepStr: '20'))
    }

    environment {
        MONGO_KUBECONFIG_CREDENTIALS = 'k8s-staging-mongo'
        IMAGE_NAME = 'ghcr.io/devoops-team/devoops-backend'
        // devoops-infra는 CollabOps 원본에 쓴다 (GitHub 미러에 쓰면 미러가 깨짐) — ci/update-image.sh
        INFRA_REPO = 'git@collabops.ai:devoops/devoops-infra.git'
        INFRA_SSH_KEY = '/var/jenkins_home/.ssh/id_ed25519_collabops_ci'
        GHCR_CREDENTIALS = 'ghcr-push'
    }

    stages {
        stage('Checkout') {
            // develop → 테스트 · 빌드 · staging 반영 / main → staging에서 검증한 같은 이미지를 prod로 (10-14 결정 A안)
            steps {
                script {
                    def scmVars = checkout scm
                    if (env.CHANGE_ID) {
                        error('PR 빌드는 배포할 수 없습니다. develop 또는 main 브랜치 작업을 실행하세요.')
                    }
                    // Multibranch는 BRANCH_NAME, 일반 Pipeline은 checkout 결과를 사용한다.
                    def branch = env.BRANCH_NAME ?: (scmVars.GIT_BRANCH ?: '')
                    branch = branch.replaceFirst(/^refs\/remotes\/origin\//, '')
                        .replaceFirst(/^origin\//, '')
                        .replaceFirst(/^refs\/heads\//, '')
                    env.SOURCE_BRANCH = branch
                    if (branch == 'develop') {
                        env.DEPLOY_ENV = 'staging'
                    } else if (branch == 'main') {
                        env.DEPLOY_ENV = 'prod'
                    } else {
                        error("develop → staging, main → prod만 허용합니다 (받은 브랜치: ${branch})")
                    }
                    def commit = sh(script: 'git rev-parse --short=12 HEAD', returnStdout: true).trim()
                    // 내용(tree) 해시: develop→main 머지 커밋은 SHA가 달라도 내용이 같으면 tree가 같다
                    env.TREE = sh(script: 'git rev-parse "HEAD^{tree}"', returnStdout: true).trim().substring(0, 12)
                    env.IMAGE_TAG = "${env.BUILD_NUMBER}-${commit}"
                    currentBuild.description = "${branch} → ${env.DEPLOY_ENV} (tree ${env.TREE})"
                }
            }
        }

        stage('Check tools') {
            steps {
                sh '''
                    set -eu
                    for tool in git bash awk ssh docker; do
                        command -v "$tool" >/dev/null
                    done
                    test -r "$INFRA_SSH_KEY"
                    docker version
                '''
                script {
                    if (env.DEPLOY_ENV == 'staging') {
                        sh '''
                    set -eu
                    go version
                    gcc --version
                    kubectl version --client
                    mongosh --version
                    trivy --version
                        '''
                    }
                }
            }
        }

        stage('Vet') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                sh 'make vet'
            }
        }

        stage('Unit test') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                sh 'make test'
            }
        }

        stage('Integration test') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                withCredentials([file(credentialsId: env.MONGO_KUBECONFIG_CREDENTIALS,
                    variable: 'KUBECONFIG')]) {
                    sh 'bash scripts/test-k8s-integration.sh'
                }
            }
        }

        stage('Docker build') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                sh '''
                    set -eu
                    docker build --pull -t "$IMAGE_NAME:$IMAGE_TAG" .
                '''
            }
        }

        stage('Trivy') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                sh '''
                    trivy image --scanners vuln --severity HIGH,CRITICAL \
                        --exit-code 1 --no-progress "$IMAGE_NAME:$IMAGE_TAG"
                '''
            }
        }

        stage('GHCR push') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                withCredentials([usernamePassword(credentialsId: env.GHCR_CREDENTIALS,
                    usernameVariable: 'GHCR_USERNAME', passwordVariable: 'GHCR_TOKEN')]) {
                    sh '''
                        set +x
                        set -eu
                        export DOCKER_CONFIG="$(mktemp -d)"
                        trap 'rm -rf "$DOCKER_CONFIG"' EXIT
                        printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
                        docker push "$IMAGE_NAME:$IMAGE_TAG"
                        # 승격용 tree 태그는 staging 갱신 성공 후 별도 단계에서 발행한다.
                        docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE_NAME:$IMAGE_TAG" | sed 's/.*@//' > "$WORKSPACE/.deploy-digest"
                    '''
                }
            }
        }

        stage('Resolve prod image') {
            // main: 새로 빌드하지 않는다 — develop에서 테스트 · 빌드 · staging 반영까지 끝난 같은 digest
            when { expression { env.DEPLOY_ENV == 'prod' } }
            steps {
                withCredentials([usernamePassword(credentialsId: env.GHCR_CREDENTIALS,
                    usernameVariable: 'GHCR_USERNAME', passwordVariable: 'GHCR_TOKEN')]) {
                    sh '''
                        set +x
                        set -eu
                        export DOCKER_CONFIG="$(mktemp -d)"
                        trap 'rm -rf "$DOCKER_CONFIG"' EXIT
                        printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
                        if ! docker pull -q "$IMAGE_NAME:tree-$TREE" >/dev/null; then
                            echo "staging에서 검증된 이미지가 없습니다: $IMAGE_NAME:tree-$TREE"
                            echo "main 내용이 develop 빌드와 다릅니다. main에는 develop에서만 머지하고, develop 빌드가 성공한 뒤 머지하세요."
                            exit 1
                        fi
                        docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE_NAME:tree-$TREE" | sed 's/.*@//' > "$WORKSPACE/.deploy-digest"
                    '''
                }
            }
        }

        stage('Update infra image') {
            // CollabOps devoops-infra overlays/<staging|prod> → GitHub 미러 → Argo CD
            steps {
                dir('infra-manifests') {
                    deleteDir()
                    sh '''
                        set -eu
                        # prod는 main, staging은 develop에서만 변경할 수 있다.
                        case "$SOURCE_BRANCH:$DEPLOY_ENV" in
                            develop:staging|main:prod) ;;
                            *) echo '브랜치와 배포 환경이 일치하지 않습니다.'; exit 1 ;;
                        esac
                        DIGEST="$(cat "$WORKSPACE/.deploy-digest")"
                        case "$DIGEST" in sha256:*) ;; *) echo "digest 없음"; exit 1 ;; esac
                        export GIT_SSH_COMMAND="ssh -i $INFRA_SSH_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes"
                        git clone -q --depth 1 -b main "$INFRA_REPO" infra
                        rc=0
                        bash infra/ci/update-image.sh "$DEPLOY_ENV" "$IMAGE_NAME" "$DIGEST" "jenkins #$BUILD_NUMBER tree-$TREE" || rc=$?
                        # 0 = 반영, 3 = 이미 같은 이미지
                        [ "$rc" = 0 ] || [ "$rc" = 3 ]
                    '''
                }
            }
        }

        stage('Publish promotion tag') {
            // staging manifest push 실패 시 main에서 찾을 승격 태그를 만들지 않는다.
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                withCredentials([usernamePassword(credentialsId: env.GHCR_CREDENTIALS,
                    usernameVariable: 'GHCR_USERNAME', passwordVariable: 'GHCR_TOKEN')]) {
                    sh '''
                        set +x
                        set -eu
                        export DOCKER_CONFIG="$(mktemp -d)"
                        trap 'rm -rf "$DOCKER_CONFIG"' EXIT
                        printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
                        EXPECTED="$(cat "$WORKSPACE/.deploy-digest")"
                        # Tag the exact pushed digest, even if a local tag changed meanwhile.
                        docker pull -q "$IMAGE_NAME@$EXPECTED" >/dev/null
                        docker tag "$IMAGE_NAME@$EXPECTED" "$IMAGE_NAME:tree-$TREE"
                        docker push "$IMAGE_NAME:tree-$TREE"
                    '''
                }
            }
        }
    }

    post {
        success {
            echo "${env.SOURCE_BRANCH} → 인프라 overlays/${env.DEPLOY_ENV} 갱신 완료. Argo CD 동기화·Healthy 상태는 별도로 확인하세요."
        }
        always {
            notifyDiscord('devoops-backend')
        }
    }
}

// 빌드가 끝나면 결과를 Discord로 알린다. 웹훅 URL은 Jenkins Credential(discord-ci-webhook, Secret text)에만 둔다.
// Credential이 없거나 전송에 실패해도 빌드 결과는 바꾸지 않는다.
def notifyDiscord(String repo) {
    try {
        def commit = sh(script: 'git log -1 --format="%h %s" 2>/dev/null || true', returnStdout: true).trim()
        withCredentials([string(credentialsId: 'discord-ci-webhook', variable: 'DISCORD_WEBHOOK')]) {
            withEnv([
                "N_REPO=${repo}",
                "N_BRANCH=${env.BRANCH_NAME ?: env.SOURCE_BRANCH ?: '-'}",
                "N_RESULT=${currentBuild.currentResult}",
                "N_BUILD=${env.BUILD_NUMBER}",
                "N_DESC=${currentBuild.description ?: ''}",
                "N_DURATION=${currentBuild.durationString.replace(' and counting', '')}",
                "N_COMMIT=${commit}",
            ]) {
                sh(label: 'Discord 알림', script: '''
                    python3 - <<'PY' > discord-payload.json
import json, os, re
e = os.environ
r = e["N_RESULT"]
icon = {"SUCCESS": "✅", "FAILURE": "❌", "UNSTABLE": "⚠️", "ABORTED": "⏹️"}.get(r, "ℹ️")
color = {"SUCCESS": 0x2ECC71, "FAILURE": 0xE74C3C, "UNSTABLE": 0xF1C40F}.get(r, 0x95A5A6)
fields = [{"name": "브랜치", "value": e["N_BRANCH"], "inline": True},
          {"name": "소요 시간", "value": e["N_DURATION"] or "-", "inline": True}]
if e.get("N_DESC"):
    fields.append({"name": "대상", "value": e["N_DESC"][:200], "inline": False})
if e.get("N_COMMIT"):
    fields.append({"name": "커밋", "value": e["N_COMMIT"][:200], "inline": False})
# 빌드 로그(Jenkins 가 쓰는 파일 — 자격 증명 값은 이미 가려져 있음)에서 결과 줄 · 실패 단계 · 마지막 로그를 뽑는다
ESC = chr(27); NL = chr(10)
def clean(x):
    x = re.sub(re.escape(ESC + "[8mha:") + ".*?" + re.escape(ESC + "[0m"), "", x)
    x = re.sub(re.escape(ESC + "[") + "[0-9;]*[A-Za-z]", "", x)
    return x.replace("`" * 3, chr(39) * 3).rstrip()
lines = []
try:
    top, _, rest = e.get("JOB_NAME", "").partition("/")
    path = e["JENKINS_HOME"] + "/jobs/" + top + ("/branches/" + rest.replace("/", "%2F") if rest else "") + "/builds/" + e["N_BUILD"] + "/log"
    with open(path, encoding="utf-8", errors="replace") as f:
        lines = [clean(x) for x in f.read().splitlines()]
    lines = [x for x in lines if x.strip() and "Discord" not in x and "discord-payload" not in x]
except Exception:
    lines = []
desc = ""
POST = "Declarative: Post Actions"
cut = max([i for i, x in enumerate(lines) if POST in x] or [len(lines)])
body = lines[:cut]   # 알림 단계(post) 자신의 로그는 뺀다
if lines and r == "SUCCESS":
    done = [x for x in body if "반영:" in x] or [x for x in body if "갱신 완료" in x]
    if done:
        fields.append({"name": "결과", "value": done[-1][-300:], "inline": False})
elif lines and r in ("FAILURE", "UNSTABLE"):
    stages = [m.group(1) for m in (re.search(re.escape("[Pipeline] { (") + "(.*)[)]", x) for x in body) if m]
    skipped = [m.group(1) for m in (re.search("Stage " + chr(34) + "(.*)" + chr(34) + " skipped", x) for x in lines) if m]
    stages = [x for x in stages if not x.startswith("Declarative:") and x not in skipped]   # 건너뛴 단계는 실패 단계가 아니다
    if stages:
        fields.append({"name": "실패 단계", "value": stages[-1][:100], "inline": True})
    shown = [x[:200] for x in body if not x.startswith("[Pipeline]")][-25:]
    errs = [x[:200] for x in lines if x.startswith("ERROR:") or x.startswith("Finished:")][-2:]
    desc = "`" * 3 + NL + NL.join(shown + errs)[-3300:] + NL + "`" * 3
embed = {"title": f"{icon} {e['N_REPO']} #{e['N_BUILD']} {r}", "color": color, "fields": fields}
if desc:
    embed["description"] = desc
print(json.dumps({"username": "Jenkins", "embeds": [embed]}))
PY
                    code=$(curl -sS -m 10 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary @discord-payload.json "$DISCORD_WEBHOOK" || true)
                    rm -f discord-payload.json
                    echo "Discord 알림 HTTP ${code:-실패}"
                ''')
            }
        }
    } catch (err) {
        echo "Discord 알림 생략: ${err}"
    }
}
