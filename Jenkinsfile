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
                    def branch = (scmVars.GIT_BRANCH ?: '').replaceFirst(/^origin\//, '')
                    if (branch == 'develop') {
                        env.DEPLOY_ENV = 'staging'
                    } else if (branch == 'main') {
                        env.DEPLOY_ENV = 'prod'
                    } else {
                        error("develop · main만 배포합니다 (받은 브랜치: ${branch})")
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
            when { expression { env.DEPLOY_ENV == 'staging' } }
            steps {
                sh '''
                    set -eu
                    go version
                    gcc --version
                    docker version
                    kubectl version --client
                    mongosh --version
                '''
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
                    docker build -t "$IMAGE_NAME:$IMAGE_TAG" .
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
                        # main 머지 때 이 tree 태그로 "staging에서 검증한 그 이미지"를 찾는다
                        docker tag "$IMAGE_NAME:$IMAGE_TAG" "$IMAGE_NAME:tree-$TREE"
                        docker push "$IMAGE_NAME:tree-$TREE"
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
                        DIGEST="$(cat "$WORKSPACE/.deploy-digest")"
                        case "$DIGEST" in sha256:*) ;; *) echo "digest 없음"; exit 1 ;; esac
                        export GIT_SSH_COMMAND="ssh -i $INFRA_SSH_KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
                        git clone -q --depth 1 -b main "$INFRA_REPO" infra
                        rc=0
                        bash infra/ci/update-image.sh "$DEPLOY_ENV" "$IMAGE_NAME" "$DIGEST" "jenkins #$BUILD_NUMBER tree-$TREE" || rc=$?
                        # 0 = 반영, 3 = 이미 같은 이미지
                        [ "$rc" = 0 ] || [ "$rc" = 3 ]
                    '''
                }
            }
        }
    }
}
