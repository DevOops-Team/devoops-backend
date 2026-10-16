pipeline {
    agent any

    options {
        skipDefaultCheckout(true)
        disableConcurrentBuilds()
        timestamps()
        timeout(time: 60, unit: 'MINUTES')
        buildDiscarder(logRotator(numToKeepStr: '20'))
    }

    parameters {
        string(name: 'STAGING_VERIFY_CREDENTIALS', defaultValue: 'k8s-staging-backend-verify',
            description: 'staging Argo CD·Rollout·Deployment·Service·Pod 조회 및 backend port-forward kubeconfig')
        string(name: 'STAGING_SMOKE_CREDENTIALS', defaultValue: 'staging-backend-smoke',
            description: 'staging 전용 USER 테스트 계정 이메일·비밀번호 (Username with password)')
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
        stage('소스 가져오기') {
            // develop → 테스트 · 빌드 · staging 반영 / main → staging에서 검증한 같은 이미지를 prod로 (10-14 결정 A안)
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                script {
                    sh 'rm -f .deploy-digest .staging-infra-revision .staging-verified'
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

        stage('검사 도구 확인') {
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh '''
                    set -eu
                    for tool in git bash awk ssh docker python3; do
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

        stage('스테이징 검증 권한 확인') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                withCredentials([
                    file(credentialsId: params.STAGING_VERIFY_CREDENTIALS, variable: 'KUBECONFIG'),
                    usernamePassword(credentialsId: params.STAGING_SMOKE_CREDENTIALS,
                        usernameVariable: 'SMOKE_EMAIL', passwordVariable: 'SMOKE_PASSWORD')
                ]) {
                    sh 'python3 scripts/verify-staging.py --preflight'
                }
            }
        }

        stage('정적 검사') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh 'make vet'
            }
        }

        stage('단위 테스트') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh 'make test'
            }
        }

        stage('통합 테스트') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                withCredentials([file(credentialsId: env.MONGO_KUBECONFIG_CREDENTIALS,
                    variable: 'KUBECONFIG')]) {
                    sh 'bash scripts/test-k8s-integration.sh'
                }
            }
        }

        stage('이미지 빌드') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh '''
                    set -eu
                    docker build --pull -t "$IMAGE_NAME:$IMAGE_TAG" .
                '''
            }
        }

        stage('이미지 보안 검사') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh '''
                    trivy image --scanners vuln --severity HIGH,CRITICAL \
                        --exit-code 1 --no-progress "$IMAGE_NAME:$IMAGE_TAG"
                '''
            }
        }

        stage('GHCR 이미지 업로드') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                withCredentials([usernamePassword(credentialsId: env.GHCR_CREDENTIALS,
                    usernameVariable: 'GHCR_USERNAME', passwordVariable: 'GHCR_TOKEN')]) {
                    sh '''
                        set +x
                        set -eu
                        export DOCKER_CONFIG="$(mktemp -d)"
                        trap 'rm -rf "$DOCKER_CONFIG"' EXIT
                        printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
                        docker push "$IMAGE_NAME:$IMAGE_TAG"
                        # verified-tree 태그는 실제 staging 배포·API 검증 성공 후 발행한다.
                        docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE_NAME:$IMAGE_TAG" | sed 's/.*@//' > "$WORKSPACE/.deploy-digest"
                    '''
                }
            }
        }

        stage('운영 승격 이미지 조회') {
            // main: 새로 빌드하지 않는다 — develop에서 테스트 · 빌드 · staging 반영까지 끝난 같은 digest
            when { expression { env.DEPLOY_ENV == 'prod' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                withCredentials([usernamePassword(credentialsId: env.GHCR_CREDENTIALS,
                    usernameVariable: 'GHCR_USERNAME', passwordVariable: 'GHCR_TOKEN')]) {
                    sh '''
                        set +x
                        set -eu
                        export DOCKER_CONFIG="$(mktemp -d)"
                        trap 'rm -rf "$DOCKER_CONFIG"' EXIT
                        printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
                        if ! docker pull -q "$IMAGE_NAME:verified-tree-$TREE" >/dev/null; then
                            echo "staging에서 검증된 이미지가 없습니다: $IMAGE_NAME:verified-tree-$TREE"
                            echo "main 내용이 develop 빌드와 다릅니다. main에는 develop에서만 머지하고, develop 빌드가 성공한 뒤 머지하세요."
                            exit 1
                        fi
                        docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE_NAME:verified-tree-$TREE" | sed 's/.*@//' > "$WORKSPACE/.deploy-digest"
                    '''
                }
            }
        }

        stage('인프라 이미지 반영') {
            // CollabOps devoops-infra overlays/<staging|prod> → GitHub 미러 → Argo CD
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
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
                        if [ "$DEPLOY_ENV" = staging ]; then
                            git -C infra fetch -q origin main
                            REVISION="$(git -C infra rev-parse FETCH_HEAD)"
                            ACTUAL="$(git -C infra show "$REVISION:k8s/portal/overlays/staging/kustomization.yaml" |
                                awk -v mode=get -v img="$IMAGE_NAME" -f infra/ci/images.awk)"
                            [ "$ACTUAL" = "$DIGEST" ] || { echo 'staging 이미지가 다른 변경으로 교체되었습니다'; exit 1; }
                            printf '%s' "$REVISION" > "$WORKSPACE/.staging-infra-revision"
                        fi
                    '''
                }
            }
        }

        stage('스테이징 배포·백엔드 API 검증') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                withCredentials([
                    file(credentialsId: params.STAGING_VERIFY_CREDENTIALS, variable: 'KUBECONFIG'),
                    usernamePassword(credentialsId: params.STAGING_SMOKE_CREDENTIALS,
                        usernameVariable: 'SMOKE_EMAIL', passwordVariable: 'SMOKE_PASSWORD')
                ]) {
                    timeout(time: 15, unit: 'MINUTES') {
                        sh 'python3 scripts/verify-staging.py'
                    }
                }
            }
        }

        stage('검증 완료 이미지 태그 발행') {
            // 배포·동일 digest·Pod·API 검증 성공 시에만 verified-tree 태그를 만든다.
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                withCredentials([usernamePassword(credentialsId: env.GHCR_CREDENTIALS,
                    usernameVariable: 'GHCR_USERNAME', passwordVariable: 'GHCR_TOKEN')]) {
                    sh '''
                        set +x
                        set -eu
                        export DOCKER_CONFIG="$(mktemp -d)"
                        trap 'rm -rf "$DOCKER_CONFIG"' EXIT
                        printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
                        EXPECTED="$(cat "$WORKSPACE/.deploy-digest")"
                        [ "$(cat "$WORKSPACE/.staging-verified")" = "$IMAGE_NAME@$EXPECTED" ]
                        # Tag the exact pushed digest, even if a local tag changed meanwhile.
                        docker pull -q "$IMAGE_NAME@$EXPECTED" >/dev/null
                        docker tag "$IMAGE_NAME@$EXPECTED" "$IMAGE_NAME:verified-tree-$TREE"
                        docker push "$IMAGE_NAME:verified-tree-$TREE"
                    '''
                }
            }
        }
    }

    post {
        success {
            script {
                echo env.DEPLOY_ENV == 'staging' ?
                    'staging 배포·digest·Pod·백엔드 API 검증 및 검증 태그 발행 완료' :
                    'prod 매니페스트 갱신 완료. 실제 prod 배포 상태는 Argo CD에서 확인하세요.'
            }
        }
        always {
            notifyDiscord('devoops-backend')
        }
    }
}

