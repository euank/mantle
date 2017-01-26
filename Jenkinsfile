#!groovy

properties([
    buildDiscarder(logRotator(daysToKeepStr: '20', numToKeepStr: '30')),

    [$class: 'GithubProjectProperty',
     projectUrlStr: 'https://github.com/coreos/mantle'],

    [$class: 'CopyArtifactPermissionProperty',
     projectNames: '*'],

    parameters([
        choice(name: 'GOARCH',
               choices: "amd64\narm64",
               description: 'target architecture for building binaries')
    ]),

    pipelineTriggers([pollSCM('H/15 * * * *')])
])

node('docker') {
    stage('SCM') {
      // In PR branches, don't automatically build if these files are modified
      for(String file : ['build', 'test', 'env', 'cover']) {
          readTrusted file
      }
      checkout scm
    }

    stage('Build') {
        sh "docker run --rm -e CGO_ENABLED=1 -e GOARCH=${params.GOARCH} -u \"\$(id -u):\$(id -g)\" -v /etc/passwd:/etc/passwd:ro -v /etc/group:/etc/group:ro -v \"\$PWD\":/usr/src/myapp -w /usr/src/myapp golang:1.7.1 ./build"
    }

    stage('Test') {
        sh 'docker run --rm -u "$(id -u):$(id -g)" -v /etc/passwd:/etc/passwd:ro -v /etc/group:/etc/group:ro -v "$PWD":/usr/src/myapp -w /usr/src/myapp golang:1.7.1 ./test'
    }

    stage('Post-build') {
        if (env.JOB_BASE_NAME == "master-builder") {
            archiveArtifacts artifacts: 'bin/**', fingerprint: true, onlyIfSuccessful: true
        }
    }
}
