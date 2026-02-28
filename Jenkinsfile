pipeline {
    agent { label 'brain' }

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
                branch 'main' // typically you'd deploy from main
            }
            steps {
                script {
                    def target = "${DEPLOY_USER}@${DEPLOY_SERVER}"

                    // Ensure target directories exist
                    sh "ssh ${target} 'mkdir -p ${APP_DIR} ${LOG_DIR}'"
                    sh "ssh ${target} 'sudo mkdir -p ${CFG_DIR} && sudo chown -R ${DEPLOY_USER}:${DEPLOY_USER} ${CFG_DIR}'"

                    // Stop the existing background server if running
                    sh "ssh ${target} 'pkill -f \"agentic-go --serve\" || true'"

                    // Deploy binary and config templates
                    sh "scp agentic-go ${target}:${APP_DIR}/agentic-go"
                    sh "scp config.example.json ${target}:${CFG_DIR}/config.example.json"
                    sh "scp mcp.example.json ${target}:${CFG_DIR}/mcp.example.json"

                    // Make it executable
                    sh "ssh ${target} 'chmod +x ${APP_DIR}/agentic-go'"

                    // Start the service in the background (nohup)
                    sh "ssh ${target} 'cd ${APP_DIR} && nohup ./agentic-go --serve > ${LOG_DIR}/agentic.log 2>&1 &'"
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
