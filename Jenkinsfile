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
        string(name: 'PROD_VERIFY_CREDENTIALS', defaultValue: 'k8s-prod-readonly',
            description: 'vdi-prod Deployment·Rollout·Pod 조회 전용 kubeconfig Secret file')
    }

    environment {
        IMAGE_NAME = 'ghcr.io/devoops-team/devoops-backend'
        PROD_HEALTHCHECK_URL = 'https://devoops.site'
        // devoops-manifests는 CollabOps 원본에 쓴다 (GitHub 미러에 쓰면 미러가 깨짐) — ci/update-image.sh
        MANIFESTS_REPO = 'git@collabops.ai:devoops/devoops-manifests.git'
        INFRA_SSH_KEY = '/var/jenkins_home/.ssh/id_ed25519_collabops_ci'
        GHCR_CREDENTIALS = 'ghcr-push'
    }

    stages {
        stage('소스 가져오기 [develop·main]') {
            // develop → 테스트 · 빌드 · staging 매니페스트 반영 / main → 사람이 staging을 확인한 같은 이미지를 prod로 (10-14 결정 A안)
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                script {
                    sh 'rm -f .deploy-digest .staging-infra-revision .staging-verified release.json'
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
                    env.SOURCE_COMMIT = sh(script: 'git rev-parse HEAD', returnStdout: true).trim()
                    env.SOURCE_TREE = sh(script: 'git rev-parse "HEAD^{tree}"', returnStdout: true).trim()
                    env.TREE = env.SOURCE_TREE.substring(0, 12) // 알림용 소스 지문, 이미지 태그가 아님
                    env.IMAGE_TAG = "${env.BUILD_NUMBER}-${commit}"
                    currentBuild.description = "${branch} → ${env.DEPLOY_ENV} (tree ${env.TREE})"
                }
            }
        }

        stage('도구 확인 [develop·main]') {
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh '''
                    set -eu
                    for tool in git bash awk ssh docker python3; do
                        command -v "$tool" >/dev/null
                    done
                    test -r "$INFRA_SSH_KEY"
                    docker version
                    kubectl version --client
                '''
                script {
                    if (env.DEPLOY_ENV == 'staging') {
                        sh '''
                    set -eu
                    go version
                    gcc --version
                    trivy --version
                        '''
                    } else {
                        // Check credential/access before changing the production manifest.
                        withCredentials([file(credentialsId: params.PROD_VERIFY_CREDENTIALS, variable: 'KUBECONFIG')]) {
                            sh '''
                                set -eu
                                kubectl --request-timeout=10s -n vdi-prod get deployment/backend -o name
                                kubectl --request-timeout=10s -n vdi-prod get pods -l app=backend -o name
                            '''
                        }
                    }
                }
            }
        }

        stage('정적 검사 [develop]') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh 'make vet'
            }
        }

        stage('단위 테스트 [develop]') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh 'make test'
                sh 'python3 scripts/test-main-health.py'
                sh 'python3 scripts/test-prod-deployment.py'
                sh 'python3 scripts/test-image-release.py'
            }
        }

        stage('Mongo 통합 테스트 [develop]') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh 'env -u TEST_MONGO_URI bash scripts/test-integration.sh'
            }
        }

        stage('이미지 빌드 [develop]') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh '''
                    set -eu
                    docker build --pull \
                        --label "org.opencontainers.image.revision=$SOURCE_COMMIT" \
                        --label "io.devoops.source-tree=$SOURCE_TREE" \
                        -t "$IMAGE_NAME:$IMAGE_TAG" .
                '''
            }
        }

        stage('Trivy 보안 검사 [develop]') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                sh '''
                    trivy image --scanners vuln --severity HIGH,CRITICAL \
                        --exit-code 1 --no-progress "$IMAGE_NAME:$IMAGE_TAG"
                '''
            }
        }

        stage('이미지 업로드 [develop]') {
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
                        # 업로드한 digest를 staging 배포와 릴리스 기록에 사용한다.
                        docker inspect --format '{{index .RepoDigests 0}}' "$IMAGE_NAME:$IMAGE_TAG" | sed 's/.*@//' > "$WORKSPACE/.deploy-digest"
                    '''
                }
            }
        }

        stage('이미지 기동 검사 [develop]') {
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                timeout(time: 10, unit: 'MINUTES') {
                    sh 'python3 scripts/check-image-startup.py'
                }
            }
        }

        stage('이미지 조회 [main]') {
            // main: 새로 빌드하지 않는다 — develop에서 테스트 · 빌드 · staging 반영까지 끝난 같은 digest
            when { expression { env.DEPLOY_ENV == 'prod' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                dir('promotion-manifests') {
                    deleteDir()
                    sh '''
                        set -eu
                        export GIT_SSH_COMMAND="ssh -i $INFRA_SSH_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes"
                        git clone -q --depth 1 -b main "$MANIFESTS_REPO" .
                    '''
                }
                sh 'python3 scripts/image-release.py prepare-prod'
                withCredentials([usernamePassword(credentialsId: env.GHCR_CREDENTIALS,
                    usernameVariable: 'GHCR_USERNAME', passwordVariable: 'GHCR_TOKEN')]) {
                    sh '''
                        set +x
                        set -eu
                        export DOCKER_CONFIG="$(mktemp -d)"
                        trap 'rm -rf "$DOCKER_CONFIG"' EXIT
                        printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
                        DIGEST="$(cat "$WORKSPACE/.deploy-digest")"
                        docker pull -q "$IMAGE_NAME@$DIGEST" >/dev/null
                        python3 scripts/image-release.py verify-prod
                    '''
                }
            }
        }

        stage('매니페스트 갱신 [develop·main]') {
            // CollabOps devoops-manifests overlays/<staging|prod> → GitHub 미러 → Argo CD
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
                        git clone -q --depth 1 -b main "$MANIFESTS_REPO" manifests
                        test -f "manifests/overlays/$DEPLOY_ENV/kustomization.yaml"
                        rc=0
                        bash manifests/ci/update-image.sh "$DEPLOY_ENV" "$IMAGE_NAME" "$DIGEST" "jenkins #$BUILD_NUMBER source-$SOURCE_COMMIT" || rc=$?
                        # 0 = 반영, 3 = 이미 같은 이미지
                        [ "$rc" = 0 ] || [ "$rc" = 3 ]
                        if [ "$DEPLOY_ENV" = staging ]; then
                            git -C manifests fetch -q origin main
                            REVISION="$(git -C manifests rev-parse FETCH_HEAD)"
                            ACTUAL="$(git -C manifests show "$REVISION:overlays/staging/kustomization.yaml" |
                                awk -v mode=get -v img="$IMAGE_NAME" -f manifests/ci/images.awk)"
                            [ "$ACTUAL" = "$DIGEST" ] || { echo 'staging 이미지가 다른 변경으로 교체되었습니다'; exit 1; }
                            printf '%s' "$REVISION" > "$WORKSPACE/.staging-infra-revision"
                        fi
                    '''
                }
                script {
                    if (env.DEPLOY_ENV == 'staging') {
                        sh 'python3 scripts/image-release.py record-staging'
                        archiveArtifacts artifacts: 'release.json,.deploy-digest', fingerprint: true
                    }
                }
            }
        }

        stage('운영 HTTP 확인 [main]') {
            when { expression { env.DEPLOY_ENV == 'prod' } }
            steps {
                script { env.LAST_STAGE = env.STAGE_NAME }
                timeout(time: 45, unit: 'MINUTES') {
                    withCredentials([file(credentialsId: params.PROD_VERIFY_CREDENTIALS, variable: 'KUBECONFIG')]) {
                        sh 'python3 scripts/check-prod-health.py'
                    }
                }
            }
        }

    }

    post {
        success {
            script {
                echo env.DEPLOY_ENV == 'staging' ?
                    '이미지 기동 검사·staging 매니페스트 갱신·release.json 기록 완료. staging 검증 후 main에 머지하면 digest를 자동 조회해 승격합니다.' :
                    '목표 digest의 prod 배포 완료·전체 Pod Ready·운영 HTTP 확인 성공.'
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