// 알림은 최종 post/always 한 곳에서만 전송한다. 실패해도 원래 빌드 결과를 바꾸지 않는다.
def notifyDiscord(String repo) {
    try {
        withCredentials([string(credentialsId: 'discord-ci-webhook', variable: 'DISCORD_WEBHOOK')]) {
            withEnv([
                "N_REPO=${repo}",
                "N_BRANCH=${env.BRANCH_NAME ?: env.SOURCE_BRANCH ?: '-'}",
                "N_ENV=${env.DEPLOY_ENV ?: '-'}",
                "N_RESULT=${currentBuild.currentResult}",
                "N_BUILD=${env.BUILD_NUMBER}",
                "N_BUILD_URL=${env.BUILD_URL ?: ''}",
                "N_DURATION=${currentBuild.durationString.replace(' and counting', '')}",
                "N_TREE=${env.TREE ?: '-'}",
                "N_LAST_STAGE=${env.LAST_STAGE ?: '파이프라인 시작'}",
                "N_START_MILLIS=${currentBuild.startTimeInMillis}",
            ]) {
                sh(label: 'Discord 알림', script: '''
                    set +x
                    python3 scripts/notify-discord.py
                ''')
            }
        }
    } catch (err) {
        echo 'Discord 알림을 완료하지 못했습니다. Credential·Python·네트워크 설정을 확인하세요. 빌드 결과는 유지합니다.'
    }
}
