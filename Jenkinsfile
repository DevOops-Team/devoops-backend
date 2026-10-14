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
        // This job needs a dedicated Linux agent: the test DB binds port 27018.
        COMPOSE_PROJECT_NAME = 'devoops-backend-ci'
    }

    stages {
        stage('Checkout') {
            steps {
                checkout scm
            }
        }

        stage('Check tools') {
            steps {
                sh '''
                    set -eu
                    go version
                    gcc --version
                    docker version
                    docker compose version
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
                sh 'make test-integration'
            }
            post {
                always {
                    sh 'docker compose -f compose.test.yaml down -v --remove-orphans'
                }
            }
        }

        stage('Docker build') {
            steps {
                sh '''
                    set -eu
                    COMMIT_SHORT=$(git rev-parse --short=12 HEAD)
                    docker build -t "vdi-api:${BUILD_NUMBER}-${COMMIT_SHORT}" .
                '''
            }
        }
    }
}
