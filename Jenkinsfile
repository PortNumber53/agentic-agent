pipeline {
    agent any

    environment {
        DEPLOY_USER = 'grimlock'
        DEPLOY_SERVER = 'crash'
        APP_DIR = '/var/www/vhosts/agentic.dev.portnumber53.com'
        LOG_DIR = '/var/www/vhosts/agentic.dev.portnumber53.com/logs'
        CFG_DIR = '/etc/agentic'
    }

    stages {
        stage('Build') {
            steps {
                sh 'go build -o agentic-go main.go server.go'
            }
        }

        stage('Deploy & Restart') {
            when {
                branch 'master' // typically you'd deploy from master
            }
            steps {
                script {
                    def target = "${DEPLOY_USER}@${DEPLOY_SERVER}"

                    // Ensure target directories exist
                    sh "ssh ${target} 'sudo mkdir -p ${APP_DIR} ${LOG_DIR} && sudo chown -R ${DEPLOY_USER}:${DEPLOY_USER} ${APP_DIR}'"
                    sh "ssh ${target} 'sudo mkdir -p ${CFG_DIR} && sudo chown -R ${DEPLOY_USER}:${DEPLOY_USER} ${CFG_DIR}'"

                    // Stop the existing background server if running
                    sh "ssh ${target} 'sudo systemctl stop agentic.service || true'"

                    // Deploy binary, config templates, and service file
                    sh "scp agentic-go ${target}:${APP_DIR}/agentic-go"
                    sh "scp config.example.json ${target}:${CFG_DIR}/config.example.json"
                    sh "scp mcp.example.json ${target}:${CFG_DIR}/mcp.example.json"
                    sh "scp agentic.service ${target}:${APP_DIR}/agentic.service"
                    sh "scp Dockerfile.webdev ${target}:${APP_DIR}/Dockerfile.webdev"

                    // Install service and make it executable
                    sh "ssh ${target} 'chmod +x ${APP_DIR}/agentic-go'"
                    sh "ssh ${target} 'sudo mv ${APP_DIR}/agentic.service /etc/systemd/system/agentic.service'"
                    sh "ssh ${target} 'sudo chown root:root /etc/systemd/system/agentic.service'"

                    // Build local docker image
                    sh "ssh ${target} 'cd ${APP_DIR} && docker build -f Dockerfile.webdev -t agentic-webdev:latest .'"

                    // Start the service
                    sh "ssh ${target} 'sudo systemctl daemon-reload'"
                    sh "ssh ${target} 'sudo systemctl enable agentic.service'"
                    sh "ssh ${target} 'sudo systemctl start agentic.service'"

                    // Fix log file ownership since systemd creates append-logs as root
                    sh "ssh ${target} 'sudo chown -R ${DEPLOY_USER}:${DEPLOY_USER} ${LOG_DIR}'"
                }
            }
        }
    }

    post {
        success {
            echo "Successfully deployed Agentic server to ${DEPLOY_SERVER}!"
        }
        failure {
            echo "Deployment failed."
        }
    }
}
