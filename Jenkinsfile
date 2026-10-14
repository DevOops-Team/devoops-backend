pipeline {
    agent any

    options {
        skipDefaultCheckout(true)
        disableConcurrentBuilds()
        timestamps()
        timeout(time: 30, unit: 'MINUTES')
        buildDiscarder(logRotator(numToKeepStr: '20'))
    }

    parameters {
        string(name: 'INFRA_BRANCH', defaultValue: 'develop', description: '이미지 태그를 갱신할 인프라 브랜치')
        string(name: 'MANIFEST_DIR', defaultValue: 'k8s/portal/overlays/prod', description: '이미지 newTag를 갱신할 kustomization.yaml 디렉터리')
    }

    environment {
        MONGO_KUBECONFIG_CREDENTIALS = 'k8s-staging-mongo'
        IMAGE_NAME = 'ghcr.io/devoops-team/devoops-backend'
        INFRA_REPO = 'git@collabops.ai:devoops/devoops-infra.git'
        INFRA_CREDENTIALS = 'collabops-infra-write'
        GHCR_CREDENTIALS = 'ghcr-push'
    }

    stages {
        stage('Checkout') {
            steps {
                checkout scm
                script {
                    def commit = sh(script: 'git rev-parse --short=12 HEAD', returnStdout: true).trim()
                    env.IMAGE_TAG = "${env.BUILD_NUMBER}-${commit}"
                }
            }
        }

        stage('Check tools') {
            steps {
                sh '''
                    set -eu
                    go version
                    gcc --version
                    docker version
                    kubectl version --client
                    mongosh --version
                    kustomize version
                    git check-ref-format "refs/heads/$INFRA_BRANCH"
                '''
            }
        }

        stage('Vet') {
            steps {
                sh 'make vet'
            }
        }

        stage('Unit test') {
            steps {
                sh 'make test'
            }
        }

        stage('Integration test') {
            steps {
                withCredentials([file(credentialsId: env.MONGO_KUBECONFIG_CREDENTIALS,
                    variable: 'KUBECONFIG')]) {
                    sh 'bash scripts/test-k8s-integration.sh'
                }
            }
        }

        stage('Docker build') {
            steps {
                sh '''
                    set -eu
                    docker build -t "$IMAGE_NAME:$IMAGE_TAG" .
                '''
            }
        }

        stage('GHCR push') {
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
                    '''
                }
            }
        }

        stage('Update infra image') {
            steps {
                dir('infra-manifests') {
                    deleteDir()
                    git branch: params.INFRA_BRANCH,
                        credentialsId: env.INFRA_CREDENTIALS,
                        url: env.INFRA_REPO
                    sshagent(credentials: [env.INFRA_CREDENTIALS]) {
                        sh '''
                            set +x
                            set -eu
                            export GIT_SSH_COMMAND='ssh -o BatchMode=yes -o StrictHostKeyChecking=yes'
                            cd "$MANIFEST_DIR"
                            test -f kustomization.yaml
                            kustomize edit set image "$IMAGE_NAME=$IMAGE_NAME:$IMAGE_TAG"
                            rendered=$(mktemp)
                            trap 'rm -f "$rendered"' EXIT
                            kustomize build . > "$rendered"
                            grep -F "image: $IMAGE_NAME:$IMAGE_TAG" "$rendered"
                            git config user.name 'jenkins'
                            git config user.email 'jenkins@devoops.local'
                            git add -- kustomization.yaml
                            if git diff --cached --quiet; then
                                echo 'Backend image is already up to date.'
                                exit 0
                            fi
                            git commit -m "chore: update backend image to $IMAGE_TAG"
                            # Never force-push: concurrent infra changes cause a visible failure.
                            git push origin "HEAD:refs/heads/$INFRA_BRANCH"
                        '''
                    }
                }
            }
        }
    }
}
